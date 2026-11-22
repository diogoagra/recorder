package assistant

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"recorder/internal/chat"
	"recorder/internal/storage"
	"strings"
	"testing"
)

func TestSummarizeLongMeetingCoversBeginning(t *testing.T) {
	// Ollama falso: nas anotações devolve a primeira fala da parte; no resumo
	// final guarda o que recebeu
	var calls int
	var final string
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		var req chat.ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		calls++
		content := req.Messages[1].Content
		answer := "resumo"
		if strings.Contains(content, "--- TRANSCRIÇÃO (parte") {
			first := strings.SplitN(strings.SplitN(content, "\n", 3)[1], "Pessoa 1: ", 2)[1]
			answer = "- começou com" + first
		} else {
			final = content
		}
		json.NewEncoder(w).Encode(chat.ChatResponse{Message: chat.ChatMessage{Role: "assistant", Content: answer}})
	}))
	defer ollama.Close()

	var transcripts []storage.Transcript
	for i := range 400 {
		transcripts = append(transcripts, storage.Transcript{Speaker: "Pessoa 1", Text: fmt.Sprintf("fala %d %s", i, strings.Repeat("x", 100))})
	}
	client := chat.NewOllamaClient(ollama.URL, "teste")
	a := New(nil, client, client, nil)
	if _, err := a.Summarize("", transcripts, nil, nil); err != nil {
		t.Fatal(err)
	}
	if calls < 3 {
		t.Fatalf("uma reunião longa deveria ser resumida em partes, foram %d chamadas", calls)
	}
	if !strings.Contains(final, "começou comfala 0 ") {
		t.Fatalf("o resumo final deveria incluir o começo da reunião:\n%.300s", final)
	}
}
