package transcript

import (
	"path/filepath"
	"recorder/internal/storage"
	"strings"
	"testing"
)

func TestWhisperPromptWithPeopleAndGlossary(t *testing.T) {
	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess, _ := store.CreateSession("", "reunião")
	ws := sess.WorkspaceID
	if got := whisperPrompt(store, sess.ID); got != "" {
		t.Fatalf("sem nomes nem glossário o prompt deveria ser vazio, veio %q", got)
	}
	store.SavePersonVoice(ws, storage.SpeakerVoice{Label: "Guilherme", Embedding: []float32{1}})
	store.SavePersonVoice(ws, storage.SpeakerVoice{Label: "Ana", Embedding: []float32{1}})
	store.SetGlossary(ws, "ClickUp, Replicate\nOKR\n\nclickup")

	want := "Participantes: Ana, Guilherme. Termos: ClickUp, Replicate, OKR."
	if got := whisperPrompt(store, sess.ID); got != want {
		t.Fatalf("esperava %q, veio %q", want, got)
	}

	// A lista de participantes vem primeiro, sem os apelidos; quem está nela
	// não se repete por ter a voz conhecida com outra grafia
	store.SetParticipants(ws, "Ana Souza (Aninha)\nOleksandr Petrenko (Alex)")
	want = "Participantes: Ana Souza, Oleksandr Petrenko, Guilherme. Termos: ClickUp, Replicate, OKR."
	if got := whisperPrompt(store, sess.ID); got != want {
		t.Fatalf("esperava %q, veio %q", want, got)
	}
	store.SetParticipants(ws, "")

	// Os nomes e os termos de outro workspace ficam de fora
	other, _ := store.CreateWorkspace("Emprego B")
	there, _ := store.CreateSession(other.ID, "reunião")
	store.SavePersonVoice(other.ID, storage.SpeakerVoice{Label: "Bruno", Embedding: []float32{1}})
	store.SetGlossary(other.ID, "Jira")
	if got := whisperPrompt(store, there.ID); got != "Participantes: Bruno. Termos: Jira." {
		t.Fatalf("prompt do outro workspace: %q", got)
	}

	store.SetGlossary(ws, strings.Repeat("termo comprido, ", 100))
	if got := whisperPrompt(store, sess.ID); len(got) > maxWhisperPrompt+1 {
		t.Fatalf("prompt com %d caracteres, acima do limite", len(got))
	}
}
