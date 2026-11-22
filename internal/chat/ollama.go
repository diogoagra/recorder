package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"recorder/internal/storage"
)

// EmbeddingModel é o modelo usado na busca por relevância do chat:
// multilíngue (português e inglês) e pequeno (~600 MB).
const EmbeddingModel = "embeddinggemma"

// Sem nenhuma resposta do Ollama por este tempo, a chamada é abandonada. A
// espera inclui carregar o modelo e ler um contexto grande (~370 tokens/s).
const idleTimeout = 3 * time.Minute

type OllamaClient struct {
	Endpoint string
	Model    string

	mu       sync.Mutex
	thinking map[string]bool // modelos que têm o modo de raciocínio
}

// DefaultOllamaEndpoint é onde o Ollama atende quando instalado normalmente.
// É 127.0.0.1, e não localhost: o Ollama escuta em IPv4, e localhost vai
// primeiro para o IPv6 (::1), onde outro programa do computador pode estar na
// mesma porta e receberia o texto das reuniões.
const DefaultOllamaEndpoint = "http://127.0.0.1:11434"

func NewOllamaClient(endpoint, model string) *OllamaClient {
	if endpoint == "" {
		endpoint = DefaultOllamaEndpoint
	}
	if model == "" {
		model = "gemma4:latest" // default model
	}
	return &OllamaClient{Endpoint: endpoint, Model: model}
}

// SetModel troca o modelo usado a partir da próxima chamada.
func (c *OllamaClient) SetModel(model string) {
	c.mu.Lock()
	c.Model = model
	c.mu.Unlock()
}

// CurrentModel é o modelo em uso. As configurações podem trocá-lo com o app
// aberto (SetModel), então quem roda em outra goroutine lê por aqui.
func (c *OllamaClient) CurrentModel() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Model
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model    string                 `json:"model"`
	Messages []ChatMessage          `json:"messages"`
	Stream   bool                   `json:"stream"`
	Think    *bool                  `json:"think,omitempty"`
	Format   any                    `json:"format,omitempty"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

type ChatResponse struct {
	Message ChatMessage `json:"message"`
	Done    bool        `json:"done"`
	Error   string      `json:"error,omitempty"`
}

const answerSystemPrompt = `Você é um assistente estritamente analítico e utilitário. Responda sempre em português do Brasil (pt-BR).
REGRA ABSOLUTA: Sua única função é extrair respostas EXCLUSIVAMENTE do texto de contexto (resumo e transcrições) fornecido. Você está terminantemente proibido de usar conhecimento prévio, conhecimento externo, adivinhar, inferir ou criar hipóteses (ex: não fale sobre VPNs, firewalls, scripts ou procedimentos padrão a menos que estejam LITERALMENTE descritos no texto do contexto).
REGRA DE FALHA: Se a resposta exata para a pergunta não estiver contida de forma explícita e clara no texto fornecido, você deve responder EXATAMENTE e APENAS com a seguinte frase: 'Não tenho dados suficientes para responder a esta pergunta.' Não adicione mais nenhuma palavra ou desculpa.
RESPOSTA: responda com as suas próprias palavras, de forma organizada (em tópicos quando houver vários pontos), sem copiar as falas inteiras da transcrição.
MOMENTOS: cada fala da transcrição começa com o momento do áudio entre colchetes, como "[12:34] Pessoa 3: ...". No fim de cada informação da resposta, coloque entre colchetes o momento da fala de onde ela veio, por exemplo: "A cópia do banco deve ficar pronta amanhã [08:30]." Use colchetes apenas para momentos.
FORMATAÇÃO: NÃO utilize formatação matemática em LaTeX, símbolos matemáticos, ou blocos complexos (como $\rightarrow$). Use apenas texto simples. Para setas, use '->'.
`

// Answer responde à pergunta usando só o contexto dado (resumo e trechos da
// transcrição) e o histórico da conversa. Com onText, a resposta chega aos
// poucos: onText recebe o texto acumulado a cada pedaço.
func (c *OllamaClient) Answer(question, contextText string, history []storage.ChatMessage, onText func(string)) (string, error) {
	return c.chat([]ChatMessage{
		{Role: "system", Content: answerSystemPrompt},
		{Role: "user", Content: answerContent(question, contextText, history)},
	}, map[string]interface{}{
		"temperature": 0.0,
		"top_k":       1,
		"top_p":       0.0,
		"num_ctx":     16384,
	}, nil, onText)
}

// answerContent junta o contexto, o histórico da conversa e a pergunta na
// mensagem que vai para a IA, junto com answerSystemPrompt.
func answerContent(question, contextText string, history []storage.ChatMessage) string {
	var user strings.Builder
	user.WriteString("Aqui está o contexto no qual você deve se basear 100%:\n\n")
	user.WriteString(contextText)
	if len(history) > 0 {
		user.WriteString("--- HISTÓRICO DA CONVERSA ---\n")
		for _, h := range history {
			role := "Usuário"
			if h.Role == "assistant" {
				role = "Assistente"
			}
			fmt.Fprintf(&user, "[%s]: %s\n", role, h.Content)
		}
		user.WriteString("--------------------\n\n")
	}
	user.WriteString("Com base ESTRITAMENTE E APENAS nas informações acima, responda à seguinte solicitação: ")
	user.WriteString(question)
	return user.String()
}

// ContextChars é quanto texto de transcrição vai em cada chamada. O contexto
// é de 16k tokens (num_ctx) e em português cada token tem uns 3-4 caracteres;
// o resto fica para as instruções e para a resposta.
func (c *OllamaClient) ContextChars() int {
	return 24000
}

// AskRaw is used for generic non-chat tasks like summarization or title generation
func (c *OllamaClient) AskRaw(systemPrompt string, userContent string) (string, error) {
	return c.StreamRaw(systemPrompt, userContent, nil)
}

// StreamRaw é como AskRaw, mas com onText recebendo o texto acumulado
// enquanto ele é escrito.
func (c *OllamaClient) StreamRaw(systemPrompt string, userContent string, onText func(string)) (string, error) {
	return c.chat([]ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userContent},
	}, rawOptions(), nil, onText)
}

// AskJSON pede uma resposta no formato do schema (JSON Schema) e a decodifica
// em out. O Ollama garante que a resposta segue o schema.
func (c *OllamaClient) AskJSON(systemPrompt, userContent string, schema any, out any) error {
	options := rawOptions()
	options["temperature"] = 0.0
	text, err := c.chat([]ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userContent},
	}, options, schema, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return fmt.Errorf("resposta do Ollama fora do formato pedido: %w", err)
	}
	return nil
}

func rawOptions() map[string]interface{} {
	return map[string]interface{}{
		"temperature": 0.3,
		"top_k":       10,
		"top_p":       0.5,
		"num_ctx":     16384,
	}
}

// chat manda as mensagens ao Ollama e devolve a resposta. Ela sempre vem aos
// poucos (stream), o que permite mostrar o texto enquanto é escrito e
// desistir se o Ollama parar de responder.
//
// O raciocínio do modelo (think) fica desligado: medido num resumo de 24
// minutos, gastava ~900 tokens a mais sem melhorar o resultado.
func (c *OllamaClient) chat(messages []ChatMessage, options map[string]interface{}, format any, onText func(string)) (string, error) {
	model := c.CurrentModel()
	request := ChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   true,
		Format:   format,
		Options:  options,
	}
	// Só modelos com o modo de raciocínio aceitam o parâmetro
	if c.supportsThinking(model) {
		off := false
		request.Think = &off
	}
	body, err := json.Marshal(request)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchdog := time.AfterFunc(idleTimeout, cancel)
	defer watchdog.Stop()

	req, err := http.NewRequestWithContext(ctx, "POST", c.Endpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("o Ollama não respondeu em %v", idleTimeout)
		}
		return "", fmt.Errorf("falha ao conectar ao Ollama (ele está rodando?): %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("erro ollama %d: %s", resp.StatusCode, string(msg))
	}

	var full strings.Builder
	dec := json.NewDecoder(resp.Body)
	for {
		var chunk ChatResponse
		if err := dec.Decode(&chunk); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if ctx.Err() != nil {
				return "", fmt.Errorf("o Ollama parou de responder por mais de %v", idleTimeout)
			}
			return "", err
		}
		watchdog.Reset(idleTimeout)
		if chunk.Error != "" {
			return "", fmt.Errorf("erro ollama: %s", chunk.Error)
		}
		if chunk.Message.Content != "" {
			full.WriteString(chunk.Message.Content)
			if onText != nil {
				onText(full.String())
			}
		}
		if chunk.Done {
			break
		}
	}
	return full.String(), nil
}

func (c *OllamaClient) supportsThinking(model string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if supported, ok := c.thinking[model]; ok {
		return supported
	}
	body, _ := json.Marshal(map[string]string{"model": model})
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(c.Endpoint+"/api/show", "application/json", bytes.NewReader(body))
	if err != nil {
		return false // tenta de novo na próxima chamada
	}
	defer resp.Body.Close()
	var show struct {
		Capabilities []string `json:"capabilities"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&show) != nil {
		return false
	}
	supported := false
	for _, capability := range show.Capabilities {
		if capability == "thinking" {
			supported = true
		}
	}
	if c.thinking == nil {
		c.thinking = map[string]bool{}
	}
	c.thinking[model] = supported
	return supported
}

