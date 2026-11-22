package storage

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranscriptsFollowTheAudio(t *testing.T) {
	s, err := NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sess, _ := s.CreateSession("", "reunião")
	// Uma fala antiga, sem posição no áudio, e blocos transcritos fora de ordem
	// (o do meio recuperado de uma gravação interrompida)
	if _, err := s.db.Exec(`INSERT INTO transcripts (session_id, source, speaker, text) VALUES (?, '', '', 'antiga')`, sess.ID); err != nil {
		t.Fatal(err)
	}
	s.InsertTranscript(sess.ID, "", "", "primeira", 5)
	s.InsertTranscript(sess.ID, "", "", "terceira", 65)
	s.InsertTranscript(sess.ID, "", "", "segunda", 31)

	all, _ := s.GetAllTranscripts(sess.ID)
	var got []string
	for _, tr := range all {
		got = append(got, tr.Text)
	}
	if want := "antiga primeira segunda terceira"; strings.Join(got, " ") != want {
		t.Fatalf("GetAllTranscripts: esperava %q, veio %q", want, strings.Join(got, " "))
	}
	recent, _ := s.GetRecentTranscripts(sess.ID, 2)
	got = nil
	for _, tr := range recent {
		got = append(got, tr.Text)
	}
	if want := "segunda terceira"; strings.Join(got, " ") != want {
		t.Fatalf("GetRecentTranscripts: esperava %q, veio %q", want, strings.Join(got, " "))
	}
}

// Um banco de antes dos workspaces: as reuniões, as vozes conhecidas e o
// glossário eram do app todo.
func TestMigratesOldDatabaseIntoDefaultWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	old, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, name TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, summary TEXT DEFAULT '')`,
		`CREATE TABLE people (name TEXT PRIMARY KEY, embedding BLOB, seconds REAL)`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO sessions (id, name) VALUES ('s1', 'Reunião 1'), ('s2', 'Reunião 2')`,
		`INSERT INTO people (name, embedding, seconds) VALUES ('Ana', x'0000803f', 30)`,
		`INSERT INTO settings (key, value) VALUES ('glossary', 'ClickUp')`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	for range 2 { // abrir de novo não pode duplicar nada
		s, err := NewStorage(path)
		if err != nil {
			t.Fatal(err)
		}
		workspaces, _ := s.GetWorkspaces()
		if len(workspaces) != 1 || workspaces[0].Name != DefaultWorkspaceName {
			t.Fatalf("esperava só o workspace %q, veio %+v", DefaultWorkspaceName, workspaces)
		}
		ws := workspaces[0].ID
		if ids, _ := s.WorkspaceSessionIDs(ws); len(ids) != 2 {
			t.Fatalf("as 2 reuniões deveriam estar no workspace, estão %v", ids)
		}
		people, _ := s.GetPeopleVoices(ws)
		if len(people) != 1 || people[0].Label != "Ana" || people[0].Embedding[0] != 1 {
			t.Fatalf("a voz da Ana deveria estar no workspace: %+v", people)
		}
		if glossary, _ := s.GetGlossary(ws); glossary != "ClickUp" {
			t.Fatalf("glossário %q", glossary)
		}
		if s.CurrentWorkspaceID() != ws {
			t.Fatalf("o workspace atual deveria ser o único")
		}
		s.Close()
	}
}

