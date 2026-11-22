package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"recorder/internal/chat"
	"recorder/internal/storage"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeJSONOllama responde toda chamada a /api/chat com answer.
func fakeJSONOllama(answer string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(chat.ChatResponse{Message: chat.ChatMessage{Role: "assistant", Content: answer}, Done: true})
	}))
}

func TestSuggestedSessionNameUsesDateAndTitle(t *testing.T) {
	store, err := storage.NewStorage(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess, _ := store.CreateSession("", "Nova sessão")
	a := &App{store: store}

	// Sem áudio, vale a data em que a sessão foi criada
	want := sess.CreatedAt.Format("02/01/2006") + " - Revisão de Sprint"
	if got := a.suggestedSessionName(sess.ID, " \"Revisão de Sprint.\"\n"); got != want {
		t.Fatalf("esperava %q, veio %q", want, got)
	}
	if got := a.suggestedSessionName(sess.ID, strings.Repeat("palavra ", 20)); got != "" {
		t.Fatalf("título longo demais deveria ser ignorado, veio %q", got)
	}
}

func TestUpdateTasksStoresStructuredTasks(t *testing.T) {
	srv := fakeJSONOllama(`{"title": "Alinhamento da Sprint", "tasks": [
		{"task": "Finalizar a cópia do banco", "owner": "Pessoa 10", "deadline": "amanhã", "time": "12:34"},
		{"task": "  ", "owner": "", "deadline": "", "time": ""},
		{"task": "Atualizar o ClickUp", "owner": "Alexander", "deadline": "", "time": "sem momento"}]}`)
	defer srv.Close()
	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess, _ := store.CreateSession("", "reunião")
	a := NewApp(store, chat.NewOllamaClient(srv.URL, "modelo"))

	if title := a.updateTasks(sess.ID, "resumo"); title != "Alinhamento da Sprint" {
		t.Fatalf("título %q", title)
	}
	tasks, _ := store.GetTasks(sess.ID)
	if len(tasks) != 2 || tasks[0].Offset != 754 || tasks[0].Owner != "Pessoa 10" || tasks[1].Offset != -1 {
		t.Fatalf("tarefas inesperadas: %+v", tasks)
	}

	// Refazer o resumo mantém marcada a tarefa que continua igual
	store.SetTaskDone(tasks[0].ID, true)
	a.updateTasks(sess.ID, "resumo")
	tasks, _ = store.GetTasks(sess.ID)
	if !tasks[0].Done || tasks[1].Done {
		t.Fatalf("a marcação de concluída deveria continuar: %+v", tasks)
	}
}

// scriptedLLM é uma IA da nuvem (cabe a reunião inteira) que responde names a
// toda pergunta em formato fixo e summary ao pedido de resumo, guardando o
// que recebeu nele.
type scriptedLLM struct {
	mu             sync.Mutex
	names, summary string
	summaryContent string
}

func (l *scriptedLLM) Answer(string, string, []storage.ChatMessage, func(string)) (string, error) {
	return "", nil
}
func (l *scriptedLLM) AskRaw(string, string) (string, error) { return "", nil }
func (l *scriptedLLM) ContextChars() int                     { return 250000 }
func (l *scriptedLLM) StreamRaw(system, content string, onText func(string)) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.summaryContent = content
	return l.summary, nil
}
func (l *scriptedLLM) AskJSON(system, content string, schema any, out any) error {
	answer := l.names
	if strings.Contains(system, "resumo de uma reunião") {
		answer = `{"title": "Documentação do app", "tasks": [{"task": "Documentar: pôr tudo no documento", "owner": "Antoni Kowalski", "deadline": "amanhã", "time": "00:40"}]}`
	}
	return json.Unmarshal([]byte(answer), out)
}

