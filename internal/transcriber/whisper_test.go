package transcriber

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestParseResponse(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []Segment
	}{
		{
			name: "verbose_json do whisper.cpp",
			body: `{"task":"transcribe","text":" Olá. Tudo bem?","segments":[{"id":0,"text":" Olá.","start":0.0,"end":1.5},{"id":1,"text":" Tudo bem?","start":1.5,"end":3.2}]}`,
			want: []Segment{{Start: 0, End: 1.5, Text: "Olá."}, {Start: 1.5, End: 3.2, Text: "Tudo bem?"}},
		},
		{
			name: "servidor com data/sentence",
			body: `{"code":200,"data":[{"sentence":" Olá."},{"sentence":" Tudo bem?"}]}`,
			want: []Segment{{Text: "Olá."}, {Text: "Tudo bem?"}},
		},
		{
			name: "json só com text",
			body: `{"text":" Olá. Tudo bem?\n"}`,
			want: []Segment{{Text: "Olá. Tudo bem?"}},
		},
		{
			name: "texto puro",
			body: " Olá. Tudo bem?\n",
			want: []Segment{{Text: "Olá. Tudo bem?"}},
		},
	}
	for _, c := range cases {
		if got := parseResponse([]byte(c.body)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: esperava %+v, veio %+v", c.name, c.want, got)
		}
	}
	if got := Text(parseResponse([]byte(cases[0].body))); got != "Olá. Tudo bem?" {
		t.Errorf("Text: veio %q", got)
	}
}

func TestRunningOnlyForWhisperServer(t *testing.T) {
	whisper := func(status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Server", "whisper.cpp")
			w.WriteHeader(status)
		}))
	}
	ready, loading := whisper(http.StatusOK), whisper(http.StatusServiceUnavailable)
	// Outro programa na porta (ex: um servidor Java), que também responde 200
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Ok"))
	}))
	defer ready.Close()
	defer loading.Close()
	defer other.Close()

	cases := []struct {
		name           string
		url            string
		running, ready bool
	}{
		{"whisper-server pronto", ready.URL, true, true},
		{"whisper-server carregando o modelo", loading.URL, true, false},
		{"outro programa na porta", other.URL, false, false},
		{"ninguém na porta", "http://127.0.0.1:1", false, false},
	}
	for _, c := range cases {
		client := NewWhisperClient(c.url)
		if got := client.Running(); got != c.running {
			t.Errorf("%s: Running() = %v", c.name, got)
		}
		if got := client.Ready(); got != c.ready {
			t.Errorf("%s: Ready() = %v", c.name, got)
		}
	}
}

// Um servidor travado não pode segurar a fila de transcrição para sempre.
func TestTranscribeWavGivesUpOnStuckServer(t *testing.T) {
	release := make(chan struct{})
	stuck := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer stuck.Close()
	defer close(release)

	wav := filepath.Join(t.TempDir(), "bloco.wav")
	if err := os.WriteFile(wav, []byte("RIFF"), 0644); err != nil {
		t.Fatal(err)
	}
	client := NewWhisperClient(stuck.URL)
	client.Timeout = 50 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		_, err := client.TranscribeWav(wav, "")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("esperava erro de um servidor que não responde")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a transcrição ficou esperando o servidor travado")
	}
}
