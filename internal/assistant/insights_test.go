package assistant

import "testing"

func TestExtractInsightsParsesTasks(t *testing.T) {
	srv := fakeJSONOllama(`{"title": "Alinhamento da Sprint", "tasks": [
		{"task": "Finalizar a cópia do banco", "owner": "Pessoa 10", "deadline": "amanhã", "time": "12:34"},
		{"task": "  ", "owner": "", "deadline": "", "time": ""},
		{"task": "Atualizar o ClickUp", "owner": "Alexander", "deadline": "", "time": "sem momento"}]}`)
	defer srv.Close()
	a, _, _ := testAssistant(t, srv.URL)

	title, tasks, err := a.ExtractInsights("resumo")
	if err != nil {
		t.Fatal(err)
	}
	if title != "Alinhamento da Sprint" {
		t.Fatalf("título %q", title)
	}
	if len(tasks) != 2 || tasks[0].Offset != 754 || tasks[0].Owner != "Pessoa 10" || tasks[1].Offset != -1 {
		t.Fatalf("tarefas inesperadas: %+v", tasks)
	}
}
