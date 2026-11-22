// Package assistant faz o trabalho de IA das sessões: o resumo, as tarefas,
// as sugestões de nomes e as respostas do chat com busca na transcrição. O
// texto é escrito pela IA escolhida nas configurações (Ollama ou agy); a busca
// do chat usa sempre os embeddings do Ollama.
package assistant

import (
	"sync"

	"recorder/internal/chat"
	"recorder/internal/storage"
)

type Assistant struct {
	Store *storage.Storage
	// LLM escreve os resumos, as tarefas, os nomes e as respostas
	LLM chat.LLM
	// Ollama faz os embeddings da busca do chat, qualquer que seja a LLM
	Ollama *chat.OllamaClient
	// Status recebe o que está sendo feito (ex: "Gerando resumo da sessão..."),
	// para mostrar na tela; "" quando termina.
	Status func(message string)

	// Busca por relevância do chat (search.go)
	searchMu    sync.Mutex
	searchReady bool // o modelo de busca já está no Ollama
}

func New(store *storage.Storage, llm chat.LLM, ollama *chat.OllamaClient, status func(message string)) *Assistant {
	return &Assistant{Store: store, LLM: llm, Ollama: ollama, Status: status}
}

func (a *Assistant) status(message string) {
	if a.Status != nil {
		a.Status(message)
	}
}
