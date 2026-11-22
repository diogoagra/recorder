package storage

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func backups(t *testing.T, dbPath string) []string {
	t.Helper()
	found, _ := filepath.Glob(filepath.Join(filepath.Dir(dbPath), "backups", "*.db"))
	return found
}

func TestNewDatabaseGetsCurrentSchemaWithoutBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := NewStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.schemaVersion(); v != len(migrations) {
		t.Fatalf("esquema %d, esperava %d", v, len(migrations))
	}
	if found := backups(t, path); len(found) != 0 {
		t.Fatalf("um banco novo não tem o que copiar, mas há %v", found)
	}
	for _, index := range []string{"idx_transcripts_session_offset", "idx_chat_messages_session", "idx_tasks_session", "idx_passages_session", "idx_screenshots_session"} {
		var n int
		s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", index).Scan(&n)
		if n != 1 {
			t.Errorf("índice %s não foi criado", index)
		}
	}
}

// Um banco de antes das migrações numeradas (user_version 0, com dados): o
// app guarda uma cópia antes de mexer e só atualiza uma vez.
func TestOldDatabaseIsBackedUpBeforeMigrating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	old, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, name TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`INSERT INTO sessions (id, name) VALUES ('s1', 'Reunião antiga')`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	s, err := NewStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	if sess, err := s.GetSession("s1"); err != nil || sess.Name != "Reunião antiga" {
		t.Fatalf("a reunião antiga deveria continuar no banco atualizado: %+v, %v", sess, err)
	}
	s.Close()

	found := backups(t, path)
	if len(found) != 1 {
		t.Fatalf("esperava uma cópia do banco de antes da migração, há %v", found)
	}
	// A cópia é o banco como era: a reunião está lá e as colunas novas não
	copyDB, err := sql.Open("sqlite3", found[0])
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	var name string
	if err := copyDB.QueryRow("SELECT name FROM sessions WHERE id = 's1'").Scan(&name); err != nil || name != "Reunião antiga" {
		t.Fatalf("a cópia não tem a reunião antiga: %q, %v", name, err)
	}
	if _, err := copyDB.Exec("SELECT notes FROM sessions"); err == nil {
		t.Fatal("a cópia deveria ser de antes da migração, sem a coluna notes")
	}

	// Abrir de novo: já está atualizado, sem cópia nova
	s, err = NewStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if found := backups(t, path); len(found) != 1 {
		t.Fatalf("abrir um banco já atualizado não deveria copiar de novo: %v", found)
	}
}

func TestOnlyTheLastBackupsAreKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := NewStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := filepath.Join(filepath.Dir(path), "backups")
	os.MkdirAll(dir, 0700)
	for i := range 5 {
		old := filepath.Join(dir, "t-esquema0-2026010"+string(rune('1'+i))+"-000000.db")
		os.WriteFile(old, []byte("antigo"), 0600)
		when := time.Now().Add(time.Duration(i-10) * time.Hour)
		os.Chtimes(old, when, when)
	}
	backup, err := s.backupBeforeMigration(1)
	if err != nil {
		t.Fatal(err)
	}
	found := backups(t, path)
	if len(found) != keptBackups {
		t.Fatalf("esperava %d cópias guardadas, há %d: %v", keptBackups, len(found), found)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal("a cópia recém-feita foi apagada junto com as antigas")
	}
}

func TestAddColumnReportsRealErrors(t *testing.T) {
	s, err := NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Já existe: não é erro, e não duplica
	if err := s.addColumn("sessions", "notes", "TEXT DEFAULT ''"); err != nil {
		t.Fatalf("coluna que já existe: %v", err)
	}
	if err := s.addColumn("sessions", "nova", "TEXT DEFAULT 'x'"); err != nil {
		t.Fatal(err)
	}
	if has, _ := s.hasColumn("sessions", "nova"); !has {
		t.Fatal("a coluna nova não foi criada")
	}
	// Um erro de verdade (definição inválida) não pode ser engolido
	if err := s.addColumn("sessions", "quebrada", "TEXT NOT NULL"); err == nil {
		t.Fatal("esperava o erro do SQLite (NOT NULL sem valor padrão)")
	}
}

// Um Recorder antigo aberto depois de um mais novo ter atualizado o banco.
func TestDatabaseFromNewerAppStillOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := NewStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := s.CreateSession("", "reunião")
	s.db.Exec("PRAGMA user_version = 999")
	s.Close()

	s, err = NewStorage(path)
	if err != nil {
		t.Fatalf("o banco de uma versão mais nova deveria abrir: %v", err)
	}
	defer s.Close()
	if _, err := s.GetSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.schemaVersion(); v != 999 {
		t.Fatalf("a versão do banco não pode voltar atrás: %d", v)
	}
}

