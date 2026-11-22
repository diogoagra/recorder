package backend

import "testing"

// Dois trabalhos ao mesmo tempo: o fim de um não pode apagar o aviso do outro.
func TestStatusStaysWhileAnotherJobRuns(t *testing.T) {
	a, _ := testApp(t)
	if working, _, _ := a.currentStatus(); working {
		t.Fatal("sem trabalho, não há aviso")
	}
	importing := a.beginJob("s1", "Transcrevendo o áudio importado (0%)")
	summary := a.beginJob("s2", "Gerando resumo da sessão...")
	if working, message, session := a.currentStatus(); !working || message != "Gerando resumo da sessão..." || session != "s2" {
		t.Fatalf("a tela mostra o trabalho mais recente: %v %q %q", working, message, session)
	}
	importing.update("Transcrevendo o áudio importado (37%)")
	// Uma etapa do assistente é do trabalho mais recente
	a.assistantStatus("Resumindo a reunião (parte 2 de 3)...")
	if _, message, session := a.currentStatus(); message != "Resumindo a reunião (parte 2 de 3)..." || session != "s2" {
		t.Fatalf("etapa do resumo: %q %q", message, session)
	}
	a.assistantStatus("") // o assistente terminou uma etapa: o trabalho continua

	summary.end()
	if working, message, session := a.currentStatus(); !working || message != "Transcrevendo o áudio importado (37%)" || session != "s1" {
		t.Fatalf("o trabalho que continua deveria aparecer: %v %q %q", working, message, session)
	}
	importing.end()
	importing.end() // terminar duas vezes não atrapalha
	if working, _, _ := a.currentStatus(); working {
		t.Fatal("todos terminaram: o aviso some")
	}
}

// A busca do chat avisa pelo assistente, fora de qualquer trabalho.
func TestAssistantStatusOutsideAJob(t *testing.T) {
	a, _ := testApp(t)
	a.assistantStatus("Preparando a busca na transcrição (10%)...")
	a.assistantStatus("Preparando a busca na transcrição (60%)...")
	if working, message, _ := a.currentStatus(); !working || message != "Preparando a busca na transcrição (60%)..." {
		t.Fatalf("aviso da busca: %v %q", working, message)
	}
	a.assistantStatus("")
	if working, _, _ := a.currentStatus(); working {
		t.Fatal("o assistente terminou: o aviso some")
	}
	a.statusMessage("sem trabalho, não vira aviso")
	if working, _, _ := a.currentStatus(); working {
		t.Fatal("statusMessage sem trabalho não deveria criar um aviso")
	}
}

func TestSessionAudioFilesAreMatchedByName(t *testing.T) {
	id := "3f2a9c1e-5b7d-4e8a-9c06-1d2e3f4a5b6c"
	for name, want := range map[string]bool{
		id + ".m4a":            true,
		id + ".wav":            true,
		id + "_part_1789.m4a":  true,
		id + "_tmp.mp4":        false,
		id + "_novo_tmp.m4a":   false,
		id + "_tmp.wav":        false,
		"outra-" + id + ".m4a": false,
		id + "0.m4a":           false,
		id + ".txt":            false,
	} {
		if got := isSessionAudio(name, id); got != want {
			t.Errorf("isSessionAudio(%q) = %v, esperava %v", name, got, want)
		}
	}
}

// O que a tela recebe: cada mudança manda o estado inteiro, com a sessão do
// trabalho mostrado.
func TestStatusEventsCarryTheSession(t *testing.T) {
	a, _ := testApp(t)
	type status struct {
		working          bool
		message, session string
	}
	var got []status
	a.events = func(event string, data ...interface{}) {
		if event != "ollama-status" {
			return
		}
		payload := data[0].(map[string]interface{})
		got = append(got, status{payload["working"].(bool), payload["message"].(string), payload["session_id"].(string)})
	}
	first := a.beginJob("s1", "Reprocessando áudio (0%)")
	second := a.beginJob("s2", "Gerando resumo da sessão...")
	second.end()
	first.end()
	want := []status{
		{true, "Reprocessando áudio (0%)", "s1"},
		{true, "Gerando resumo da sessão...", "s2"},
		{true, "Reprocessando áudio (0%)", "s1"}, // o resumo terminou; o reprocessamento continua
		{false, "", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("eventos: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("evento %d: %+v, esperava %+v", i, got[i], want[i])
		}
	}
}
