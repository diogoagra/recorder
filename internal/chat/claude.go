package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"recorder/internal/storage"
)

// DefaultClaudeModel é o modelo usado pelo claude enquanto o usuário não
// escolher outro: o Sonnet equilibra qualidade, velocidade e o consumo da
// assinatura.
const DefaultClaudeModel = "sonnet"

// ClaudeClient usa os modelos da Anthropic pelo claude, o CLI do Claude Code
// (ver cli.go), com o login dele (assinatura ou chave de API). Cada chamada
// leva uns 3-10 s.
//
// Ao contrário do agy, o claude permite desligar tudo o que não é texto:
// --tools "" tira todas as ferramentas, --safe-mode ignora as personalizações
// do usuário (CLAUDE.md, hooks, plugins, skills, servidores MCP) sem mexer no
// login, --strict-mcp-config sem --mcp-config não carrega servidor MCP nenhum
// e --system-prompt-file troca as instruções de agente de programação pelas
// do Recorder. --no-session-persistence não guarda a conversa no histórico.
type ClaudeClient struct {
	Path  string // executável do claude; vazio se não foi encontrado
	Model string
}

func NewClaudeClient(path, model string) *ClaudeClient {
	if model == "" {
		model = DefaultClaudeModel
	}
	return &ClaudeClient{Path: path, Model: model}
}

// FindClaude procura o executável do claude; devolve "" se não encontrar.
func FindClaude() string {
	return findCLI("claude", "~/.claude/local")
}

// ClaudeModels são os apelidos de modelo que o claude aceita; cada um é o
// modelo mais novo da família.
func ClaudeModels() []ModelOption {
	return []ModelOption{
		{ID: "sonnet", Name: "Sonnet (equilibrado)"},
		{ID: "opus", Name: "Opus (mais capaz)"},
		{ID: "fable", Name: "Fable (o mais capaz, mais lento)"},
		{ID: "haiku", Name: "Haiku (mais rápido)"},
	}
}

func (c *ClaudeClient) ContextChars() int {
	return cliContextChars
}

func (c *ClaudeClient) Answer(question, contextText string, history []storage.ChatMessage, onText func(string)) (string, error) {
	return c.run(answerSystemPrompt, answerContent(question, contextText, history), nil, onText)
}

func (c *ClaudeClient) AskRaw(systemPrompt, userContent string) (string, error) {
	return c.run(systemPrompt, userContent, nil, nil)
}

func (c *ClaudeClient) StreamRaw(systemPrompt, userContent string, onText func(string)) (string, error) {
	return c.run(systemPrompt, userContent, nil, onText)
}

// AskJSON usa o --json-schema do claude, que faz a resposta seguir o schema.
func (c *ClaudeClient) AskJSON(systemPrompt, userContent string, schema any, out any) error {
	text, err := c.run(systemPrompt, userContent, schema, nil)
	if err != nil {
		return err
	}
	return decodeJSONAnswer("claude", text, out)
}

func (c *ClaudeClient) run(system, user string, schema any, onText func(string)) (string, error) {
	return runCLI("claude", c.Path, claudeDialect{}, c.Model, system, user, schema, onText)
}

type claudeDialect struct{}

func (claudeDialect) args(model, systemFile string, schema []byte) []string {
	args := []string{"-p", "--model", model,
		"--tools", "", "--safe-mode", "--strict-mcp-config", "--disable-slash-commands",
		"--no-session-persistence", "--system-prompt-file", systemFile,
		"--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	if schema != nil {
		args = append(args, "--json-schema", string(schema))
	}
	return args
}

// O conteúdo vai como texto puro: as instruções já foram no --system-prompt-file.
func (claudeDialect) stdin(system, user string) ([]byte, error) {
	return []byte(neutralizeMentions(user)), nil
}

// O claude anexa ao pedido o conteúdo de todo arquivo citado como "@caminho"
// no texto que recebe, mesmo sem ferramentas (--tools "") e com --safe-mode.
// Medido em 21/11/2026 (claude 2.1.325), com arquivos de teste: um
// "@/pasta/arquivo" no começo de uma linha ou depois de um espaço fez o
// conteúdo do arquivo ir junto com o pedido, tanto pela entrada em texto
// quanto pela de mensagens em JSON; no meio de uma palavra (um e-mail), entre
// aspas ou entre parênteses, não. O agy, chamado como em agy.go, não anexa.
//
// O texto que vai para o claude vem das reuniões: um "@/Users/.../arquivo"
// numa fala (ou num áudio importado) levaria um arquivo deste computador para
// a nuvem e, de lá, para o resumo.
//
// neutralizeMentions põe um espaço de largura zero antes e depois de cada "@"
// que não está no meio de uma palavra. Para quem lê, o texto continua igual;
// para o claude, o "@" deixa de começar uma palavra e o que vem depois dele
// deixa de ser o caminho de um arquivo (conferido com os mesmos arquivos de
// teste). Os e-mails ficam como estão.
func neutralizeMentions(text string) string {
	if !strings.Contains(text, "@") {
		return text
	}
	const zeroWidthSpace = "\u200b"
	var out strings.Builder
	out.Grow(len(text) + 16)
	var prev rune
	for _, r := range text {
		if r == '@' && !unicode.IsLetter(prev) && !unicode.IsDigit(prev) {
			out.WriteString(zeroWidthSpace + "@" + zeroWidthSpace)
		} else {
			out.WriteRune(r)
		}
		prev = r
	}
	return out.String()
}

// Eventos do --output-format stream-json do claude que interessam aqui.
type claudeEvent struct {
	Type  string `json:"type"`
	Event struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

func (claudeDialect) event(line []byte) (string, bool, string, error) {
	var ev claudeEvent
	if json.Unmarshal(line, &ev) != nil {
		return "", false, "", nil
	}
	switch ev.Type {
	case "stream_event":
		// Só o texto da resposta; com --json-schema, o JSON chega por outro caminho
		if ev.Event.Type == "content_block_delta" && ev.Event.Delta.Type == "text_delta" {
			return ev.Event.Delta.Text, false, "", nil
		}
	case "result":
		if ev.IsError || ev.Subtype != "success" {
			msg := strings.TrimSpace(ev.Result)
			if msg == "" {
				msg = ev.Subtype
			}
			return "", true, "", fmt.Errorf("%s", msg)
		}
		if len(ev.StructuredOutput) > 0 && string(ev.StructuredOutput) != "null" {
			return "", true, string(ev.StructuredOutput), nil
		}
		return "", true, ev.Result, nil
	}
	return "", false, "", nil
}