func TestWritesToMissingSessionAreRefused(t *testing.T) {
	s, err := NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sess, _ := s.CreateSession("", "reunião")
	if !s.SessionExists(sess.ID) || s.SessionExists("") || s.SessionExists("nao-existe") {
		t.Fatal("SessionExists errou")
	}
	if _, err := s.InsertTranscript(sess.ID, "", "Pessoa 1", "fala", 1); err != nil {
		t.Fatal(err)
	}
	s.DeleteSession(sess.ID)

	// Um bloco que termina de ser transcrito depois de a sessão ser apagada
	if _, err := s.InsertTranscript(sess.ID, "", "Pessoa 1", "fala atrasada", 31); !errors.Is(err, ErrNoSession) {
		t.Fatalf("InsertTranscript numa sessão apagada: %v, esperava ErrNoSession", err)
	}
	if all, _ := s.GetAllTranscripts(sess.ID); len(all) != 0 {
		t.Fatalf("ficaram falas soltas de uma sessão apagada: %+v", all)
	}
	// Notas mandadas para uma sessão vazia (tela sem reunião carregada) ou apagada
	for _, id := range []string{"", sess.ID} {
		if err := s.UpdateSessionNotes(id, "anotação"); !errors.Is(err, ErrNoSession) {
			t.Fatalf("UpdateSessionNotes(%q): %v, esperava ErrNoSession", id, err)
		}
	}
}

func TestTranscriptsAfterAndLastIndexed(t *testing.T) {
	s, err := NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sess, _ := s.CreateSession("", "reunião")
	other, _ := s.CreateSession("", "outra")
	a, _ := s.InsertTranscript(sess.ID, "", "", "um", 0)
	// Blocos transcritos fora de ordem: o id maior vem antes no áudio
	c, _ := s.InsertTranscript(sess.ID, "", "", "três", 60)
	b, _ := s.InsertTranscript(sess.ID, "", "", "dois", 30)
	s.InsertTranscript(other.ID, "", "", "de outra reunião", 0)

	if last, err := s.LastIndexedTranscript(sess.ID); err != nil || last != 0 {
		t.Fatalf("sem índice: %d, %v", last, err)
	}
	after, err := s.GetTranscriptsAfter(sess.ID, a.ID)
	if err != nil || len(after) != 2 || after[0].ID != b.ID || after[1].ID != c.ID {
		t.Fatalf("GetTranscriptsAfter: %+v, %v (esperava dois e três, na ordem do áudio)", after, err)
	}
	if err := s.AddPassages([]Passage{{SessionID: sess.ID, LastTranscriptID: a.ID, Text: "um"}, {SessionID: sess.ID, LastTranscriptID: b.ID, Text: "dois"}}); err != nil {
		t.Fatal(err)
	}
	if last, _ := s.LastIndexedTranscript(sess.ID); last != b.ID {
		t.Fatalf("última fala indexada %d, esperava %d", last, b.ID)
	}
	if last, _ := s.LastIndexedTranscript(other.ID); last != 0 {
		t.Fatalf("o índice de outra reunião não conta: %d", last)
	}
}

func TestRestoreTranscriptsKeepsWhatTheyHad(t *testing.T) {
	s, err := NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sess, _ := s.CreateSession("", "reunião")
	// Uma fala antiga, sem posição no áudio, e duas com
	s.db.Exec(`INSERT INTO transcripts (session_id, timestamp, source, speaker, text) VALUES (?, ?, 'Audio Geral', '', 'antiga')`, sess.ID, time.Now().Add(-time.Hour))
	s.InsertTranscript(sess.ID, "Audio Geral", "Ana", "primeira", 5)
	s.InsertTranscript(sess.ID, "Audio Reprocessado", "Bruno", "segunda", 31)
	before, _ := s.GetAllTranscripts(sess.ID)

	s.DeleteSessionTranscripts(sess.ID)
	if err := s.RestoreTranscripts(sess.ID, before); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetAllTranscripts(sess.ID)
	if len(after) != 3 {
		t.Fatalf("%d falas, esperava 3", len(after))
	}
	for i := range before {
		b, a := before[i], after[i]
		if a.Text != b.Text || a.Speaker != b.Speaker || a.Source != b.Source || a.Offset != b.Offset || !a.Timestamp.Equal(b.Timestamp) {
			t.Fatalf("fala %d voltou diferente: %+v, era %+v", i, a, b)
		}
	}
	if after[0].Text != "antiga" || after[0].Offset != -1 {
		t.Fatalf("a fala sem posição no áudio deveria continuar sem (e vir primeiro): %+v", after[0])
	}
}

