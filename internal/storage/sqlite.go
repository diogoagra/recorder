package storage

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	_ "github.com/mattn/go-sqlite3"
)

type Session struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"created_at"`
	Summary     string    `json:"summary"`
	Notes       string    `json:"notes"` // anotações do usuário, escritas durante a reunião
}

// Workspace separa as reuniões (ex: um por emprego). Cada um tem as suas
// reuniões, as suas vozes conhecidas e o seu glossário, e a busca do chat em
// "todas as reuniões" fica dentro dele.
type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// DefaultWorkspaceName é o nome do workspace criado num banco novo ou num de
// antes dos workspaces, com as reuniões que já existiam.
const DefaultWorkspaceName = "Principal"

type Transcript struct {
	ID        int       `json:"id"`
	SessionID string    `json:"session_id"`
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"`
	Speaker   string    `json:"speaker"` // "Pessoa 1", "Pessoa 2"... vazio se não foi identificado
	Text      string    `json:"text"`
	Offset    float64   `json:"offset"` // segundos desde o início do áudio da sessão; negativo se desconhecido
}

// Who identifica a origem da fala nos textos enviados ao Ollama: a pessoa que
// falou, quando identificada, ou a fonte do áudio.
func (t Transcript) Who() string {
	if t.Speaker != "" {
		return t.Speaker
	}
	return t.Source
}

// SpeakerVoice é a "impressão digital" da voz de uma pessoa identificada na
// sessão, usada para reconhecê-la nos próximos blocos de áudio.
type SpeakerVoice struct {
	Label     string
	Embedding []float32
	Seconds   float64
}

type Storage struct {
	db   *sql.DB
	path string
}

// ErrNoSession: a sessão não existe (mais), ex: foi apagada enquanto um bloco
// dela ainda era transcrito.
var ErrNoSession = errors.New("reunião não encontrada")

func NewStorage(dbPath string) (*Storage, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	s := &Storage{db: db, path: dbPath}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

func (s *Storage) SaveChatMessage(sessionID, role, content string) (int, error) {
	query := `
		INSERT INTO chat_messages (session_id, role, content)
		VALUES (?, ?, ?)
	`
	res, err := s.db.Exec(query, sessionID, role, content)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return int(id), err
}

func (s *Storage) DeleteChatMessage(messageID int) error {
	query := `DELETE FROM chat_messages WHERE id = ?`
	_, err := s.db.Exec(query, messageID)
	return err
}

func (s *Storage) GetChatMessages(sessionID string) ([]ChatMessage, error) {
	query := `
		SELECT id, session_id, role, content, timestamp 
		FROM chat_messages 
		WHERE session_id = ? 
		ORDER BY timestamp ASC
	`
	rows, err := s.db.Query(query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []ChatMessage
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.Timestamp); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return msgs, nil
}

func (s *Storage) init() error {
	if err := s.migrate(); err != nil {
		return err
	}

	// Criar uma sessão default se não existir nenhuma, só por precaução
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count)
	if err == nil && count == 0 {
		_, _ = s.CreateSession("", "Sessão Geral")
	}

	return nil
}

// baselineSchema são as tabelas da primeira migração (migrateBaseline). As
// colunas acrescentadas depois de cada tabela existir estão lá.
var baselineSchema = `
	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		name TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS transcripts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		source TEXT,
		text TEXT,
		FOREIGN KEY (session_id) REFERENCES sessions(id)
	);

	CREATE TABLE IF NOT EXISTS chat_messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT,
		role TEXT,
		content TEXT,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (session_id) REFERENCES sessions(id)
	);

	CREATE TABLE IF NOT EXISTS speakers (
		session_id TEXT,
		label TEXT,
		embedding BLOB,
		seconds REAL,
		PRIMARY KEY (session_id, label),
		FOREIGN KEY (session_id) REFERENCES sessions(id)
	);

	CREATE TABLE IF NOT EXISTS tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT,
		text TEXT,
		owner TEXT,
		deadline TEXT,
		audio_offset REAL,
		done INTEGER DEFAULT 0,
		FOREIGN KEY (session_id) REFERENCES sessions(id)
	);

	CREATE TABLE IF NOT EXISTS speaker_suggestions (
		session_id TEXT,
		speaker TEXT,
		name TEXT,
		evidence TEXT,
		PRIMARY KEY (session_id, speaker),
		FOREIGN KEY (session_id) REFERENCES sessions(id)
	);

	CREATE TABLE IF NOT EXISTS passages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT,
		last_transcript_id INTEGER,
		audio_offset REAL,
		text TEXT,
		embedding BLOB,
		FOREIGN KEY (session_id) REFERENCES sessions(id)
	);

	CREATE TABLE IF NOT EXISTS screenshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT,
		file TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		audio_offset REAL,
		FOREIGN KEY (session_id) REFERENCES sessions(id)
	);

	CREATE TABLE IF NOT EXISTS workspaces (
		id TEXT PRIMARY KEY,
		name TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		glossary TEXT DEFAULT ''
	);

	` + peopleTable("people") + `;

	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_timestamp ON transcripts(timestamp);
	CREATE INDEX IF NOT EXISTS idx_session ON transcripts(session_id);
	`

