package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// fakeOllama responde /api/show com as capacidades dadas e /api/chat com os
// pedaços dados, um por linha, como o Ollama faz no modo stream.
func fakeOllama(t *testing.T, capabilities []string, chunks []string, requests *[]map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			json.NewEncoder(w).Encode(map[string]any{"capabilities": capabilities})
		case "/api/chat":
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			*requests = append(*requests, req)
			for i, c := range chunks {
				json.NewEncoder(w).Encode(ChatResponse{Message: ChatMessage{Role: "assistant", Content: c}, Done: i == len(chunks)-1})
			}
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestStreamRawDeliversTextAsItArrives(t *testing.T) {
	var requests []map[string]any
	srv := fakeOllama(t, []string{"completion", "thinking"}, []string{"Bom ", "dia", "!"}, &requests)
	defer srv.Close()
	c := NewOllamaClient(srv.URL, "modelo")

	var seen []string
	got, err := c.StreamRaw("sistema", "usuário", func(text string) { seen = append(seen, text) })
	if err != nil {
		t.Fatal(err)
	}
	if got != "Bom dia!" || !reflect.DeepEqual(seen, []string{"Bom ", "Bom dia", "Bom dia!"}) {
		t.Fatalf("resposta %q, pedaços %q", got, seen)
	}
	// Modelo com raciocínio: pedimos para desligar
	if think, ok := requests[0]["think"]; !ok || think != false {
		t.Fatalf("esperava think=false, veio %v", requests[0]["think"])
	}
}

func TestThinkIsOnlySentToModelsThatSupportIt(t *testing.T) {
	var requests []map[string]any
	srv := fakeOllama(t, []string{"completion"}, []string{"ok"}, &requests)
	defer srv.Close()
	if _, err := NewOllamaClient(srv.URL, "modelo").AskRaw("s", "u"); err != nil {
		t.Fatal(err)
	}
	if _, ok := requests[0]["think"]; ok {
		t.Fatal("modelo sem raciocínio não deveria receber o parâmetro think")
	}
}

func TestAskJSONDecodesStructuredAnswer(t *testing.T) {
	var requests []map[string]any
	srv := fakeOllama(t, nil, []string{`{"title": "Revisão`, ` de Sprint"}`}, &requests)
	defer srv.Close()

	var out struct {
		Title string `json:"title"`
	}
	schema := map[string]any{"type": "object"}
	if err := NewOllamaClient(srv.URL, "modelo").AskJSON("s", "u", schema, &out); err != nil {
		t.Fatal(err)
	}
	if out.Title != "Revisão de Sprint" || requests[0]["format"] == nil {
		t.Fatalf("título %q, format %v", out.Title, requests[0]["format"])
	}
}

func TestEnsureModelPullsWhenMissing(t *testing.T) {
	var pulled string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			http.NotFound(w, r)
		case "/api/pull":
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			pulled = req["model"].(string)
			for _, done := range []int{0, 50, 100} {
				fmt.Fprintf(w, `{"status":"pulling","total":200,"completed":%d}`+"\n", done*2)
			}
			fmt.Fprintln(w, `{"status":"success"}`)
		}
	}))
	defer srv.Close()

	var progress []int
	if err := NewOllamaClient(srv.URL, "modelo").EnsureModel(EmbeddingModel, func(p int) { progress = append(progress, p) }); err != nil {
		t.Fatal(err)
	}
	if pulled != EmbeddingModel || !reflect.DeepEqual(progress, []int{0, 50, 100}) {
		t.Fatalf("baixou %q com progresso %v", pulled, progress)
	}
}

func TestEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if r.URL.Path != "/api/embed" || req.Model != EmbeddingModel {
			http.NotFound(w, r)
			return
		}
		out := [][]float32{}
		for _, in := range req.Input {
			out = append(out, []float32{float32(len(in)), 1})
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
	}))
	defer srv.Close()

	got, err := NewOllamaClient(srv.URL, "modelo").Embed([]string{"ab", "abcd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1][0] != 4 {
		t.Fatalf("embeddings inesperados: %v", got)
	}
	if strings.Contains(EmbeddingModel, " ") {
		t.Fatal("nome de modelo inválido")
	}
}
