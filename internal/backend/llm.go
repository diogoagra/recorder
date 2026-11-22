package backend

import (
	"fmt"

	"recorder/internal/chat"
)

// A IA que escreve os resumos, as tarefas, os nomes e as respostas do chat é
// escolhida nas configurações: o Ollama, neste computador, ou um modelo na
// nuvem por um CLI instalado: o agy (Antigravity: Gemini e outros) ou o claude
// (Claude Code). A busca do chat em várias reuniões usa sempre os embeddings
// do Ollama, quando ele está rodando.

// LLMSettings é a IA escolhida e o modelo de cada uma.
type LLMSettings struct {
	Provider    string `json:"provider"` // chat.ProviderOllama, chat.ProviderAgy ou chat.ProviderClaude
	OllamaModel string `json:"ollama_model"`
	AgyModel    string `json:"agy_model"`
	AgyFound    bool   `json:"agy_found"` // o agy está instalado neste computador
	ClaudeModel string `json:"claude_model"`
	ClaudeFound bool   `json:"claude_found"` // o claude está instalado neste computador
}

func (a *App) llmProvider() string {
	switch p, _ := a.store.GetSetting("llm_provider"); p {
	case chat.ProviderAgy, chat.ProviderClaude:
		return p
	}
	return chat.ProviderOllama
}

// setting devolve a configuração key, ou fallback se ela não tiver valor.
func (a *App) setting(key, fallback string) string {
	if v, _ := a.store.GetSetting(key); v != "" {
		return v
	}
	return fallback
}

// configuredLLM monta a IA escolhida nas configurações.
func (a *App) configuredLLM() chat.LLM {
	switch a.llmProvider() {
	case chat.ProviderAgy:
		return chat.NewAgyClient(chat.FindAgy(), a.setting("agy_model", ""))
	case chat.ProviderClaude:
		return chat.NewClaudeClient(chat.FindClaude(), a.setting("claude_model", ""))
	}
	return a.ollama
}

func (a *App) GetLLMSettings() LLMSettings {
	return LLMSettings{
		Provider:    a.llmProvider(),
		OllamaModel: a.ollama.CurrentModel(),
		AgyModel:    a.setting("agy_model", chat.DefaultAgyModel),
		AgyFound:    chat.FindAgy() != "",
		ClaudeModel: a.setting("claude_model", chat.DefaultClaudeModel),
		ClaudeFound: chat.FindClaude() != "",
	}
}

// SetLLM escolhe a IA e o modelo dela, a partir da próxima chamada e sem
// reiniciar o app. Com model vazio fica o modelo que já estava escolhido.
func (a *App) SetLLM(provider, model string) error {
	switch provider {
	case chat.ProviderOllama:
		if model != "" {
			if err := a.store.SetSetting("ollama_model", model); err != nil {
				return err
			}
			a.ollama.SetModel(model)
		}
	case chat.ProviderAgy, chat.ProviderClaude:
		find := map[string]func() string{chat.ProviderAgy: chat.FindAgy, chat.ProviderClaude: chat.FindClaude}[provider]
		if find() == "" {
			return fmt.Errorf("o %s não foi encontrado neste computador", provider)
		}
		if model != "" {
			if err := a.store.SetSetting(provider+"_model", model); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("IA desconhecida: %q", provider)
	}
	if err := a.store.SetSetting("llm_provider", provider); err != nil {
		return err
	}
	a.llm.Set(a.configuredLLM())
	return nil
}

// GetAgyModels lista os modelos que o agy oferece.
func (a *App) GetAgyModels() ([]chat.ModelOption, error) {
	return chat.AgyModels(chat.FindAgy())
}

// GetClaudeModels lista os modelos que podem ser escolhidos no claude.
func (a *App) GetClaudeModels() []chat.ModelOption {
	return chat.ClaudeModels()
}