// Embed devolve a "impressão" de cada texto (embedding), usada para achar os
// trechos da transcrição mais relacionados a uma pergunta.
func (c *OllamaClient) Embed(texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": EmbeddingModel, "input": texts})
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: idleTimeout}
	resp, err := client.Post(c.Endpoint+"/api/embed", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("falha ao conectar ao Ollama (ele está rodando?): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("erro ollama %d: %s", resp.StatusCode, string(msg))
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("o Ollama devolveu %d embeddings para %d textos", len(out.Embeddings), len(texts))
	}
	return out.Embeddings, nil
}

// EnsureModel baixa o modelo no Ollama se ele ainda não estiver lá. progress
// recebe a porcentagem baixada.
func (c *OllamaClient) EnsureModel(name string, progress func(percent int)) error {
	body, _ := json.Marshal(map[string]string{"model": name})
	resp, err := http.Post(c.Endpoint+"/api/show", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("falha ao conectar ao Ollama (ele está rodando?): %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}

	body, _ = json.Marshal(map[string]any{"model": name, "stream": true})
	resp, err = http.Post(c.Endpoint+"/api/pull", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("falha ao baixar %s: %w", name, err)
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var status struct {
			Status    string `json:"status"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
			Error     string `json:"error"`
		}
		if err := dec.Decode(&status); err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("o download de %s terminou sem confirmação", name)
			}
			return err
		}
		if status.Error != "" {
			return fmt.Errorf("falha ao baixar %s: %s", name, status.Error)
		}
		if status.Total > 0 && progress != nil {
			progress(int(status.Completed * 100 / status.Total))
		}
		if status.Status == "success" {
			return nil
		}
	}
}