// Um banco do esquema 2 (antes dos participantes e das vozes por reunião): as
// vozes conhecidas viram o primeiro registro de cada pessoa, e nada se perde.
func TestMigratesKnownVoicesIntoSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := NewStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := s.CreateSession("", "reunião")
	ws := sess.WorkspaceID
	s.SavePersonVoice(ws, SpeakerVoice{Label: "Caio", Embedding: []float32{0, 1}, Seconds: 300})
	// Volta ao esquema 2, como um banco do app anterior
	for _, q := range []string{"DROP TABLE person_voices", "ALTER TABLE workspaces DROP COLUMN participants", "PRAGMA user_version = 2"} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	s, err = NewStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.schemaVersion(); v != len(migrations) {
		t.Fatalf("esquema %d, esperava %d", v, len(migrations))
	}
	if len(backups(t, path)) != 1 {
		t.Fatal("esperava uma cópia do banco de antes da migração")
	}
	if people, _ := s.GetPeopleVoices(ws); len(people) != 1 || people[0].Label != "Caio" {
		t.Fatalf("a voz conhecida deveria continuar: %+v", people)
	}
	samples, err := s.GetPersonSamples(ws, "Caio")
	if err != nil || len(samples) != 1 || samples[0].SessionID != "" || samples[0].Seconds != 300 || samples[0].Embedding[1] != 1 {
		t.Fatalf("a voz guardada deveria ser o primeiro registro do Caio: %+v, %v", samples, err)
	}
	if list, err := s.GetParticipants(ws); err != nil || list != "" {
		t.Fatalf("participantes de um workspace antigo: %q, %v", list, err)
	}
}

func TestParticipantsAndPersonSamples(t *testing.T) {
	s, err := NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, _ := s.CreateSession("", "reunião 1")
	ws := first.WorkspaceID
	second, _ := s.CreateSession(ws, "reunião 2")
	other, _ := s.CreateWorkspace("Emprego B")
	elsewhere, _ := s.CreateSession(other.ID, "reunião no emprego B")

	// A lista de participantes é de cada workspace
	if err := s.SetParticipants(ws, "Ana Souza (Aninha)\nBruno Lima"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetParticipants(ws); got != "Ana Souza (Aninha)\nBruno Lima" {
		t.Fatalf("participantes: %q", got)
	}
	if got, _ := s.GetParticipants(other.ID); got != "" {
		t.Fatalf("o outro workspace não tem participantes, veio %q", got)
	}

	// Uma voz por reunião em que a pessoa recebeu o nome; salvar de novo troca
	s.SavePersonSample(ws, first.ID, SpeakerVoice{Label: "Ana", Embedding: []float32{1, 0}, Seconds: 10})
	s.SavePersonSample(ws, second.ID, SpeakerVoice{Label: "Ana", Embedding: []float32{0, 1}, Seconds: 20})
	s.SavePersonSample(ws, first.ID, SpeakerVoice{Label: "Ana", Embedding: []float32{1, 1}, Seconds: 30})
	s.SavePersonSample(other.ID, elsewhere.ID, SpeakerVoice{Label: "Ana", Embedding: []float32{0, 0}, Seconds: 5})
	samples, _ := s.GetPersonSamples(ws, "Ana")
	if len(samples) != 2 || samples[0].SessionID != first.ID || samples[0].Seconds != 30 || samples[1].SessionID != second.ID {
		t.Fatalf("vozes da Ana por reunião: %+v", samples)
	}
	s.DeletePersonSample(ws, "Ana", first.ID)
	if samples, _ := s.GetPersonSamples(ws, "Ana"); len(samples) != 1 || samples[0].SessionID != second.ID {
		t.Fatalf("depois de tirar a reunião 1: %+v", samples)
	}
	s.DeletePersonSamples(ws, "Ana")
	if samples, _ := s.GetPersonSamples(ws, "Ana"); len(samples) != 0 {
		t.Fatalf("depois de esquecer a Ana: %+v", samples)
	}
	if samples, _ := s.GetPersonSamples(other.ID, "Ana"); len(samples) != 1 {
		t.Fatalf("a Ana do outro workspace não deveria mudar: %+v", samples)
	}

	// As vozes das outras reuniões do workspace
	s.SaveSpeakerVoices(first.ID, []SpeakerVoice{{Label: "Ana", Embedding: []float32{1, 0}, Seconds: 40}, {Label: "Pessoa 2", Embedding: []float32{0, 1}, Seconds: 9}})
	s.SaveSpeakerVoices(second.ID, []SpeakerVoice{{Label: "Ana", Embedding: []float32{1, 0}, Seconds: 50}})
	s.SaveSpeakerVoices(elsewhere.ID, []SpeakerVoice{{Label: "Ana", Embedding: []float32{1, 0}, Seconds: 60}})
	voices, err := s.SessionVoices(ws, second.ID)
	if err != nil || len(voices) != 2 || voices[0].SessionID != first.ID || voices[0].Label != "Ana" || voices[0].Seconds != 40 || voices[1].Label != "Pessoa 2" {
		t.Fatalf("vozes das outras reuniões do workspace: %+v, %v", voices, err)
	}

	// Apagar o workspace leva as vozes por reunião dele
	s.DeleteWorkspace(other.ID)
	if samples, _ := s.GetPersonSamples(other.ID, "Ana"); len(samples) != 0 {
		t.Fatalf("as vozes do workspace apagado deveriam sumir: %+v", samples)
	}
}