// peopleTable cria a tabela das vozes conhecidas: o nome é único dentro do
// workspace, e a mesma pessoa pode ser conhecida em mais de um.
func peopleTable(name string) string {
	return `CREATE TABLE IF NOT EXISTS ` + name + ` (
		workspace_id TEXT,
		name TEXT,
		embedding BLOB,
		seconds REAL,
		PRIMARY KEY (workspace_id, name)
	)`
}

// migrateWorkspaces cria o primeiro workspace num banco novo ou de antes dos
// workspaces e põe nele o que era do app todo: as reuniões, as vozes
// conhecidas e o glossário. Também devolve a ele as reuniões que tenham
// ficado sem workspace.
func (s *Storage) migrateWorkspaces() error {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM workspaces").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		ws, err := s.CreateWorkspace(DefaultWorkspaceName)
		if err != nil {
			return err
		}
		if glossary, _ := s.GetSetting("glossary"); glossary != "" {
			if err := s.SetGlossary(ws.ID, glossary); err != nil {
				return err
			}
		}
	}
	first := s.firstWorkspaceID()
	if _, err := s.db.Exec(`UPDATE sessions SET workspace_id = ?
		WHERE workspace_id IS NULL OR workspace_id NOT IN (SELECT id FROM workspaces)`, first); err != nil {
		return err
	}

	// A tabela de vozes conhecidas antiga tinha o nome como chave, sem workspace
	columns, err := s.db.Query("SELECT name FROM pragma_table_info('people')")
	if err != nil {
		return err
	}
	defer columns.Close()
	for columns.Next() {
		var column string
		if err := columns.Scan(&column); err != nil {
			return err
		}
		if column == "workspace_id" {
			return nil
		}
	}
	if err := columns.Err(); err != nil {
		return err
	}
	columns.Close()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, step := range []struct {
		query string
		args  []any
	}{
		{peopleTable("people_by_workspace"), nil},
		{"INSERT INTO people_by_workspace (workspace_id, name, embedding, seconds) SELECT ?, name, embedding, seconds FROM people", []any{first}},
		{"DROP TABLE people", nil},
		{"ALTER TABLE people_by_workspace RENAME TO people", nil},
	} {
		if _, err := tx.Exec(step.query, step.args...); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// CreateWorkspace cria um workspace vazio.
func (s *Storage) CreateWorkspace(name string) (*Workspace, error) {
	ws := &Workspace{ID: uuid.New().String(), Name: name, CreatedAt: time.Now()}
	_, err := s.db.Exec(`INSERT INTO workspaces (id, name, created_at, glossary) VALUES (?, ?, ?, '')`, ws.ID, ws.Name, ws.CreatedAt)
	if err != nil {
		return nil, err
	}
	return ws, nil
}

// GetWorkspaces lista os workspaces na ordem em que foram criados.
func (s *Storage) GetWorkspaces() ([]Workspace, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at FROM workspaces ORDER BY created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	workspaces := []Workspace{}
	for rows.Next() {
		var ws Workspace
		if err := rows.Scan(&ws.ID, &ws.Name, &ws.CreatedAt); err != nil {
			return nil, err
		}
		workspaces = append(workspaces, ws)
	}
	return workspaces, rows.Err()
}

func (s *Storage) RenameWorkspace(workspaceID, name string) error {
	_, err := s.db.Exec(`UPDATE workspaces SET name = ? WHERE id = ?`, name, workspaceID)
	return err
}

// DeleteWorkspace apaga o workspace e as vozes conhecidas nele. As reuniões
// precisam ser apagadas antes (o áudio delas fica fora do banco); se sobrar
// alguma, ela volta para o primeiro workspace na próxima vez que o app abrir.
func (s *Storage) DeleteWorkspace(workspaceID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM people WHERE workspace_id = ?", workspaceID); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec("DELETE FROM person_voices WHERE workspace_id = ?", workspaceID); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec("DELETE FROM workspaces WHERE id = ?", workspaceID); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Storage) workspaceExists(workspaceID string) bool {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE id = ?", workspaceID).Scan(&n)
	return err == nil && n > 0
}

func (s *Storage) firstWorkspaceID() string {
	var id string
	s.db.QueryRow("SELECT id FROM workspaces ORDER BY created_at, rowid LIMIT 1").Scan(&id)
	return id
}

