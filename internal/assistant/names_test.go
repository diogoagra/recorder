package assistant

import (
	"encoding/json"
	"errors"
	"recorder/internal/roster"
	"recorder/internal/storage"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestSuggestNamesFromCallsAndIntroductions(t *testing.T) {
	srv := fakeNamesOllama(`{"mentions": [
		{"time": "00:10", "speaker": "Pessoa 1", "name": "Ana", "kind": "chamou"},
		{"time": "00:30", "speaker": "Pessoa 2", "name": "Bruno", "kind": "apresentou"},
		{"time": "00:40", "speaker": "Pessoa 3", "name": "Carlos", "kind": "chamou"},
		{"time": "00:50", "speaker": "Pessoa 1", "name": "Zé", "kind": "chamou"},
		{"time": "00:15", "speaker": "Pessoa 3", "name": "Diego", "kind": "apresentou"}]}`,
		map[string]string{"Ana": "Pessoa 3"})
	defer srv.Close()
	a, store, sess := testAssistant(t, srv.URL)

	transcripts := insertLines(store, sess.ID, [][3]string{
		{"00:10", "Pessoa 1", "Ana, pode contar o que você fez?"},
		{"00:12", "Pessoa 4", "Pode contar como foi a semana?"}, // quem chamou, separado em outra voz
		{"00:14", "Pessoa 5", "Ok."},
		{"00:15", "Pessoa 3", "Claro, terminei a migração ontem."},
		{"00:30", "Pessoa 2", "Aqui é o Bruno, do financeiro."},
		{"00:40", "Pessoa 3", "O Carlos me ajudou."},
	})
	got := a.SuggestNames(transcripts)

	// Ana: quem respondeu foi a Pessoa 3. Bruno: se apresentou. Carlos só foi
	// mencionado (ninguém respondeu); Zé não está em nenhuma fala; Diego não
	// está na fala apontada.
	want := []storage.SpeakerSuggestion{
		{Speaker: "Pessoa 2", Name: "Bruno", Evidence: "[00:30] Pessoa 2: Aqui é o Bruno, do financeiro."},
		{Speaker: "Pessoa 3", Name: "Ana", Evidence: "[00:10] Pessoa 1: Ana, pode contar o que você fez? → [00:15] Pessoa 3: Claro, terminei a migração ontem."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("esperava %+v, veio %+v", want, got)
	}
}

func TestSuggestNamesSkipsConflicts(t *testing.T) {
	// A mesma voz responde a dois nomes diferentes: não dá para saber quem é
	srv := fakeNamesOllama(`{"mentions": [
		{"time": "00:10", "speaker": "Pessoa 1", "name": "Ana", "kind": "chamou"},
		{"time": "01:00", "speaker": "Pessoa 1", "name": "Eva", "kind": "chamou"}]}`,
		map[string]string{"Ana": "Pessoa 3", "Eva": "Pessoa 3"})
	defer srv.Close()
	a, store, sess := testAssistant(t, srv.URL)

	transcripts := insertLines(store, sess.ID, [][3]string{
		{"00:10", "Pessoa 1", "Ana, pode contar?"},
		{"00:15", "Pessoa 3", "Claro, terminei a migração."},
		{"01:00", "Pessoa 1", "Eva, e você?"},
		{"01:05", "Pessoa 3", "Tudo certo por aqui, obrigado."},
	})
	if got := a.SuggestNames(transcripts); len(got) != 0 {
		t.Fatalf("não deveria sugerir nada, veio %+v", got)
	}
}

func TestLooksLikeAnswer(t *testing.T) {
	for text, want := range map[string]bool{
		"Okay, so during this week I set up the new account.":       true,
		"Can you tell us a little bit about how you're doing? Yes.": false,
		"True.":                         false,
		"Sim, sim.":                     false,
		"Terminei a migração ontem":     true,
		"Eu? Bom, terminei a migração.": false,
	} {
		if got := looksLikeAnswer(text); got != want {
			t.Errorf("looksLikeAnswer(%q) = %v", text, got)
		}
	}
}

// fakeLLM é uma IA da nuvem (cabe a reunião inteira) que responde answer a
// todo AskJSON e guarda o que recebeu.
type fakeLLM struct {
	mu      sync.Mutex // a pergunta dos nomes é feita mais de uma vez ao mesmo tempo
	answer  string
	answers []string // se houver, uma resposta para cada pergunta, em ordem
	err     error
	content string
	asked   int
}

func (f *fakeLLM) Answer(string, string, []storage.ChatMessage, func(string)) (string, error) {
	return "", nil
}
func (f *fakeLLM) AskRaw(string, string) (string, error)                  { return "", nil }
func (f *fakeLLM) StreamRaw(string, string, func(string)) (string, error) { return "", nil }
func (f *fakeLLM) ContextChars() int                                      { return 250000 }
func (f *fakeLLM) AskJSON(system, content string, schema any, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.content = content
	f.asked++
	if f.err != nil {
		return f.err
	}
	answer := f.answer
	if len(f.answers) > 0 {
		answer = f.answers[(f.asked-1)%len(f.answers)]
	}
	return json.Unmarshal([]byte(answer), out)
}

// A reunião de 19/11/2026, resumida: o Whisper escreve Antoni de três jeitos e
// a separação das vozes põe o "Go on, Alexander" na fala do próprio Alexander.
var meetingLines = [][3]string{
	{"00:28", "Pessoa 1", "My proposition is to start with it and move to the other provider later."},
	{"00:53", "Pessoa 2", "In my opinion it is not a big issue because, as Antoni mentioned before, we can do it with the same configuration."},
	{"03:44", "Pessoa 3", "We are going to know, like Alexander asked you, which kind of data we are storing."},
	{"08:15", "Pessoa 3", "Luis, Diogo, do you want to put something here, guys?"},
	{"08:25", "Pessoa 4", "No, it's okay. We are going to start using the new database."},
	{"08:40", "Pessoa 2", "Go on, Alexander, go on. I'm sorry. The only thing I want to put on discussion is the source."},
	{"09:05", "Pessoa 2", "I want to be sure that Anthony explains again the two options that he has."},
	{"09:10", "Pessoa 1", "So right now I have only one option, to communicate with you via HTTP."},
	{"10:46", "Pessoa 3", "Nice. Antony, just put these things on the document."},
	{"10:56", "Pessoa 1", "Yes, sure. I have already done this in the task in ClickUp."},
	{"14:41", "Pessoa 3", "So, Anthony, Dominic, tomorrow we can go with this document."},
}

const meetingClues = `{"clues": [
	{"time": "00:53", "speaker": "Pessoa 2", "name": "Antoni", "kind": "citou", "about": "Pessoa 1", "note": ""},
	{"time": "03:44", "speaker": "Pessoa 3", "name": "Alexander", "kind": "citou", "about": "Pessoa 2", "note": ""},
	{"time": "08:15", "speaker": "Pessoa 3", "name": "Luis", "kind": "chamou", "about": "", "note": ""},
	{"time": "08:15", "speaker": "Pessoa 3", "name": "Diogo", "kind": "chamou", "about": "", "note": ""},
	{"time": "08:40", "speaker": "Pessoa 2", "name": "Alexander", "kind": "chamou", "about": "Pessoa 2", "note": ""},
	{"time": "10:46", "speaker": "Pessoa 3", "name": "Antony", "kind": "chamou", "about": "Pessoa 1", "note": ""},
	{"time": "09:05", "speaker": "Pessoa 2", "name": "Anthony", "kind": "citou", "about": "Pessoa 1", "note": ""},
	{"time": "14:41", "speaker": "Pessoa 3", "name": "Anthony", "kind": "chamou", "about": "Pessoa 1", "note": ""},
	{"time": "14:41", "speaker": "Pessoa 3", "name": "Dominic", "kind": "chamou", "about": "", "note": ""},
	{"time": "09:99", "speaker": "Pessoa 3", "name": "Caio", "kind": "apresentou", "about": "Pessoa 3", "note": "fala que não existe"},
	{"time": "08:25", "speaker": "Pessoa 4", "name": "Diogo", "kind": "apresentou", "about": "Pessoa 4", "note": "o nome não está na fala"}],
"people": [
	{"speaker": "Pessoa 1", "name": "Anthony", "confidence": "alta", "reason": ""},
	{"speaker": "Pessoa 2", "name": "Alexander", "confidence": "media", "reason": ""},
	{"speaker": "Pessoa 3", "name": "Caio", "confidence": "baixa", "reason": "palpite"},
	{"speaker": "Pessoa 4", "name": "", "confidence": "baixa", "reason": ""}]}`

func TestIdentifySpeakersFromClues(t *testing.T) {
	llm := &fakeLLM{answer: meetingClues}
	a, store, sess := testAssistant(t, "http://127.0.0.1:1")
	a.LLM = llm
	transcripts := insertLines(store, sess.ID, meetingLines)
	people := roster.Parse("Luiz Prado\nOleksandr Petrenko (Alexander, Alex)\nCaio Ramos\nDiogo Agra\nAntoni Kowalski\nDominik Nowak")

	got := a.IdentifySpeakers(transcripts, people)
	// Antoni: três falas de outras pessoas, com três grafias, apontam só para a
	// Pessoa 1 (a que chama também o Dominic conta menos). Oleksandr: uma fala
	// de outra pessoa e uma da própria voz (o chamado que a separação pôs na
	// fala dele): fica como sugestão. O Caio (palpite) e a Pessoa 4 (dois nomes
	// chamados juntos) ficam sem nome.
	if len(got) != 2 {
		t.Fatalf("esperava 2 nomes, veio %+v", got)
	}
	if g := got[0]; g.Speaker != "Pessoa 1" || g.Name != "Antoni Kowalski" || !g.Sure ||
		!strings.Contains(g.Evidence, "[00:53] Pessoa 2:") || !strings.Contains(g.Evidence, "[09:05] Pessoa 2:") {
		t.Fatalf("Pessoa 1: %+v", g)
	}
	if g := got[1]; g.Speaker != "Pessoa 2" || g.Name != "Oleksandr Petrenko" || g.Sure {
		t.Fatalf("Pessoa 2: %+v", g)
	}
	// A IA recebe só a transcrição; a grafia da lista é aplicada depois
	if strings.Contains(llm.content, "Petrenko") || !strings.Contains(llm.content, "[00:28] Pessoa 1: My proposition") {
		t.Fatalf("a IA deveria receber a transcrição, sem os participantes; recebeu:\n%s", llm.content)
	}

	if llm.asked != nameAsks {
		t.Fatalf("esperava %d perguntas à IA, veio %d", nameAsks, llm.asked)
	}

	// Sem a lista de participantes, fica a grafia mais comum nas falas
	got = a.IdentifySpeakers(transcripts, nil)
	if len(got) != 2 || got[0].Name != "Anthony" || !got[0].Sure || got[1].Name != "Alexander" {
		t.Fatalf("sem participantes: %+v", got)
	}
}

func TestIdentifySpeakersRefusesConflictsAndRepeatedNames(t *testing.T) {
	llm := &fakeLLM{}
	a, store, sess := testAssistant(t, "http://127.0.0.1:1")
	a.LLM = llm
	transcripts := insertLines(store, sess.ID, [][3]string{
		{"00:10", "Caio", "Ana, pode contar o que você fez?"},
		{"00:15", "Pessoa 2", "Claro, terminei a migração ontem."},
		{"00:30", "Caio", "Obrigado, Ana. E você, Eva?"},
		{"00:35", "Pessoa 2", "Tudo certo por aqui."},
		{"00:50", "Pessoa 3", "Como a Ana falou, a migração terminou."},
		{"01:00", "Pessoa 4", "Caio, você pode repetir?"},
		{"01:10", "Pessoa 5", "Aqui é a Ana, cheguei agora."},
	})

	// A Pessoa 2 responde por Ana e por Eva: não dá para saber quem é. O nome
	// Caio já é de outra voz da sessão. A Pessoa 5 se apresentou como Ana.
	llm.answer = `{"clues": [
		{"time": "00:10", "speaker": "Caio", "name": "Ana", "kind": "chamou", "about": "Pessoa 2", "note": ""},
		{"time": "00:30", "speaker": "Caio", "name": "Ana", "kind": "citou", "about": "Pessoa 2", "note": ""},
		{"time": "00:30", "speaker": "Caio", "name": "Eva", "kind": "chamou", "about": "Pessoa 2", "note": ""},
		{"time": "01:00", "speaker": "Pessoa 4", "name": "Caio", "kind": "chamou", "about": "Pessoa 3", "note": ""},
		{"time": "01:10", "speaker": "Pessoa 5", "name": "Ana", "kind": "apresentou", "about": "Pessoa 5", "note": ""}],
	"people": [
		{"speaker": "Pessoa 2", "name": "Ana", "confidence": "alta", "reason": ""},
		{"speaker": "Pessoa 3", "name": "Caio", "confidence": "media", "reason": ""},
		{"speaker": "Pessoa 5", "name": "Ana", "confidence": "media", "reason": ""},
		{"speaker": "Pessoa 9", "name": "Zé", "confidence": "alta", "reason": "voz que não existe"}]}`
	got := a.IdentifySpeakers(transcripts, nil)
	if len(got) != 1 || got[0].Speaker != "Pessoa 5" || got[0].Name != "Ana" || got[0].Sure {
		t.Fatalf("esperava só a sugestão Pessoa 5 = Ana, veio %+v", got)
	}

	// Sem "Pessoa N" na sessão não há o que procurar
	if got := a.IdentifySpeakers(transcripts[:1], nil); got != nil {
		t.Fatalf("esperava nil, veio %+v", got)
	}
}

// O mesmo nome escrito de dois jeitos é um voto só: antes as grafias
// empatavam e a sugestão sumia.
func TestSuggestNamesCountsSpellingsAsOneName(t *testing.T) {
	srv := fakeNamesOllama(`{"mentions": [
		{"time": "00:10", "speaker": "Pessoa 1", "name": "Antoni", "kind": "chamou"},
		{"time": "01:00", "speaker": "Pessoa 1", "name": "Anthony", "kind": "chamou"}]}`,
		map[string]string{"Antoni": "Pessoa 3", "Anthony": "Pessoa 3"})
	defer srv.Close()
	a, store, sess := testAssistant(t, srv.URL)

	transcripts := insertLines(store, sess.ID, [][3]string{
		{"00:10", "Pessoa 1", "Antoni, pode contar?"},
		{"00:15", "Pessoa 3", "Claro, terminei a migração."},
		{"01:00", "Pessoa 1", "Anthony, e o documento?"},
		{"01:05", "Pessoa 3", "Coloco tudo lá amanhã cedo."},
	})
	got := a.IdentifySpeakers(transcripts, roster.Parse("Antoni Kowalski"))
	if len(got) != 1 || got[0].Speaker != "Pessoa 3" || got[0].Name != "Antoni Kowalski" || got[0].Sure {
		t.Fatalf("esperava a sugestão Pessoa 3 = Antoni Kowalski, veio %+v", got)
	}
}

// Se a IA da nuvem falhar, os nomes ainda são procurados pelo caminho em duas etapas.
func TestIdentifySpeakersFallsBackWhenTheCloudFails(t *testing.T) {
	llm := &fakeLLM{err: errors.New("fora do ar")}
	a, store, sess := testAssistant(t, "http://127.0.0.1:1")
	a.LLM = llm
	transcripts := insertLines(store, sess.ID, meetingLines)
	if got := a.IdentifySpeakers(transcripts, nil); got == nil || len(got) != 0 {
		t.Fatalf("esperava uma lista vazia (nada descoberto), veio %#v", got)
	}
	// As duas perguntas por pistas e a busca de chamadas do outro caminho
	if llm.asked != nameAsks+1 {
		t.Fatalf("esperava %d perguntas, veio %d", nameAsks+1, llm.asked)
	}
}

// De vez em quando uma resposta da IA sai confusa (aqui, com as duas pessoas
// trocadas e um nome que ninguém mais viu): vale o que a maioria conclui, com
// as pistas que mais de uma resposta viu.
func TestIdentifySpeakersGoesWithTheMajority(t *testing.T) {
	confused := `{"clues": [
		{"time": "00:53", "speaker": "Pessoa 2", "name": "Antoni", "kind": "apresentou", "about": "Pessoa 2", "note": ""},
		{"time": "10:46", "speaker": "Pessoa 3", "name": "Antony", "kind": "chamou", "about": "Pessoa 2", "note": ""},
		{"time": "03:44", "speaker": "Pessoa 3", "name": "Alexander", "kind": "citou", "about": "Pessoa 1", "note": ""},
		{"time": "14:41", "speaker": "Pessoa 3", "name": "Dominic", "kind": "chamou", "about": "Pessoa 4", "note": ""}],
	"people": [
		{"speaker": "Pessoa 1", "name": "Alexander", "confidence": "alta", "reason": ""},
		{"speaker": "Pessoa 2", "name": "Antoni", "confidence": "alta", "reason": ""},
		{"speaker": "Pessoa 4", "name": "Dominic", "confidence": "media", "reason": ""}]}`
	llm := &fakeLLM{answers: []string{meetingClues, confused, meetingClues}}
	a, store, sess := testAssistant(t, "http://127.0.0.1:1")
	a.LLM = llm
	transcripts := insertLines(store, sess.ID, meetingLines)
	people := roster.Parse("Oleksandr Petrenko\nAntoni Kowalski\nDominik Nowak")

	got := a.IdentifySpeakers(transcripts, people)
	if len(got) != 2 || got[0].Speaker != "Pessoa 1" || got[0].Name != "Antoni Kowalski" || !got[0].Sure ||
		got[1].Speaker != "Pessoa 2" || got[1].Name != "Oleksandr Petrenko" || got[1].Sure {
		t.Fatalf("esperava Antoni (certo) e Oleksandr (sugestão), veio %+v", got)
	}

	// Sem maioria (cada resposta diz uma coisa), ninguém ganha nome
	llm.answers = []string{meetingClues, confused, `{"clues": [], "people": []}`}
	llm.asked = 0
	if got := a.IdentifySpeakers(transcripts, people); len(got) != 0 {
		t.Fatalf("sem maioria não deveria haver nomes, veio %+v", got)
	}
}
