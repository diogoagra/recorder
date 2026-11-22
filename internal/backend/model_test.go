package backend

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"recorder/internal/audio"
	"recorder/internal/chat"
	"recorder/internal/datadir"
	"recorder/internal/transcriber"
)

// pendingModel é um modelo do Whisper que não termina de baixar: o servidor
// dele está fora do ar, como numa primeira execução sem internet.
func pendingModel(t *testing.T) *transcriber.ModelFile {
	t.Helper()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fora do ar", http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)
	model := transcriber.FetchModel(transcriber.Model{
		File: "modelo-de-teste.bin", Size: 1000, SHA256: strings.Repeat("0", 64), URLs: []string{down.URL},
	}, t.TempDir(), nil)
	t.Cleanup(model.Stop)
	return model
}

// Na primeira execução o whisper-server só sobe depois de o modelo ser
// baixado. A tela abre assim mesmo (para ler as reuniões que já existem), em
// vez de parar no aviso de serviços indisponíveis.
func TestServicesAreNotDownWhileTheModelDownloads(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[]}`))
	}))
	defer ollama.Close()
	_, store := testApp(t)
	a := NewApp(store, chat.NewOllamaClient(ollama.URL, "modelo"))
	a.whisperEndpoint = "http://127.0.0.1:1" // nenhum whisper-server atendendo

	// Com o modelo no disco, o Whisper parado é uma falha
	health := a.CheckServices()
	if health["ok"] != false || health["whisper"] != false {
		t.Fatalf("com o modelo pronto e o Whisper parado, os serviços não estão OK: %+v", health)
	}
	if st := health["model"].(transcriber.ModelStatus); st.State != transcriber.ModelReady {
		t.Fatalf("sem download em andamento, o modelo é dado como pronto: %+v", st)
	}

	a.model = pendingModel(t)
	health = a.CheckServices()
	if health["ok"] != true || health["whisper"] != false {
		t.Fatalf("com o modelo sendo baixado, a tela deveria abrir: %+v", health)
	}
	if st := health["model"].(transcriber.ModelStatus); st.State == transcriber.ModelReady || st.Total != 1000 {
		t.Fatalf("a tela precisa saber que o modelo está sendo baixado: %+v", st)
	}
}

// Sem o modelo, gravar deixaria uma reunião sem transcrição, e reprocessar
// apagaria a que existe.
func TestNothingIsTranscribedBeforeTheModelArrives(t *testing.T) {
	a, store := testApp(t)
	a.model = pendingModel(t)
	sess, _ := store.CreateSession("", "Reunião")
	store.InsertTranscript(sess.ID, "Audio Geral", "Ana", "o que já estava transcrito", 0)
	os.MkdirAll(datadir.SessionsAudio(), 0755)
	os.WriteFile(audio.SessionFile(datadir.SessionsAudio(), sess.ID), []byte("áudio"), 0644)

	wantsTheModel := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "modelo de transcrição ainda está sendo baixado") {
			t.Fatalf("%s: esperava o aviso do download do modelo, veio %v", what, err)
		}
	}

	_, err := a.StartRecording(sess.ID)
	wantsTheModel("gravar", err)
	if rec, _, _ := a.recorder().Status(); rec {
		t.Fatal("a gravação começou sem o modelo")
	}

	wantsTheModel("reprocessar", a.ReprocessSession(sess.ID))
	if all, _ := store.GetAllTranscripts(sess.ID); len(all) != 1 {
		t.Fatalf("a transcrição atual foi apagada: %+v", all)
	}

	// Recusado antes de abrir a janela de escolha do arquivo
	_, err = a.ImportAudio("")
	wantsTheModel("importar um áudio", err)

	_, err = a.whisperModelPath()
	wantsTheModel("subir o Whisper do Intérprete", err)
}

func TestModelProgressReachesTheScreen(t *testing.T) {
	a, _ := testApp(t)
	var got []interface{}
	a.events = func(event string, data ...interface{}) {
		if event == "whisper-model" {
			got = data
		}
	}
	a.broadcastWhisperModel(transcriber.ModelStatus{State: transcriber.ModelDownloading, Done: 250, Total: 1000})
	if len(got) != 1 {
		t.Fatalf("a tela não recebeu o andamento: %v", got)
	}
	if st := got[0].(transcriber.ModelStatus); st.Percent() != 25 {
		t.Fatalf("andamento %+v", st)
	}
}
