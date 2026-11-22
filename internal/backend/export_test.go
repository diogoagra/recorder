package backend

import (
	"path/filepath"
	"recorder/internal/storage"
	"strings"
	"testing"
	"time"
)

func TestExportFilenameReplacesInvalidCharacters(t *testing.T) {
	cases := map[string]string{
		"08/10/2026 - Revisão de Sprint": "08-10-2026 - Revisão de Sprint - Transcrição.md",
		"Cliente: \"ACME\" <v2>?":        "Cliente- -ACME- -v2-- - Transcrição.md",
		"  ":                             "Transcrição.md",
	}
	for name, want := range cases {
		if got := exportFilename(name, "Transcrição"); got != want {
			t.Errorf("exportFilename(%q) = %q, esperava %q", name, got, want)
		}
	}
}

func TestTranscriptMarkdownUsesAudioTimeAndSpeaker(t *testing.T) {
	transcribed := time.Date(2026, 10, 8, 14, 5, 0, 0, time.Local)
	got := transcriptMarkdown("Reunião", []storage.Transcript{
		{Speaker: "Pessoa 1", Text: "Bom dia.", Offset: 12},
		{Text: "Sem voz identificada.", Offset: 3725},
		{Speaker: "Ana", Text: "Fala antiga.", Offset: -1, Timestamp: transcribed},
	})
	want := "# Reunião\n\n## Transcrição\n" +
		"\n[00:12] Pessoa 1: Bom dia.\n" +
		"\n[1:02:05] Sem voz identificada.\n" +
		"\n[14:05] Ana: Fala antiga.\n"
	if got != want {
		t.Fatalf("veio:\n%s\nesperava:\n%s", got, want)
	}
}

func TestSummaryMarkdownListsTasksBeforeSummary(t *testing.T) {
	got := summaryMarkdown("Reunião", "### Ata\n\nFicou decidido migrar [12:34].\n\n", []storage.Task{
		{Text: "Finalizar a cópia do banco", Owner: "Pessoa 10", Deadline: "amanhã", Offset: 754, Done: true},
		{Text: "Atualizar o ClickUp", Offset: -1},
	})
	want := "# Reunião\n\n## Tarefas\n\n" +
		"- [x] Finalizar a cópia do banco — Pessoa 10 · prazo: amanhã · [12:34]\n" +
		"- [ ] Atualizar o ClickUp\n" +
		"\n## Resumo\n\n### Ata\n\nFicou decidido migrar [12:34].\n"
	if got != want {
		t.Fatalf("veio:\n%s\nesperava:\n%s", got, want)
	}

	// Sem tarefas, o resumo vem logo depois do nome da sessão
	if got := summaryMarkdown("Reunião", "Resumo.", nil); got != "# Reunião\n\nResumo.\n" {
		t.Fatalf("sem tarefas, veio %q", got)
	}
}

func TestChatMarkdownLabelsWhoWrote(t *testing.T) {
	got := chatMarkdown("Reunião", []storage.ChatMessage{
		{Role: "user", Content: "O que ficou combinado?"},
		{Role: "assistant", Content: "- Migrar os dados [12:34]\n"},
	})
	want := "# Reunião\n\n## Chat\n" +
		"\n**Você:**\n\nO que ficou combinado?\n" +
		"\n**Assistente:**\n\n- Migrar os dados [12:34]\n"
	if got != want {
		t.Fatalf("veio:\n%s\nesperava:\n%s", got, want)
	}
}

func TestExportTextRefusesEmptyTabs(t *testing.T) {
	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess, _ := store.CreateSession("", "Reunião")
	a := &App{store: store}

	for _, tab := range []string{"summary", "transcript", "chat", "notes", "outra"} {
		if _, _, err := a.exportText(sess, tab); err == nil {
			t.Errorf("aba %q vazia deveria dar erro", tab)
		}
	}

	store.InsertTranscript(sess.ID, "Audio Geral", "Pessoa 1", "Bom dia.", 12)
	title, text, err := a.exportText(sess, "transcript")
	if err != nil || title != "Transcrição" || !strings.Contains(text, "[00:12] Pessoa 1: Bom dia.") {
		t.Fatalf("título %q, texto %q, erro %v", title, text, err)
	}
}
