package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"recorder/internal/chat"
	"recorder/internal/storage"
	"strings"
	"testing"
)

// fakeJSONOllama responde toda chamada a /api/chat com answer.
func fakeJSONOllama(answer string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(chat.ChatResponse{Message: chat.ChatMessage{Role: "assistant", Content: answer}, Done: true})
	}))
}

func testAssistant(t *testing.T, ollamaURL string) (*Assistant, *storage.Storage, *storage.Session) {
	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	sess, _ := store.CreateSession("", "reunião")
	ollama := chat.NewOllamaClient(ollamaURL, "modelo")
	return New(store, ollama, ollama, nil), store, sess
}

// fakeNamesOllama responde à busca de chamadas com mentions e, na conferência
// de quem respondeu, com answers[nome chamado na primeira fala da janela].
func fakeNamesOllama(mentions string, answers map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		var req chat.ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		answer := mentions
		if strings.Contains(req.Messages[0].Content, "Quem respondeu") {
			answer = `{"speaker": "nenhum"}`
			first := strings.SplitN(req.Messages[1].Content, "\n", 2)[0]
			for name, speaker := range answers {
				if strings.Contains(first, name) {
					answer = `{"speaker": "` + speaker + `"}`
				}
			}
		}
		json.NewEncoder(w).Encode(chat.ChatResponse{Message: chat.ChatMessage{Role: "assistant", Content: answer}, Done: true})
	}))
}

func insertLines(store *storage.Storage, sessionID string, lines [][3]string) []storage.Transcript {
	var out []storage.Transcript
	for _, l := range lines {
		tr, _ := store.InsertTranscript(sessionID, "Audio Geral", l[1], l[2], parseOffset(l[0]))
		out = append(out, *tr)
	}
	return out
}