func TestSessionsAndPeopleBelongToWorkspace(t *testing.T) {
	s, err := NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := s.CurrentWorkspaceID()
	other, _ := s.CreateWorkspace("Emprego B")

	a, _ := s.CreateSession("", "reunião A")
	b, _ := s.CreateSession(other.ID, "reunião B")
	if a.WorkspaceID != first || b.WorkspaceID != other.ID {
		t.Fatalf("workspaces das sessões: %q e %q", a.WorkspaceID, b.WorkspaceID)
	}
	if got := s.SessionWorkspace(b.ID); got != other.ID {
		t.Fatalf("SessionWorkspace: %q", got)
	}

	// O mesmo nome pode ser uma pessoa diferente em cada workspace
	s.SavePersonVoice(first, SpeakerVoice{Label: "Ana", Embedding: []float32{1}, Seconds: 10})
	s.SavePersonVoice(other.ID, SpeakerVoice{Label: "Ana", Embedding: []float32{2}, Seconds: 20})
	if p, _ := s.GetPeopleVoices(other.ID); len(p) != 1 || p[0].Seconds != 20 {
		t.Fatalf("vozes do outro workspace: %+v", p)
	}

	if err := s.SetCurrentWorkspace(other.ID); err != nil {
		t.Fatal(err)
	}
	if s.CurrentWorkspaceID() != other.ID {
		t.Fatal("o workspace atual deveria ter mudado")
	}
	if err := s.DeleteWorkspace(other.ID); err != nil {
		t.Fatal(err)
	}
	if s.CurrentWorkspaceID() != first {
		t.Fatal("sem o workspace atual, vale o primeiro")
	}
	if p, _ := s.GetPeopleVoices(other.ID); len(p) != 0 {
		t.Fatalf("as vozes do workspace apagado deveriam sumir: %+v", p)
	}
	if p, _ := s.GetPeopleVoices(first); len(p) != 1 {
		t.Fatalf("as vozes do primeiro workspace não deveriam mudar: %+v", p)
	}
}

// Um banco de antes das notas: a coluna nova aparece e as reuniões que já
// existiam ficam sem notas.
func TestOldSessionsGetEmptyNotes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	old, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, name TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, summary TEXT DEFAULT '', workspace_id TEXT)`,
		`INSERT INTO sessions (id, name, summary) VALUES ('s1', 'Reunião 1', 'resumo')`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	for range 2 { // abrir de novo não pode apagar as notas
		s, err := NewStorage(path)
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.GetSession("s1")
		if err != nil || sess.Summary != "resumo" {
			t.Fatalf("sessão antiga: %+v, erro %v", sess, err)
		}
		if sess.Notes != "" && sess.Notes != "anotado" {
			t.Fatalf("notas %q", sess.Notes)
		}
		if err := s.UpdateSessionNotes("s1", "anotado"); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	s, err := NewStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if sess, _ := s.GetSession("s1"); sess == nil || sess.Notes != "anotado" {
		t.Fatalf("as notas não ficaram guardadas: %+v", sess)
	}
	sessions, _ := s.GetSessions()
	if len(sessions) != 1 || sessions[0].Notes != "anotado" {
		t.Fatalf("GetSessions: %+v", sessions)
	}
}

func TestScreenshotsBelongToSession(t *testing.T) {
	s, err := NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _ := s.CreateSession("", "reunião A")
	b, _ := s.CreateSession("", "reunião B")

	first, err := s.AddScreenshot(a.ID, "um.png", 12.5)
	if err != nil {
		t.Fatal(err)
	}
	s.AddScreenshot(a.ID, "dois.png", -1)
	other, _ := s.AddScreenshot(b.ID, "tres.png", 3)

	shots, err := s.GetScreenshots(a.ID)
	if err != nil || len(shots) != 2 {
		t.Fatalf("GetScreenshots: %d capturas, erro %v", len(shots), err)
	}
	if shots[0].File != "um.png" || shots[0].Offset != 12.5 || shots[0].SessionID != a.ID || shots[0].CreatedAt.IsZero() {
		t.Fatalf("primeira captura: %+v", shots[0])
	}
	if shots[1].File != "dois.png" || shots[1].Offset != -1 {
		t.Fatalf("segunda captura: %+v", shots[1])
	}
	if got, err := s.GetScreenshot(first.ID); err != nil || got.File != "um.png" {
		t.Fatalf("GetScreenshot: %+v, erro %v", got, err)
	}

	if err := s.DeleteScreenshot(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetScreenshot(first.ID); err == nil {
		t.Fatal("a captura apagada continua no banco")
	}
	// Apagar a sessão leva as capturas dela, e só as dela
	if err := s.DeleteSession(a.ID); err != nil {
		t.Fatal(err)
	}
	if shots, _ := s.GetScreenshots(a.ID); len(shots) != 0 {
		t.Fatalf("sobraram %d capturas da sessão apagada", len(shots))
	}
	if got, err := s.GetScreenshot(other.ID); err != nil || got.SessionID != b.ID {
		t.Fatalf("a captura da outra sessão sumiu: %v", err)
	}
}
