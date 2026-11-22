package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"recorder/internal/storage"
	"reflect"
	"strings"
	"testing"
)

func TestPickPassagesByRelevanceWithinBudget(t *testing.T) {
	passages := []storage.Passage{
		{SessionID: "outra", Text: strings.Repeat("a", 40), Embedding: []float32{1, 0}},
		{SessionID: "atual", Text: strings.Repeat("b", 40), Embedding: []float32{0.9, 0.1}},
		{SessionID: "atual", Text: strings.Repeat("c", 40), Embedding: []float32{0, 1}},
		{SessionID: "atual", Text: strings.Repeat("d", 40), Embedding: []float32{1, 0.05}},
	}
	got := pickPassages(passages, []float32{1, 0}, "atual", 120)
	var texts []string
	for _, p := range got {
		texts = append(texts, p.Text[:1])
	}
	// Os três mais parecidos cabem; a sessão atual vem antes, na ordem em que foi dito
	if want := []string{"b", "d", "a"}; !reflect.DeepEqual(texts, want) {
		t.Fatalf("esperava %v, veio %v", want, texts)
	}
}

func TestBuildPassagesGroupsLinesWithTimes(t *testing.T) {
	var transcripts []storage.Transcript
	for i := range 6 {
		transcripts = append(transcripts, storage.Transcript{ID: i + 1, Speaker: "Pessoa 1", Text: strings.Repeat("palavra ", 30), Offset: float64(i * 20)})
	}
	passages := buildPassages("s", transcripts)
	if len(passages) < 2 {
		t.Fatalf("esperava vários trechos, veio %d", len(passages))
	}
	for _, p := range passages {
		if len(p.Text) > passageSize || !strings.HasPrefix(p.Text, "[") {
			t.Fatalf("trecho inválido (%d caracteres): %.40q", len(p.Text), p.Text)
		}
	}
	if last := passages[len(passages)-1]; last.LastTranscriptID != 6 {
		t.Fatalf("o último trecho deveria terminar na fala 6, terminou na %d", last.LastTranscriptID)
	}
}

func TestChatContextWithoutSearchUsesRecentTranscripts(t *testing.T) {
	// Ollama sem o modelo de busca e sem conseguir baixá-lo
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	a, store, sess := testAssistant(t, srv.URL)
	store.UpdateSessionSummary(sess.ID, "Resumo da reunião")
	store.InsertTranscript(sess.ID, "Audio Geral", "Pessoa 1", "Bom dia a todos.", 65)

	got := a.chatContext(sess.ID, "o que foi dito?", false)
	for _, want := range []string{"Resumo da reunião", "[01:05] Pessoa 1: Bom dia a todos."} {
		if !strings.Contains(got, want) {
			t.Fatalf("o contexto deveria ter %q:\n%s", want, got)
		}
	}
}

// "Todas as reuniões" no chat são as do workspace da sessão.
func TestSearchAllSessionsStaysInWorkspace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			w.WriteHeader(http.StatusOK)
		case "/api/embed":
			var req struct {
				Input []string `json:"input"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			embeddings := make([][]float32, len(req.Input))
			for i := range embeddings {
				embeddings[i] = []float32{1, 0}
			}
			json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a, store, sess := testAssistant(t, srv.URL)
	sameWS, _ := store.CreateSession(sess.WorkspaceID, "outra reunião do emprego A")
	otherWS, _ := store.CreateWorkspace("Emprego B")
	elsewhere, _ := store.CreateSession(otherWS.ID, "reunião do emprego B")
	store.InsertTranscript(sess.ID, "Audio Geral", "Pessoa 1", "Falamos do deploy.", 0)
	store.InsertTranscript(sameWS.ID, "Audio Geral", "Pessoa 1", "O cliente A pediu o relatório.", 0)
	store.InsertTranscript(elsewhere.ID, "Audio Geral", "Pessoa 1", "Segredo do emprego B.", 0)

	got := a.chatContext(sess.ID, "o que foi combinado?", true)
	if !strings.Contains(got, "O cliente A pediu o relatório.") {
		t.Fatalf("deveria buscar nas outras reuniões do workspace:\n%s", got)
	}
	if strings.Contains(got, "Segredo do emprego B.") {
		t.Fatalf("não deveria buscar em reuniões de outro workspace:\n%s", got)
	}
}
