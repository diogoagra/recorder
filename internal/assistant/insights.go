package assistant

import (
	"recorder/internal/storage"
	"strings"
)

const insightsPrompt = `A partir do resumo de uma reunião, extraia:
- title: um título curto e direto para a reunião (no máximo 5 palavras), em português do Brasil, sem data.
- tasks: as tarefas da seção de tarefas (Action Items) do resumo, uma para cada linha, na mesma ordem. Para cada uma: task (a tarefa como está no resumo, no formato "Título: o que fazer", sem negrito e sem o momento entre colchetes), owner (quem ficou responsável, como aparece no resumo; vários nomes separados por vírgula; vazio se ninguém), deadline (o prazo, como aparece no resumo; vazio se não houver) e time (o momento do áudio citado no resumo para essa tarefa, no formato mm:ss ou h:mm:ss; vazio se não houver).
Use apenas as tarefas que estão nessa seção: não transforme em tarefa o que está na ata, nas decisões ou no e-mail, e não invente tarefas. Se o resumo não tiver tarefas, devolva a lista vazia.`

var insightsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"title": map[string]any{"type": "string"},
		"tasks": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task":     map[string]any{"type": "string"},
					"owner":    map[string]any{"type": "string"},
					"deadline": map[string]any{"type": "string"},
					"time":     map[string]any{"type": "string"},
				},
				"required": []string{"task", "owner", "deadline", "time"},
			},
		},
	},
	"required": []string{"title", "tasks"},
}

// ExtractInsights tira do resumo o título da reunião e as tarefas combinadas,
// no formato fixo pedido ao Ollama.
func (a *Assistant) ExtractInsights(summary string) (string, []storage.Task, error) {
	var out struct {
		Title string `json:"title"`
		Tasks []struct {
			Task     string `json:"task"`
			Owner    string `json:"owner"`
			Deadline string `json:"deadline"`
			Time     string `json:"time"`
		} `json:"tasks"`
	}
	if err := a.LLM.AskJSON(insightsPrompt, "--- RESUMO ---\n"+summary, insightsSchema, &out); err != nil {
		return "", nil, err
	}
	tasks := []storage.Task{}
	for _, t := range out.Tasks {
		if text := strings.TrimSpace(t.Task); text != "" {
			tasks = append(tasks, storage.Task{
				Text:     text,
				Owner:    strings.TrimSpace(t.Owner),
				Deadline: strings.TrimSpace(t.Deadline),
				Offset:   parseOffset(t.Time),
			})
		}
	}
	return out.Title, tasks, nil
}
