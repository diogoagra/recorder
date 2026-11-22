//go:build !windows

package backend

import (
	"os"
	"path/filepath"
	"recorder/internal/chat"
	"testing"
)

func TestSetLLMSwitchesBetweenOllamaAndAgy(t *testing.T) {
	_, store := testApp(t) // HOME temporário: o agy de verdade não é encontrado
	t.Setenv("PATH", "/usr/bin:/bin")
	a := NewApp(store, chat.NewOllamaClient("http://127.0.0.1:1", "llama3.1"))

	settings := a.GetLLMSettings()
	if settings.Provider != chat.ProviderOllama || settings.AgyModel != chat.DefaultAgyModel || settings.AgyFound {
		t.Fatalf("configuração inicial: %+v", settings)
	}
	if err := a.SetLLM(chat.ProviderAgy, "gemini-x"); err == nil {
		t.Fatal("sem o agy instalado não deveria trocar para ele")
	}
	if a.llmProvider() != chat.ProviderOllama {
		t.Fatal("a IA não deveria ter mudado")
	}

	// Instala um agy falso no PATH
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "agy"), []byte("#!/bin/sh\necho '{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"response\":\"resposta do agy\"}}'\n"), 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")

	if err := a.SetLLM(chat.ProviderAgy, "gemini-x"); err != nil {
		t.Fatal(err)
	}
	agy, ok := a.llm.Current().(*chat.AgyClient)
	if !ok || agy.Model != "gemini-x" {
		t.Fatalf("deveria estar usando o agy com o gemini-x, está com %#v", a.llm.Current())
	}
	if got, err := a.assistant.LLM.AskRaw("s", "u"); err != nil || got != "resposta do agy" {
		t.Fatalf("o assistente deveria usar o agy: %q, %v", got, err)
	}
	// Reabrir o app mantém a escolha
	if again := NewApp(store, chat.NewOllamaClient("http://127.0.0.1:1", "llama3.1")); again.GetLLMSettings().Provider != chat.ProviderAgy {
		t.Fatal("a escolha do agy deveria ficar salva")
	}

	if err := a.SetLLM(chat.ProviderOllama, "gemma3"); err != nil {
		t.Fatal(err)
	}
	if a.llm.Current() != chat.LLM(a.ollama) || a.ollama.Model != "gemma3" {
		t.Fatalf("deveria voltar ao Ollama com o gemma3: %#v", a.llm.Current())
	}
	if got := a.GetLLMSettings(); got.AgyModel != "gemini-x" || got.OllamaModel != "gemma3" {
		t.Fatalf("cada IA mantém o seu modelo: %+v", got)
	}
	if err := a.SetLLM("outra", ""); err == nil {
		t.Fatal("IA desconhecida deveria dar erro")
	}
}

func TestSetLLMToClaude(t *testing.T) {
	_, store := testApp(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	a := NewApp(store, chat.NewOllamaClient("http://127.0.0.1:1", "llama3.1"))
	if a.GetLLMSettings().ClaudeFound {
		t.Fatal("o claude de verdade não deveria ser encontrado com o HOME temporário")
	}
	if err := a.SetLLM(chat.ProviderClaude, "opus"); err == nil {
		t.Fatal("sem o claude instalado não deveria trocar para ele")
	}

	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"resposta do claude\"}'\n"), 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")

	if s := a.GetLLMSettings(); !s.ClaudeFound || s.ClaudeModel != chat.DefaultClaudeModel {
		t.Fatalf("configuração: %+v", s)
	}
	if err := a.SetLLM(chat.ProviderClaude, "opus"); err != nil {
		t.Fatal(err)
	}
	claude, ok := a.llm.Current().(*chat.ClaudeClient)
	if !ok || claude.Model != "opus" {
		t.Fatalf("deveria estar usando o claude com o opus, está com %#v", a.llm.Current())
	}
	if got, err := a.assistant.LLM.AskRaw("s", "u"); err != nil || got != "resposta do claude" {
		t.Fatalf("o assistente deveria usar o claude: %q, %v", got, err)
	}
	if again := NewApp(store, chat.NewOllamaClient("http://127.0.0.1:1", "llama3.1")); again.GetLLMSettings().Provider != chat.ProviderClaude {
		t.Fatal("a escolha do claude deveria ficar salva")
	}
}