// CurrentWorkspaceID devolve o workspace aberto por último na tela, ou o
// primeiro, se aquele não existir mais.
func (s *Storage) CurrentWorkspaceID() string {
	if id, _ := s.GetSetting("current_workspace"); id != "" && s.workspaceExists(id) {
		return id
	}
	return s.firstWorkspaceID()
}

func (s *Storage) SetCurrentWorkspace(workspaceID string) error {
	if !s.workspaceExists(workspaceID) {
		return fmt.Errorf("workspace não encontrado")
	}
	return s.SetSetting("current_workspace", workspaceID)
}

// SessionWorkspace devolve o workspace da sessão; para uma sessão que não
// existe, o workspace atual.
func (s *Storage) SessionWorkspace(sessionID string) string {
	var id string
	if err := s.db.QueryRow("SELECT IFNULL(workspace_id, '') FROM sessions WHERE id = ?", sessionID).Scan(&id); err != nil || id == "" {
		return s.CurrentWorkspaceID()
	}
	return id
}

// MoveSession passa a sessão para outro workspace.
func (s *Storage) MoveSession(sessionID, workspaceID string) error {
	if !s.workspaceExists(workspaceID) {
		return fmt.Errorf("workspace não encontrado")
	}
	_, err := s.db.Exec("UPDATE sessions SET workspace_id = ? WHERE id = ?", workspaceID, sessionID)
	return err
}

// WorkspaceSessionIDs devolve os ids das sessões do workspace.
func (s *Storage) WorkspaceSessionIDs(workspaceID string) ([]string, error) {
	return s.sessionIDs("SELECT id FROM sessions WHERE workspace_id = ?", workspaceID)
}

// SessionsWithSpeaker devolve as sessões do workspace com falas ou vozes de label.
func (s *Storage) SessionsWithSpeaker(workspaceID, label string) ([]string, error) {
	return s.sessionIDs(`SELECT id FROM sessions WHERE workspace_id = ? AND (
		EXISTS (SELECT 1 FROM transcripts t WHERE t.session_id = sessions.id AND t.speaker = ?)
		OR EXISTS (SELECT 1 FROM speakers v WHERE v.session_id = sessions.id AND v.label = ?))`, workspaceID, label, label)
}

