package backend

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"recorder/internal/assistant"
	"recorder/internal/storage"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ExportSession salva num arquivo escolhido pelo usuário uma das abas da
// sessão: "summary" (resumo e tarefas), "transcript", "chat" ou "notes" (as
// notas e onde estão as capturas de tela). O texto sai em
// Markdown, que também se lê bem como texto puro. Devolve onde o arquivo foi
// salvo, ou "" se o usuário desistiu.
func (a *App) ExportSession(sessionID, tab string) (string, error) {
	sess, err := a.store.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	title, text, err := a.exportText(sess, tab)
	if err != nil {
		return "", err
	}
	path, err := runtime.SaveFileDialog(a.wailsCtx(), runtime.SaveDialogOptions{
		Title:                "Exportar " + strings.ToLower(title),
		DefaultFilename:      exportFilename(sess.Name, title),
		CanCreateDirectories: true,
		Filters: []runtime.FileFilter{
			{DisplayName: "Markdown (*.md)", Pattern: "*.md"},
			{DisplayName: "Texto (*.txt)", Pattern: "*.txt"},
		},
	})
	if err != nil || path == "" {
		return "", err
	}
	if filepath.Ext(path) == "" {
		path += ".md"
	}
	return path, os.WriteFile(path, []byte(text), 0644)
}

// exportText monta o texto de uma aba da sessão. Devolve também o nome da aba,
// para o título da janela e o nome do arquivo.
func (a *App) exportText(sess *storage.Session, tab string) (title, text string, err error) {
	switch tab {
	case "summary":
		tasks, err := a.store.GetTasks(sess.ID)
		if err != nil {
			return "", "", err
		}
		if strings.TrimSpace(sess.Summary) == "" && len(tasks) == 0 {
			return "", "", fmt.Errorf("a sessão ainda não tem resumo")
		}
		return "Resumo", summaryMarkdown(sess.Name, sess.Summary, tasks), nil
	case "transcript":
		transcripts, err := a.store.GetAllTranscripts(sess.ID)
		if err != nil {
			return "", "", err
		}
		if len(transcripts) == 0 {
			return "", "", fmt.Errorf("a sessão ainda não tem falas transcritas")
		}
		return "Transcrição", transcriptMarkdown(sess.Name, transcripts), nil
	case "chat":
		messages, err := a.store.GetChatMessages(sess.ID)
		if err != nil {
			return "", "", err
		}
		if len(messages) == 0 {
			return "", "", fmt.Errorf("o chat da sessão ainda não tem mensagens")
		}
		return "Chat", chatMarkdown(sess.Name, messages), nil
	case "notes":
		shots, err := a.store.GetScreenshots(sess.ID)
		if err != nil {
			return "", "", err
		}
		if strings.TrimSpace(sess.Notes) == "" && len(shots) == 0 {
			return "", "", fmt.Errorf("a sessão ainda não tem notas")
		}
		return "Notas", notesMarkdown(sess.Name, sess.Notes, shots, screenshotsDir(sess.ID)), nil
	}
	return "", "", fmt.Errorf("aba desconhecida: %q", tab)
}

// summaryMarkdown escreve as tarefas, marcadas as já feitas, e o resumo, na
// ordem da tela.
func summaryMarkdown(name, summary string, tasks []storage.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", name)
	if len(tasks) > 0 {
		b.WriteString("## Tarefas\n\n")
		for _, t := range tasks {
			check := " "
			if t.Done {
				check = "x"
			}
			var details []string
			if t.Owner != "" {
				details = append(details, t.Owner)
			}
			if t.Deadline != "" {
				details = append(details, "prazo: "+t.Deadline)
			}
			if t.Offset >= 0 {
				details = append(details, "["+assistant.FormatOffset(t.Offset)+"]")
			}
			fmt.Fprintf(&b, "- [%s] %s", check, t.Text)
			if len(details) > 0 {
				fmt.Fprintf(&b, " — %s", strings.Join(details, " · "))
			}
			b.WriteString("\n")
		}
		if strings.TrimSpace(summary) != "" {
			b.WriteString("\n## Resumo\n\n")
		}
	}
	if strings.TrimSpace(summary) != "" {
		fmt.Fprintf(&b, "%s\n", strings.TrimSpace(summary))
	}
	return b.String()
}

// transcriptMarkdown escreve uma fala por parágrafo, como "[12:34] Pessoa 3:
// ...". Como na tela, as falas antigas, sem a posição no áudio, levam a hora
// em que foram transcritas.
func transcriptMarkdown(name string, transcripts []storage.Transcript) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n## Transcrição\n", name)
	for _, t := range transcripts {
		when := t.Timestamp.Local().Format("15:04")
		if t.Offset >= 0 {
			when = assistant.FormatOffset(t.Offset)
		}
		if t.Speaker != "" {
			fmt.Fprintf(&b, "\n[%s] %s: %s\n", when, t.Speaker, t.Text)
		} else {
			fmt.Fprintf(&b, "\n[%s] %s\n", when, t.Text)
		}
	}
	return b.String()
}

// chatMarkdown escreve as perguntas e as respostas do chat em ordem.
func chatMarkdown(name string, messages []storage.ChatMessage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n## Chat\n", name)
	for _, m := range messages {
		who := "Você"
		if m.Role == "assistant" {
			who = "Assistente"
		}
		fmt.Fprintf(&b, "\n**%s:**\n\n%s\n", who, strings.TrimSpace(m.Content))
	}
	return b.String()
}

// notesMarkdown escreve as notas como o usuário as digitou e, embaixo, onde
// está o arquivo de cada captura de tela (em dir), com o momento da reunião
// em que foi feita ou, se não estava gravando, a hora.
func notesMarkdown(name, notes string, shots []storage.Screenshot, dir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n## Notas\n", name)
	if strings.TrimSpace(notes) != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(notes))
	}
	if len(shots) > 0 {
		b.WriteString("\n## Capturas de tela\n\n")
		for _, shot := range shots {
			when := shot.CreatedAt.Local().Format("15:04")
			if shot.Offset >= 0 {
				when = assistant.FormatOffset(shot.Offset)
			}
			fmt.Fprintf(&b, "- [%s] %s\n", when, filepath.Join(dir, shot.File))
		}
	}
	return b.String()
}

// exportFilename sugere "08-10-2026 - Revisão de Sprint - Transcrição.md": as
// barras da data e os outros caracteres que o Windows não aceita em nomes de
// arquivo viram "-".
func exportFilename(sessionName, title string) string {
	name := strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, sessionName))
	if name == "" {
		return title + ".md"
	}
	return name + " - " + title + ".md"
}
