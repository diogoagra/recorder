package backend

import (
	"os"
	"path/filepath"
	"recorder/internal/datadir"
	"recorder/internal/storage"
	"testing"
)

func testApp(t *testing.T) (*App, *storage.Storage) {
	// Apagar sessões mexe nos áudios em ~/.recorder: fica numa pasta temporária
	t.Setenv("HOME", t.TempDir())
	t.Setenv(datadir.EnvVar, "")
	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &App{store: store}, store
}

func TestWorkspaceNamesMustBeUniqueAndNotEmpty(t *testing.T) {
	a, _ := testApp(t)
	ws, err := a.CreateWorkspace("  Emprego B  ")
	if err != nil {
		t.Fatal(err)
	}
	if ws.Name != "Emprego B" {
		t.Fatalf("nome %q", ws.Name)
	}
	if _, err := a.CreateWorkspace("emprego b"); err == nil {
		t.Fatal("deveria recusar um nome repetido")
	}
	if _, err := a.CreateWorkspace("   "); err == nil {
		t.Fatal("deveria recusar um nome vazio")
	}
	// Renomear para o próprio nome, mudando só as maiúsculas, pode
	if err := a.RenameWorkspace(ws.ID, "EMPREGO B"); err != nil {
		t.Fatal(err)
	}
	if err := a.RenameWorkspace(ws.ID, "principal"); err == nil {
		t.Fatal("deveria recusar o nome de outro workspace")
	}
}

func TestDeleteWorkspaceRemovesItsMeetings(t *testing.T) {
	a, store := testApp(t)
	first := store.CurrentWorkspaceID()
	if err := a.DeleteWorkspace(first); err == nil {
		t.Fatal("não deveria apagar o único workspace")
	}

	other, _ := a.CreateWorkspace("Emprego B")
	keep, _ := a.CreateSession(first, "reunião que fica")
	gone, _ := a.CreateSession(other.ID, "reunião que vai embora")
	store.InsertTranscript(gone.ID, "Audio Geral", "Pessoa 1", "olá", 0)
	audioDir := datadir.SessionsAudio()
	os.MkdirAll(audioDir, 0755)
	audioFile := filepath.Join(audioDir, gone.ID+".m4a")
	os.WriteFile(audioFile, []byte("áudio"), 0644)

	// Gravando numa reunião do workspace, não apaga
	a.recorder().Start(gone.ID, 0)
	if err := a.DeleteWorkspace(other.ID); err == nil {
		t.Fatal("não deveria apagar o workspace com uma reunião sendo gravada")
	}
	if err := a.MoveSession(gone.ID, first); err == nil {
		t.Fatal("não deveria mover a reunião que está sendo gravada")
	}
	a.recorder().Stop()

	if err := a.DeleteWorkspace(other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSession(gone.ID); err == nil {
		t.Fatal("a reunião do workspace apagado deveria ter sido apagada")
	}
	if _, err := os.Stat(audioFile); !os.IsNotExist(err) {
		t.Fatal("o áudio da reunião apagada deveria ter sido apagado")
	}
	if _, err := store.GetSession(keep.ID); err != nil {
		t.Fatal("a reunião do outro workspace não deveria ser apagada")
	}
	if workspaces, _ := a.GetWorkspaces(); len(workspaces) != 1 {
		t.Fatalf("esperava 1 workspace, veio %+v", workspaces)
	}
}

func TestGetSessionsTellsTheWorkspace(t *testing.T) {
	a, store := testApp(t)
	other, _ := a.CreateWorkspace("Emprego B")
	sess, _ := a.CreateSession(other.ID, "reunião")
	if err := a.MoveSession(sess.ID, store.CurrentWorkspaceID()); err != nil {
		t.Fatal(err)
	}
	sessions, err := a.GetSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.ID == sess.ID && s.WorkspaceID != store.CurrentWorkspaceID() {
			t.Fatalf("a reunião deveria aparecer no workspace para onde foi movida, está no %q", s.WorkspaceID)
		}
	}
}