func (s *Storage) sessionIDs(query string, args ...any) ([]string, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetGlossary devolve os termos que o Whisper deve reconhecer nas reuniões do workspace.
func (s *Storage) GetGlossary(workspaceID string) (string, error) {
	var glossary string
	err := s.db.QueryRow("SELECT IFNULL(glossary, '') FROM workspaces WHERE id = ?", workspaceID).Scan(&glossary)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return glossary, err
}

func (s *Storage) SetGlossary(workspaceID, glossary string) error {
	_, err := s.db.Exec("UPDATE workspaces SET glossary = ? WHERE id = ?", glossary, workspaceID)
	return err
}

// GetParticipants devolve a lista das pessoas que costumam estar nas reuniões
// do workspace, como o usuário escreveu (uma por linha; ver o pacote roster).
func (s *Storage) GetParticipants(workspaceID string) (string, error) {
	var participants string
	err := s.db.QueryRow("SELECT IFNULL(participants, '') FROM workspaces WHERE id = ?", workspaceID).Scan(&participants)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return participants, err
}

func (s *Storage) SetParticipants(workspaceID, participants string) error {
	_, err := s.db.Exec("UPDATE workspaces SET participants = ? WHERE id = ?", participants, workspaceID)
	return err
}

// CreateSession cria uma sessão no workspace; com workspaceID vazio ou de um
// workspace que não existe mais, no workspace atual.
func (s *Storage) CreateSession(workspaceID, name string) (*Session, error) {
	if workspaceID == "" || !s.workspaceExists(workspaceID) {
		workspaceID = s.CurrentWorkspaceID()
	}
	id := uuid.New().String()
	now := time.Now()

	query := `INSERT INTO sessions (id, workspace_id, name, created_at, summary) VALUES (?, ?, ?, ?, '')`
	_, err := s.db.Exec(query, id, workspaceID, name, now)
	if err != nil {
		return nil, err
	}

	return &Session{
		ID:          id,
		WorkspaceID: workspaceID,
		Name:        name,
		CreatedAt:   now,
		Summary:     "",
	}, nil
}

// GetSessions devolve as sessões de todos os workspaces.
func (s *Storage) GetSessions() ([]Session, error) {
	query := `SELECT id, IFNULL(workspace_id, ''), name, created_at, IFNULL(summary, ''), IFNULL(notes, '') FROM sessions ORDER BY created_at DESC`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.WorkspaceID, &sess.Name, &sess.CreatedAt, &sess.Summary, &sess.Notes); err != nil {
			return nil, err
		}
		result = append(result, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Storage) UpdateSessionSummary(sessionID string, summary string) error {
	query := `UPDATE sessions SET summary = ? WHERE id = ?`
	_, err := s.db.Exec(query, summary, sessionID)
	return err
}

// UpdateSessionNotes guarda as anotações do usuário sobre a reunião. Devolve
// ErrNoSession se ela não existir: antes, anotações mandadas para uma sessão
// vazia ou apagada sumiam sem erro.
func (s *Storage) UpdateSessionNotes(sessionID string, notes string) error {
	res, err := s.db.Exec(`UPDATE sessions SET notes = ? WHERE id = ?`, notes, sessionID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNoSession
	}
	return nil
}

// SessionExists diz se a sessão existe.
func (s *Storage) SessionExists(sessionID string) bool {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM sessions WHERE id = ?", sessionID).Scan(&n)
	return err == nil && n > 0
}

func (s *Storage) UpdateSessionName(sessionID string, name string) error {
	query := `UPDATE sessions SET name = ? WHERE id = ?`
	_, err := s.db.Exec(query, name, sessionID)
	return err
}

func (s *Storage) GetSession(sessionID string) (*Session, error) {
	query := `SELECT id, IFNULL(workspace_id, ''), name, created_at, IFNULL(summary, ''), IFNULL(notes, '') FROM sessions WHERE id = ?`
	var sess Session
	err := s.db.QueryRow(query, sessionID).Scan(&sess.ID, &sess.WorkspaceID, &sess.Name, &sess.CreatedAt, &sess.Summary, &sess.Notes)
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Storage) DeleteSession(sessionID string) error {
	// Inicia uma transação para garantir que exclua as transcrições e a sessão juntas
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	_, err = tx.Exec("DELETE FROM chat_messages WHERE session_id = ?", sessionID)
	if err != nil {
		tx.Rollback()
		return err
	}

	_, err = tx.Exec("DELETE FROM transcripts WHERE session_id = ?", sessionID)
	if err != nil {
		tx.Rollback()
		return err
	}

	for _, table := range []string{"speakers", "tasks", "speaker_suggestions", "passages", "screenshots"} {
		if _, err = tx.Exec("DELETE FROM "+table+" WHERE session_id = ?", sessionID); err != nil {
			tx.Rollback()
			return err
		}
	}

	_, err = tx.Exec("DELETE FROM sessions WHERE id = ?", sessionID)
	if err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

func (s *Storage) GetDefaultSessionID() string {
	var id string
	err := s.db.QueryRow("SELECT id FROM sessions ORDER BY created_at ASC LIMIT 1").Scan(&id)
	if err != nil {
		return ""
	}
	return id
}

// InsertTranscript grava uma fala; offset é onde ela começa no áudio da
// sessão, em segundos. Devolve ErrNoSession se a sessão não existir (mais):
// uma fala que termina de ser transcrita depois de a sessão ser apagada não
// pode ficar solta no banco.
func (s *Storage) InsertTranscript(sessionID string, source string, speaker string, text string, offset float64) (*Transcript, error) {
	now := time.Now()
	query := `INSERT INTO transcripts (session_id, timestamp, source, speaker, text, audio_offset)
		SELECT ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM sessions WHERE id = ?)`
	res, err := s.db.Exec(query, sessionID, now, source, speaker, text, offset, sessionID)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return nil, ErrNoSession
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &Transcript{
		ID:        int(id),
		SessionID: sessionID,
		Timestamp: now,
		Source:    source,
		Speaker:   speaker,
		Text:      text,
		Offset:    offset,
	}, nil
}

// RestoreTranscripts grava de volta falas tiradas do banco (ex: as de antes
// de um reprocessamento que não pôde terminar), com a hora, a pessoa e a
// posição no áudio que elas tinham.
func (s *Storage) RestoreTranscripts(sessionID string, transcripts []Transcript) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, t := range transcripts {
		var offset any // sem posição no áudio (falas antigas): NULL, como eram
		if t.Offset >= 0 {
			offset = t.Offset
		}
		if _, err := tx.Exec(`INSERT INTO transcripts (session_id, timestamp, source, speaker, text, audio_offset) VALUES (?, ?, ?, ?, ?, ?)`,
			sessionID, t.Timestamp, t.Source, t.Speaker, t.Text, offset); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// DeleteTranscriptsBetween apaga as falas da sessão que começam entre from
// (inclusive) e to, em segundos no áudio da sessão.
func (s *Storage) DeleteTranscriptsBetween(sessionID string, from, to float64) error {
	_, err := s.db.Exec("DELETE FROM transcripts WHERE session_id = ? AND audio_offset >= ? AND audio_offset < ?", sessionID, from, to)
	return err
}

func (s *Storage) GetRecentTranscripts(sessionID string, limit int) ([]Transcript, error) {
	query := `SELECT id, session_id, timestamp, source, IFNULL(speaker, ''), text, IFNULL(audio_offset, -1) FROM transcripts WHERE session_id = ? ORDER BY ` + newestFirst + ` LIMIT ?`
	rows, err := s.db.Query(query, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Transcript
	for rows.Next() {
		var t Transcript
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Timestamp, &t.Source, &t.Speaker, &t.Text, &t.Offset); err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result, nil
}

// Ordem das falas na reunião: pela posição no áudio, que vale também para
// blocos transcritos fora de ordem (ex: recuperados de uma gravação
// interrompida). As falas antigas, sem a posição, vêm antes, na ordem em que
// foram transcritas.
const (
	oldestFirst = `audio_offset IS NOT NULL, audio_offset, timestamp, id`
	newestFirst = `audio_offset IS NOT NULL DESC, audio_offset DESC, timestamp DESC, id DESC`
)

// GetAllTranscripts devolve as falas da sessão na ordem em que aconteceram.
func (s *Storage) GetAllTranscripts(sessionID string) ([]Transcript, error) {
	query := `SELECT id, session_id, timestamp, source, IFNULL(speaker, ''), text, IFNULL(audio_offset, -1) FROM transcripts WHERE session_id = ? ORDER BY ` + oldestFirst
	rows, err := s.db.Query(query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Transcript
	for rows.Next() {
		var t Transcript
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Timestamp, &t.Source, &t.Speaker, &t.Text, &t.Offset); err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

func (s *Storage) Close() error {
	return s.db.Close()
}

type ChatMessage struct {
	ID        int       `json:"id"`
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"` // 'user' ou 'assistant'
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

func (s *Storage) SetSetting(key, value string) error {
	query := `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`
	_, err := s.db.Exec(query, key, value)
	return err
}

func (s *Storage) GetSetting(key string) (string, error) {
	var value string
	query := `SELECT value FROM settings WHERE key = ?`
	err := s.db.QueryRow(query, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil // se não tiver, retorna string vazia sem dar erro crash
	}
	return value, err
}

func (s *Storage) DeleteSessionTranscripts(sessionID string) error {
	_, err := s.db.Exec("DELETE FROM transcripts WHERE session_id = ?", sessionID)
	return err
}

func (s *Storage) GetSpeakerVoices(sessionID string) ([]SpeakerVoice, error) {
	return s.queryVoices(`SELECT label, embedding, seconds FROM speakers WHERE session_id = ? ORDER BY rowid`, sessionID)
}

// SaveSpeakerVoices grava as vozes da sessão, criando as novas e atualizando
// as que já existiam.
func (s *Storage) SaveSpeakerVoices(sessionID string, voices []SpeakerVoice) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := saveVoices(tx, sessionID, voices); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ReplaceSpeakerVoices troca todas as vozes da sessão (ex: depois de renomear
// ou juntar duas vozes).
func (s *Storage) ReplaceSpeakerVoices(sessionID string, voices []SpeakerVoice) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM speakers WHERE session_id = ?", sessionID); err != nil {
		tx.Rollback()
		return err
	}
	if err := saveVoices(tx, sessionID, voices); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func saveVoices(tx *sql.Tx, sessionID string, voices []SpeakerVoice) error {
	query := `
		INSERT INTO speakers (session_id, label, embedding, seconds) VALUES (?, ?, ?, ?)
		ON CONFLICT(session_id, label) DO UPDATE SET embedding = excluded.embedding, seconds = excluded.seconds
	`
	for _, v := range voices {
		if _, err := tx.Exec(query, sessionID, v.Label, encodeEmbedding(v.Embedding), v.Seconds); err != nil {
			return err
		}
	}
	return nil
}

// RenameTranscriptSpeaker troca o rótulo das falas da sessão (ex: "Pessoa 3" -> "Ana").
func (s *Storage) RenameTranscriptSpeaker(sessionID, from, to string) error {
	_, err := s.db.Exec("UPDATE transcripts SET speaker = ? WHERE session_id = ? AND speaker = ?", to, sessionID, from)
	return err
}

// RenameTaskOwner troca o responsável das tarefas da sessão (ex: "Pessoa 3" -> "Ana").
func (s *Storage) RenameTaskOwner(sessionID, from, to string) error {
	_, err := s.db.Exec("UPDATE tasks SET owner = ? WHERE session_id = ? AND owner = ?", to, sessionID, from)
	return err
}

// GetPeopleVoices devolve as pessoas que o usuário já nomeou no workspace (Label é o nome).
func (s *Storage) GetPeopleVoices(workspaceID string) ([]SpeakerVoice, error) {
	return s.queryVoices(`SELECT name, embedding, seconds FROM people WHERE workspace_id = ? ORDER BY name`, workspaceID)
}

func (s *Storage) SavePersonVoice(workspaceID string, v SpeakerVoice) error {
	_, err := s.db.Exec(`
		INSERT INTO people (workspace_id, name, embedding, seconds) VALUES (?, ?, ?, ?)
		ON CONFLICT(workspace_id, name) DO UPDATE SET embedding = excluded.embedding, seconds = excluded.seconds
	`, workspaceID, v.Label, encodeEmbedding(v.Embedding), v.Seconds)
	return err
}

// RenamePersonVoice troca o nome de uma voz conhecida; o nome novo não pode
// existir no workspace.
func (s *Storage) RenamePersonVoice(workspaceID, from, to string) error {
	_, err := s.db.Exec("UPDATE people SET name = ? WHERE workspace_id = ? AND name = ?", to, workspaceID, from)
	return err
}

func (s *Storage) DeletePersonVoice(workspaceID, name string) error {
	_, err := s.db.Exec("DELETE FROM people WHERE workspace_id = ? AND name = ?", workspaceID, name)
	return err
}

// A voz conhecida de uma pessoa (people) é a combinação das vozes que
// receberam o nome dela, uma por reunião (person_voices; SessionID vazio é a
// voz que já estava guardada antes desse registro existir). Guardar cada uma
// permite desfazer um nome dado errado sem perder as outras.

// PersonSample é a voz que recebeu o nome de uma pessoa numa reunião.
type PersonSample struct {
	SessionID string
	SpeakerVoice
}

// GetPersonSamples devolve as vozes que receberam o nome no workspace, da mais antiga para a mais nova.
func (s *Storage) GetPersonSamples(workspaceID, name string) ([]PersonSample, error) {
	rows, err := s.db.Query(`SELECT session_id, embedding, seconds FROM person_voices
		WHERE workspace_id = ? AND name = ? ORDER BY created_at, rowid`, workspaceID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var samples []PersonSample
	for rows.Next() {
		sample := PersonSample{SpeakerVoice: SpeakerVoice{Label: name}}
		var blob []byte
		if err := rows.Scan(&sample.SessionID, &blob, &sample.Seconds); err != nil {
			return nil, err
		}
		sample.Embedding = decodeEmbedding(blob)
		samples = append(samples, sample)
	}
	return samples, rows.Err()
}

// SavePersonSample guarda (ou troca) a voz que recebeu o nome v.Label na reunião.
func (s *Storage) SavePersonSample(workspaceID, sessionID string, v SpeakerVoice) error {
	_, err := s.db.Exec(`
		INSERT INTO person_voices (workspace_id, name, session_id, embedding, seconds) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, name, session_id) DO UPDATE SET embedding = excluded.embedding, seconds = excluded.seconds
	`, workspaceID, v.Label, sessionID, encodeEmbedding(v.Embedding), v.Seconds)
	return err
}

func (s *Storage) DeletePersonSample(workspaceID, name, sessionID string) error {
	_, err := s.db.Exec("DELETE FROM person_voices WHERE workspace_id = ? AND name = ? AND session_id = ?", workspaceID, name, sessionID)
	return err
}

// DeletePersonSamples apaga todas as vozes que receberam o nome no workspace.
func (s *Storage) DeletePersonSamples(workspaceID, name string) error {
	_, err := s.db.Exec("DELETE FROM person_voices WHERE workspace_id = ? AND name = ?", workspaceID, name)
	return err
}

// SessionVoices devolve as vozes das reuniões do workspace, menos as da
// reunião exceptSessionID, com o rótulo que cada uma tem na sua reunião.
func (s *Storage) SessionVoices(workspaceID, exceptSessionID string) ([]PersonSample, error) {
	rows, err := s.db.Query(`SELECT v.session_id, v.label, v.embedding, v.seconds FROM speakers v
		JOIN sessions s ON s.id = v.session_id
		WHERE s.workspace_id = ? AND v.session_id != ? ORDER BY s.created_at, v.rowid`, workspaceID, exceptSessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var voices []PersonSample
	for rows.Next() {
		var voice PersonSample
		var blob []byte
		if err := rows.Scan(&voice.SessionID, &voice.Label, &blob, &voice.Seconds); err != nil {
			return nil, err
		}
		voice.Embedding = decodeEmbedding(blob)
		voices = append(voices, voice)
	}
	return voices, rows.Err()
}

func (s *Storage) queryVoices(query string, args ...any) ([]SpeakerVoice, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SpeakerVoice
	for rows.Next() {
		var v SpeakerVoice
		var blob []byte
		if err := rows.Scan(&v.Label, &blob, &v.Seconds); err != nil {
			return nil, err
		}
		v.Embedding = decodeEmbedding(blob)
		result = append(result, v)
	}
	return result, rows.Err()
}

func encodeEmbedding(emb []float32) []byte {
	blob := make([]byte, len(emb)*4)
	for i, x := range emb {
		binary.LittleEndian.PutUint32(blob[i*4:], math.Float32bits(x))
	}
	return blob
}

func decodeEmbedding(blob []byte) []float32 {
	emb := make([]float32, len(blob)/4)
	for i := range emb {
		emb[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return emb
}

func (s *Storage) DeleteSessionSpeakers(sessionID string) error {
	_, err := s.db.Exec("DELETE FROM speakers WHERE session_id = ?", sessionID)
	return err
}

// Task é uma tarefa combinada na reunião, extraída do resumo.
type Task struct {
	ID       int     `json:"id"`
	Text     string  `json:"text"`
	Owner    string  `json:"owner"`
	Deadline string  `json:"deadline"`
	Offset   float64 `json:"offset"` // momento no áudio da sessão em que foi combinada; negativo se desconhecido
	Done     bool    `json:"done"`
}

func (s *Storage) GetTasks(sessionID string) ([]Task, error) {
	rows, err := s.db.Query(`SELECT id, text, owner, deadline, IFNULL(audio_offset, -1), done FROM tasks WHERE session_id = ? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []Task{}
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Text, &t.Owner, &t.Deadline, &t.Offset, &t.Done); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// ReplaceTasks troca as tarefas da sessão (ex: depois de refazer o resumo).
// As que continuam com o mesmo texto mantêm a marcação de concluída.
func (s *Storage) ReplaceTasks(sessionID string, tasks []Task) error {
	old, err := s.GetTasks(sessionID)
	if err != nil {
		return err
	}
	done := map[string]bool{}
	for _, t := range old {
		done[t.Text] = t.Done
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM tasks WHERE session_id = ?", sessionID); err != nil {
		tx.Rollback()
		return err
	}
	for _, t := range tasks {
		if _, err := tx.Exec(`INSERT INTO tasks (session_id, text, owner, deadline, audio_offset, done) VALUES (?, ?, ?, ?, ?, ?)`,
			sessionID, t.Text, t.Owner, t.Deadline, t.Offset, done[t.Text]); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Storage) SetTaskDone(taskID int, done bool) error {
	_, err := s.db.Exec("UPDATE tasks SET done = ? WHERE id = ?", done, taskID)
	return err
}

// SpeakerSuggestion é um nome provável para uma voz da sessão, tirado da
// própria conversa, esperando a confirmação do usuário.
type SpeakerSuggestion struct {
	Speaker  string `json:"speaker"`
	Name     string `json:"name"`
	Evidence string `json:"evidence"` // trecho da conversa que indica o nome
}

func (s *Storage) GetSpeakerSuggestions(sessionID string) ([]SpeakerSuggestion, error) {
	rows, err := s.db.Query(`SELECT speaker, name, evidence FROM speaker_suggestions WHERE session_id = ? ORDER BY rowid`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	suggestions := []SpeakerSuggestion{}
	for rows.Next() {
		var sg SpeakerSuggestion
		if err := rows.Scan(&sg.Speaker, &sg.Name, &sg.Evidence); err != nil {
			return nil, err
		}
		suggestions = append(suggestions, sg)
	}
	return suggestions, rows.Err()
}

func (s *Storage) ReplaceSpeakerSuggestions(sessionID string, suggestions []SpeakerSuggestion) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM speaker_suggestions WHERE session_id = ?", sessionID); err != nil {
		tx.Rollback()
		return err
	}
	for _, sg := range suggestions {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO speaker_suggestions (session_id, speaker, name, evidence) VALUES (?, ?, ?, ?)`,
			sessionID, sg.Speaker, sg.Name, sg.Evidence); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Storage) DeleteSpeakerSuggestion(sessionID, speaker string) error {
	_, err := s.db.Exec("DELETE FROM speaker_suggestions WHERE session_id = ? AND speaker = ?", sessionID, speaker)
	return err
}

// Passage é um trecho da transcrição com o embedding usado na busca do chat.
type Passage struct {
	SessionID        string
	SessionName      string
	LastTranscriptID int // última fala incluída; as seguintes ainda não foram indexadas
	Offset           float64
	Text             string
	Embedding        []float32
}

// GetPassages devolve os trechos indexados de uma sessão.
func (s *Storage) GetPassages(sessionID string) ([]Passage, error) {
	return s.queryPassages("p.session_id = ?", sessionID)
}

// GetWorkspacePassages devolve os trechos indexados das sessões do workspace.
func (s *Storage) GetWorkspacePassages(workspaceID string) ([]Passage, error) {
	return s.queryPassages("s.workspace_id = ?", workspaceID)
}

func (s *Storage) queryPassages(where string, args ...any) ([]Passage, error) {
	query := `SELECT p.session_id, IFNULL(s.name, ''), p.last_transcript_id, IFNULL(p.audio_offset, -1), p.text, p.embedding
		FROM passages p LEFT JOIN sessions s ON s.id = p.session_id
		WHERE ` + where + ` ORDER BY p.id`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var passages []Passage
	for rows.Next() {
		var p Passage
		var blob []byte
		if err := rows.Scan(&p.SessionID, &p.SessionName, &p.LastTranscriptID, &p.Offset, &p.Text, &blob); err != nil {
			return nil, err
		}
		p.Embedding = decodeEmbedding(blob)
		passages = append(passages, p)
	}
	return passages, rows.Err()
}

func (s *Storage) AddPassages(passages []Passage) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, p := range passages {
		if _, err := tx.Exec(`INSERT INTO passages (session_id, last_transcript_id, audio_offset, text, embedding) VALUES (?, ?, ?, ?, ?)`,
			p.SessionID, p.LastTranscriptID, p.Offset, p.Text, encodeEmbedding(p.Embedding)); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// DeleteSessionPassages apaga o índice de busca da sessão, que é refeito na
// próxima pergunta (ex: depois de reprocessar ou de renomear uma pessoa).
func (s *Storage) DeleteSessionPassages(sessionID string) error {
	_, err := s.db.Exec("DELETE FROM passages WHERE session_id = ?", sessionID)
	return err
}

// GetTranscriptsAfter devolve as falas da sessão com id maior que afterID, em ordem.
func (s *Storage) GetTranscriptsAfter(sessionID string, afterID int) ([]Transcript, error) {
	query := `SELECT id, session_id, timestamp, source, IFNULL(speaker, ''), text, IFNULL(audio_offset, -1)
		FROM transcripts WHERE session_id = ? AND id > ? ORDER BY ` + oldestFirst
	rows, err := s.db.Query(query, sessionID, afterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Transcript
	for rows.Next() {
		var t Transcript
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Timestamp, &t.Source, &t.Speaker, &t.Text, &t.Offset); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LastIndexedTranscript devolve o id da última fala da sessão que já está no
// índice da busca do chat (0 se nenhuma), sem carregar os trechos.
func (s *Storage) LastIndexedTranscript(sessionID string) (int, error) {
	var last int
	err := s.db.QueryRow("SELECT IFNULL(MAX(last_transcript_id), 0) FROM passages WHERE session_id = ?", sessionID).Scan(&last)
	return last, err
}

// Screenshot é uma captura de tela guardada com a reunião (ex: um slide
// mostrado na chamada).
type Screenshot struct {
	ID        int       `json:"id"`
	SessionID string    `json:"session_id"`
	File      string    `json:"file"` // nome do arquivo na pasta de capturas da sessão
	CreatedAt time.Time `json:"created_at"`
	Offset    float64   `json:"offset"` // momento no áudio da sessão; negativo se não estava gravando
}

// AddScreenshot registra uma captura da sessão; offset é o momento do áudio
// em que ela foi feita, em segundos (negativo se a reunião não estava sendo gravada).
func (s *Storage) AddScreenshot(sessionID, file string, offset float64) (*Screenshot, error) {
	now := time.Now()
	res, err := s.db.Exec(`INSERT INTO screenshots (session_id, file, created_at, audio_offset) VALUES (?, ?, ?, ?)`, sessionID, file, now, offset)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &Screenshot{ID: int(id), SessionID: sessionID, File: file, CreatedAt: now, Offset: offset}, nil
}

// GetScreenshots devolve as capturas da sessão, da mais antiga para a mais nova.
func (s *Storage) GetScreenshots(sessionID string) ([]Screenshot, error) {
	rows, err := s.db.Query(`SELECT id, session_id, file, created_at, IFNULL(audio_offset, -1) FROM screenshots WHERE session_id = ? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	shots := []Screenshot{}
	for rows.Next() {
		var shot Screenshot
		if err := rows.Scan(&shot.ID, &shot.SessionID, &shot.File, &shot.CreatedAt, &shot.Offset); err != nil {
			return nil, err
		}
		shots = append(shots, shot)
	}
	return shots, rows.Err()
}

func (s *Storage) GetScreenshot(id int) (*Screenshot, error) {
	var shot Screenshot
	err := s.db.QueryRow(`SELECT id, session_id, file, created_at, IFNULL(audio_offset, -1) FROM screenshots WHERE id = ?`, id).
		Scan(&shot.ID, &shot.SessionID, &shot.File, &shot.CreatedAt, &shot.Offset)
	if err != nil {
		return nil, err
	}
	return &shot, nil
}

func (s *Storage) DeleteScreenshot(id int) error {
	_, err := s.db.Exec("DELETE FROM screenshots WHERE id = ?", id)
	return err
}
