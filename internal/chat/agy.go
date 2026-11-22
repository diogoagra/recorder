package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"recorder/internal/storage"
)

// DefaultAgyModel é o modelo usado pelo agy enquanto o usuário não escolher outro.
const DefaultAgyModel = "gemini-4-argon-medium"

// AgyClient usa um modelo na nuvem (Gemini, Claude...) pelo agy, o CLI do
// Antigravity (ver cli.go). Cada chamada leva uns 5-15 s e manda junto ~20k
// tokens de instruções do próprio agy.
//
// O agy é um agente, com ferramentas (comandos, arquivos, internet), e não
// tem como desligá-las. Para o texto das reuniões não conseguir usá-las, ele
// roda numa pasta vazia, com --sandbox e sem --dangerously-skip-permissions:
// no modo não interativo o agy nega toda ferramenta que pede permissão e
// termina sem resposta, o que aqui vira erro.
type AgyClient struct {
	Path  string // executável do agy; vazio se não foi encontrado
	Model string
}

func NewAgyClient(path, model string) *AgyClient {
	if model == "" {
		model = DefaultAgyModel
	}
	return &AgyClient{Path: path, Model: model}
}

// FindAgy procura o executável do agy; devolve "" se não encontrar.
func FindAgy() string {
	return findCLI("agy", "~/.gemini/bin")
}

func (c *AgyClient) ContextChars() int {
	return cliContextChars
}

func (c *AgyClient) Answer(question, contextText string, history []storage.ChatMessage, onText func(string)) (string, error) {
	return c.run(answerSystemPrompt, answerContent(question, contextText, history), nil, onText)
}

func (c *AgyClient) AskRaw(systemPrompt, userContent string) (string, error) {
	return c.run(systemPrompt, userContent, nil, nil)
}

func (c *AgyClient) StreamRaw(systemPrompt, userContent string, onText func(string)) (string, error) {
	return c.run(systemPrompt, userContent, nil, onText)
}

// AskJSON usa o --json-schema do agy, que faz a resposta seguir o schema.
func (c *AgyClient) AskJSON(systemPrompt, userContent string, schema any, out any) error {
	text, err := c.run(systemPrompt, userContent, schema, nil)
	if err != nil {
		return err
	}
	return decodeJSONAnswer("agy", text, out)
}

func (c *AgyClient) run(system, user string, schema any, onText func(string)) (string, error) {
	return runCLI("agy", c.Path, agyDialect{}, c.Model, system, user, schema, onText)
}

type agyDialect struct{}

// O agy não tem opção para as instruções: elas vão no começo da mensagem.
func (agyDialect) args(model, systemFile string, schema []byte) []string {
	args := []string{"--model", model, "--sandbox", "--disable-slash-commands",
		"--print-timeout", cliTimeout.String(),
		"--input-format", "stream-json", "--output-format", "stream-json"}
	if schema != nil {
		args = append(args, "--json-schema", string(schema))
	}
	return append(args, "-p", "")
}

// A mensagem vai como um evento "user" do --input-format stream-json.
func (agyDialect) stdin(system, user string) ([]byte, error) {
	input, err := json.Marshal(map[string]any{"event": "user", "message": map[string]any{
		"content": system + "\n\n=== CONTEÚDO ===\n" + user,
	}})
	return append(input, '\n'), err
}

// Eventos do --output-format stream-json do agy que interessam aqui.
type agyEvent struct {
	Event      string `json:"event"`
	StepUpdate struct {
		StepType  string `json:"step_type"`
		TextDelta string `json:"text_delta"`
	} `json:"step_update"`
	Result *struct {
		Status        string `json:"status"`
		Response      string `json:"response"`
		Error         string `json:"error"`
		DeniedActions []struct {
			Action string `json:"action"`
		} `json:"denied_actions"`
	} `json:"result"`
}

func (agyDialect) event(line []byte) (string, bool, string, error) {
	var ev agyEvent
	if json.Unmarshal(line, &ev) != nil {
		return "", false, "", nil
	}
	if ev.Event == "step_update" && ev.StepUpdate.StepType == "agent_response" {
		return ev.StepUpdate.TextDelta, false, "", nil
	}
	if ev.Event != "result" || ev.Result == nil {
		return "", false, "", nil
	}
	r := ev.Result
	if r.Status != "SUCCESS" {
		return "", true, "", fmt.Errorf("%s", strings.TrimSpace(r.Error))
	}
	// O texto tentou usar uma ferramenta: o agy a negou e ficou sem resposta
	if strings.TrimSpace(r.Response) == "" && len(r.DeniedActions) > 0 {
		return "", true, "", fmt.Errorf("o agy tentou usar uma ferramenta (%s), o que o Recorder não permite, e ficou sem resposta", r.DeniedActions[0].Action)
	}
	return "", true, r.Response, nil
}

// AgyModels lista os modelos que o agy oferece ("agy models").
func AgyModels(path string) ([]ModelOption, error) {
	if path == "" {
		return nil, fmt.Errorf("o agy não foi encontrado neste computador")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "models")
	cmd.Dir = os.TempDir()
	hideConsole(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("erro ao listar os modelos do agy: %w", err)
	}
	return parseAgyModels(string(out)), nil
}

// parseAgyModels lê as linhas "id<TAB>nome" da saída de "agy models".
func parseAgyModels(out string) []ModelOption {
	models := []ModelOption{}
	for _, line := range strings.Split(out, "\n") {
		id, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || id == "" || strings.Contains(id, " ") {
			continue
		}
		models = append(models, ModelOption{ID: id, Name: strings.TrimSpace(name)})
	}
	return models
}
