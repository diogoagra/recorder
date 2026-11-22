package chat

import (
	"sync"

	"recorder/internal/storage"
)

// IAs que podem escrever os resumos, as tarefas, as sugestões de nomes e as
// respostas do chat (configuração "llm_provider").
const (
	ProviderOllama = "ollama" // no computador; o texto das reuniões não sai dele
	ProviderAgy    = "agy"    // na nuvem (Gemini e outros), pelo CLI do Antigravity
	ProviderClaude = "claude" // na nuvem (Anthropic), pelo CLI do Claude Code
)

// LLM é a IA que escreve os resumos, as tarefas, as sugestões de nomes e as
// respostas do chat.
type LLM interface {
	// Answer responde à pergunta usando só o contexto dado e o histórico da
	// conversa. onText, se não for nil, recebe o texto acumulado enquanto é escrito.
	Answer(question, contextText string, history []storage.ChatMessage, onText func(string)) (string, error)
	AskRaw(systemPrompt, userContent string) (string, error)
	StreamRaw(systemPrompt, userContent string, onText func(string)) (string, error)
	// AskJSON pede uma resposta no formato do schema (JSON Schema) e a decodifica em out.
	AskJSON(systemPrompt, userContent string, schema any, out any) error
	// ContextChars é quanto texto de transcrição cabe numa chamada.
	ContextChars() int
}

// Switch é a IA escolhida nas configurações. Pode ser trocada com o app
// aberto; uma chamada em andamento termina com a IA em que começou.
type Switch struct {
	mu  sync.RWMutex
	llm LLM
}

func NewSwitch(llm LLM) *Switch {
	return &Switch{llm: llm}
}

func (s *Switch) Set(llm LLM) {
	s.mu.Lock()
	s.llm = llm
	s.mu.Unlock()
}

func (s *Switch) Current() LLM {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.llm
}

func (s *Switch) Answer(question, contextText string, history []storage.ChatMessage, onText func(string)) (string, error) {
	return s.Current().Answer(question, contextText, history, onText)
}

func (s *Switch) AskRaw(systemPrompt, userContent string) (string, error) {
	return s.Current().AskRaw(systemPrompt, userContent)
}

func (s *Switch) StreamRaw(systemPrompt, userContent string, onText func(string)) (string, error) {
	return s.Current().StreamRaw(systemPrompt, userContent, onText)
}

func (s *Switch) AskJSON(systemPrompt, userContent string, schema any, out any) error {
	return s.Current().AskJSON(systemPrompt, userContent, schema, out)
}

func (s *Switch) ContextChars() int {
	return s.Current().ContextChars()
}
