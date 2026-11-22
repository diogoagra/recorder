package interpreter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWhisperRequestExplainsHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // sem ler o pedido inteiro, o servidor não vê quem pediu desistir
		switch r.URL.Query().Get("caso") {
		case "carregando": // o whisper-server ainda carregando o modelo
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("Loading model"))
		case "erro": // erro do próprio Whisper, em JSON
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error": "failed to read audio"}`))
		case "outro": // outro programa na porta
			w.Write([]byte("<html>olá</html>"))
		case "lento":
			<-r.Context().Done()
		}
	}))
	defer srv.Close()
	ask := func(ctx context.Context, caso string) error {
		_, err := whisperRequest(ctx, srv.URL+"/?caso="+caso+"&x=", tone(0.1, true), nil)
		return err
	}
	for caso, want := range map[string]string{
		"carregando": "HTTP 503: Loading model",
		"erro":       "whisper: failed to read audio",
		"outro":      "resposta fora do formato",
	} {
		if err := ask(context.Background(), caso); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: o erro deveria dizer %q, veio %v", caso, want, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := ask(ctx, "lento"); err == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("o pedido deveria acabar no prazo de quem pediu: %v em %v", err, time.Since(started))
	}
}