// No fim da reunião, o nome com pistas bastantes na conversa já é dado à voz
// antes do resumo, fica guardado para as próximas reuniões e pode ser desfeito.
func TestSummaryNamesSpeakersFromTheConversation(t *testing.T) {
	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess, _ := store.CreateSession("", "reunião")
	ws := sess.WorkspaceID
	store.SetParticipants(ws, "Antoni Kowalski\nOleksandr Petrenko")
	store.SaveSpeakerVoices(sess.ID, []storage.SpeakerVoice{
		{Label: "Pessoa 1", Embedding: []float32{1, 0, 0}, Seconds: 60},
		{Label: "Pessoa 2", Embedding: []float32{0, 1, 0}, Seconds: 80},
		{Label: "Pessoa 3", Embedding: []float32{0, 0, 1}, Seconds: 40},
	})
	for _, l := range [][3]string{
		{"5", "Pessoa 1", "My proposition is to start with it."},
		{"10", "Pessoa 2", "As Antoni mentioned before, we can keep the same configuration."},
		{"20", "Pessoa 3", "Like Alexander asked, which data are we storing?"},
		{"30", "Pessoa 3", "Antony, just put these things on the document."},
		{"35", "Pessoa 1", "Yes, sure."},
		{"40", "Pessoa 2", "I want Anthony to explain the two options again."},
	} {
		offset, _ := strconv.ParseFloat(l[0], 64)
		store.InsertTranscript(sess.ID, "Audio Geral", l[1], l[2], offset)
	}
	llm := &scriptedLLM{summary: "# Ata\n\nAntoni Kowalski vai documentar [00:40].", names: `{"clues": [
		{"time": "00:10", "speaker": "Pessoa 2", "name": "Antoni", "kind": "citou", "about": "Pessoa 1", "note": ""},
		{"time": "00:30", "speaker": "Pessoa 3", "name": "Antony", "kind": "chamou", "about": "Pessoa 1", "note": ""},
		{"time": "00:40", "speaker": "Pessoa 2", "name": "Anthony", "kind": "citou", "about": "Pessoa 1", "note": ""},
		{"time": "00:20", "speaker": "Pessoa 3", "name": "Alexander", "kind": "citou", "about": "Pessoa 2", "note": ""}],
	"people": [
		{"speaker": "Pessoa 1", "name": "Anthony", "confidence": "alta", "reason": ""},
		{"speaker": "Pessoa 2", "name": "Alexander", "confidence": "media", "reason": ""}]}`}
	a := NewApp(store, chat.NewOllamaClient("http://127.0.0.1:1", "modelo"))
	a.llm.Set(llm)
	var events []string
	a.events = func(event string, data ...interface{}) { events = append(events, event) }

	a.generateSummary(sess.ID)

	// A Pessoa 1 já é o Antoni (três falas de outras pessoas); o Oleksandr, com
	// uma fala só, espera a confirmação
	if got, want := sessionSpeakers(t, store, sess.ID), []string{"Antoni Kowalski", "Pessoa 2", "Pessoa 3", "Pessoa 3", "Antoni Kowalski", "Pessoa 2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("falas: esperava %v, veio %v", want, got)
	}
	suggestions, _ := store.GetSpeakerSuggestions(sess.ID)
	if len(suggestions) != 2 || suggestions[0].Speaker != "Antoni Kowalski" || suggestions[0].Name != "Antoni Kowalski" ||
		suggestions[1].Speaker != "Pessoa 2" || suggestions[1].Name != "Oleksandr Petrenko" {
		t.Fatalf("sugestões: %+v", suggestions)
	}
	if people, _ := store.GetPeopleVoices(ws); len(people) != 1 || people[0].Label != "Antoni Kowalski" {
		t.Fatalf("a voz do Antoni deveria ficar guardada: %+v", people)
	}
	// O resumo foi pedido com os nomes já nas falas e com os participantes
	if !strings.Contains(llm.summaryContent, "[00:05] Antoni Kowalski: My proposition") || !strings.Contains(llm.summaryContent, "Oleksandr Petrenko\n") {
		t.Fatalf("o resumo deveria receber os nomes e os participantes, recebeu:\n%s", llm.summaryContent)
	}
	if tasks, _ := store.GetTasks(sess.ID); len(tasks) != 1 || tasks[0].Owner != "Antoni Kowalski" || tasks[0].Offset != 40 {
		t.Fatalf("tarefas: %+v", tasks)
	}
	if !slices.Contains(events, "speaker-suggestions") || !slices.Contains(events, "speakers-changed") {
		t.Fatalf("a tela deveria ser avisada dos nomes: %v", events)
	}

	// "Está certo" num nome que o app deu só tira o aviso
	if err := a.ConfirmSpeakerSuggestion(sess.ID, "Antoni Kowalski", "Antoni Kowalski"); err != nil {
		t.Fatal(err)
	}
	if suggestions, _ := store.GetSpeakerSuggestions(sess.ID); len(suggestions) != 1 {
		t.Fatalf("depois de confirmar: %+v", suggestions)
	}
	// Confirmar a sugestão dá o nome e guarda a voz
	if err := a.ConfirmSpeakerSuggestion(sess.ID, "Pessoa 2", "Oleksandr Petrenko"); err != nil {
		t.Fatal(err)
	}
	if names := knownNames(t, store, ws); !reflect.DeepEqual(names, []string{"Antoni Kowalski", "Oleksandr Petrenko"}) {
		t.Fatalf("vozes conhecidas: %v", names)
	}

	// "Não é": a voz volta a ser uma Pessoa N (o próximo rótulo livre), a
	// tarefa também, e a voz guardada some (era a única reunião com ela)
	store.ReplaceSpeakerSuggestions(sess.ID, []storage.SpeakerSuggestion{{Speaker: "Antoni Kowalski", Name: "Antoni Kowalski", Evidence: "..."}})
	if err := a.DismissSpeakerSuggestion(sess.ID, "Antoni Kowalski"); err != nil {
		t.Fatal(err)
	}
	if got := sessionSpeakers(t, store, sess.ID); got[0] != "Pessoa 4" || got[4] != "Pessoa 4" {
		t.Fatalf("depois de desfazer, as falas deveriam voltar a ser de uma Pessoa N: %v", got)
	}
	if names := knownNames(t, store, ws); !reflect.DeepEqual(names, []string{"Oleksandr Petrenko"}) {
		t.Fatalf("vozes conhecidas depois de desfazer: %v", names)
	}
	if tasks, _ := store.GetTasks(sess.ID); tasks[0].Owner != "Pessoa 4" {
		t.Fatalf("a tarefa deveria voltar para a Pessoa N: %+v", tasks)
	}
	if suggestions, _ := store.GetSpeakerSuggestions(sess.ID); len(suggestions) != 0 {
		t.Fatalf("depois de desfazer: %+v", suggestions)
	}
}

func sessionSpeakers(t *testing.T, store *storage.Storage, sessionID string) []string {
	t.Helper()
	transcripts, err := store.GetAllTranscripts(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var who []string
	for _, tr := range transcripts {
		who = append(who, tr.Speaker)
	}
	return who
}

func knownNames(t *testing.T, store *storage.Storage, workspaceID string) []string {
	t.Helper()
	people, err := store.GetPeopleVoices(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range people {
		names = append(names, p.Label)
	}
	return names
}
