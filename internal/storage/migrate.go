package storage

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// O esquema do banco tem uma versão, guardada no próprio arquivo (PRAGMA
// user_version). Ao abrir um banco de uma versão anterior, o app faz primeiro
// uma cópia dele (pasta backups, ao lado do banco) e só então aplica os passos
// que faltam, em ordem. Antes, os ALTER TABLE rodavam em toda abertura com o
// erro ignorado, e uma migração errada valia direto no banco de verdade.
//
// Para mudar o esquema: acrescente um passo no fim de migrations. Um passo já
// publicado nunca muda. Cada passo precisa poder rodar de novo sem estragar
// nada (IF NOT EXISTS, addColumn), porque o app pode fechar no meio dele.
var migrations = []struct {
	name string
	run  func(s *Storage) error
}{
	{"esquema até as notas e capturas de tela", (*Storage).migrateBaseline},
	{"índices por sessão", (*Storage).migrateSessionIndexes},
	{"participantes do workspace", (*Storage).migrateParticipants},
	{"vozes de cada pessoa por reunião", (*Storage).migratePersonVoices},
}

// Quantos backups de antes de uma migração ficam guardados.
const keptBackups = 3

func (s *Storage) migrate() error {
	version, err := s.schemaVersion()
	if err != nil {
		return err
	}
	if version > len(migrations) {
		// Banco criado por uma versão mais nova do app (ex: um Recorder antigo
		// aberto depois de um novo). As mudanças de esquema só acrescentam, então
		// ele segue funcionando; avisa para o caso de algo estranho aparecer.
		log.Printf("Aviso: o banco é de uma versão mais nova do Recorder (esquema %d; este app conhece até o %d)", version, len(migrations))
		return nil
	}
	if version == len(migrations) {
		return nil
	}

	existing, err := s.hasTables()
	if err != nil {
		return err
	}
	if existing {
		backup, err := s.backupBeforeMigration(version)
		if err != nil {
			return fmt.Errorf("não foi possível copiar o banco antes de atualizá-lo: %w", err)
		}
		if backup != "" {
			log.Printf("-> Atualizando o banco do esquema %d para o %d; cópia de antes em %s", version, len(migrations), backup)
		}
	}
	for i := version; i < len(migrations); i++ {
		m := migrations[i]
		if err := m.run(s); err != nil {
			return fmt.Errorf("migração %d (%s): %w", i+1, m.name, err)
		}
		// PRAGMA não aceita parâmetro; i é um número nosso
		if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			return fmt.Errorf("migração %d (%s): %w", i+1, m.name, err)
		}
	}
	return nil
}

func (s *Storage) schemaVersion() (int, error) {
	var version int
	err := s.db.QueryRow("PRAGMA user_version").Scan(&version)
	return version, err
}

// hasTables diz se o banco já tinha alguma coisa (não é um arquivo novo).
func (s *Storage) hasTables() (bool, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'").Scan(&n)
	return n > 0, err
}

// backupBeforeMigration copia o banco para a pasta backups, ao lado dele, e
// apaga as cópias mais antigas. Devolve "" para um banco que não está num
// arquivo (em memória).
func (s *Storage) backupBeforeMigration(version int) (string, error) {
	if s.path == "" || strings.HasPrefix(s.path, ":memory:") || strings.HasPrefix(s.path, "file:") {
		return "", nil
	}
	dir := filepath.Join(filepath.Dir(s.path), "backups")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	base := strings.TrimSuffix(filepath.Base(s.path), filepath.Ext(s.path))
	backup := filepath.Join(dir, fmt.Sprintf("%s-esquema%d-%s.db", base, version, time.Now().Format("20060102-150405")))
	// Dois apps abrindo no mesmo segundo: a cópia que já existe serve
	if _, err := os.Stat(backup); err == nil {
		return backup, nil
	}
	// VACUUM INTO grava uma cópia íntegra mesmo com o banco em uso
	if _, err := s.db.Exec("VACUUM INTO '" + strings.ReplaceAll(backup, "'", "''") + "'"); err != nil {
		os.Remove(backup)
		return "", err
	}

	old, _ := filepath.Glob(filepath.Join(dir, base+"-esquema*.db"))
	sort.Slice(old, func(i, j int) bool { return modTime(old[i]).Before(modTime(old[j])) })
	for len(old) > keptBackups {
		os.Remove(old[0])
		old = old[1:]
	}
	return backup, nil
}

func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// addColumn acrescenta a coluna à tabela, se ela ainda não existir.
func (s *Storage) addColumn(table, column, definition string) error {
	has, err := s.hasColumn(table, column)
	if err != nil || has {
		return err
	}
	_, err = s.db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition)
	return err
}

func (s *Storage) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// migrateBaseline leva um banco novo, ou de qualquer versão do app anterior
// às migrações numeradas, ao esquema das notas e capturas de tela.
func (s *Storage) migrateBaseline() error {
	if _, err := s.db.Exec(baselineSchema); err != nil {
		return fmt.Errorf("failed to init tables: %w", err)
	}
	for _, c := range []struct{ table, column, definition string }{
		{"sessions", "summary", "TEXT DEFAULT ''"},
		{"transcripts", "speaker", "TEXT DEFAULT ''"},
		{"transcripts", "audio_offset", "REAL"},
		{"sessions", "workspace_id", "TEXT"},
		{"sessions", "notes", "TEXT DEFAULT ''"},
	} {
		if err := s.addColumn(c.table, c.column, c.definition); err != nil {
			return fmt.Errorf("coluna %s.%s: %w", c.table, c.column, err)
		}
	}
	if err := s.migrateWorkspaces(); err != nil {
		return fmt.Errorf("failed to migrate workspaces: %w", err)
	}
	return nil
}

// migrateSessionIndexes cria os índices das tabelas consultadas por sessão,
// que só as transcrições tinham.
func (s *Storage) migrateSessionIndexes() error {
	_, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_transcripts_session_offset ON transcripts(session_id, audio_offset);
		CREATE INDEX IF NOT EXISTS idx_chat_messages_session ON chat_messages(session_id);
		CREATE INDEX IF NOT EXISTS idx_tasks_session ON tasks(session_id);
		CREATE INDEX IF NOT EXISTS idx_passages_session ON passages(session_id);
		CREATE INDEX IF NOT EXISTS idx_screenshots_session ON screenshots(session_id);
		CREATE INDEX IF NOT EXISTS idx_sessions_workspace ON sessions(workspace_id);
	`)
	return err
}

// migrateParticipants cria a lista das pessoas que costumam estar nas reuniões
// de cada workspace.
func (s *Storage) migrateParticipants() error {
	return s.addColumn("workspaces", "participants", "TEXT DEFAULT ''")
}

// migratePersonVoices cria o registro das vozes que deram origem à voz
// conhecida de cada pessoa: uma por reunião em que ela recebeu o nome. Quem já
// tinha a voz guardada fica com ela como primeiro registro (sem reunião).
func (s *Storage) migratePersonVoices() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS person_voices (
			workspace_id TEXT,
			name TEXT,
			session_id TEXT,
			embedding BLOB,
			seconds REAL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (workspace_id, name, session_id)
		);
		INSERT OR IGNORE INTO person_voices (workspace_id, name, session_id, embedding, seconds)
			SELECT workspace_id, name, '', embedding, seconds FROM people;
	`)
	return err
}
