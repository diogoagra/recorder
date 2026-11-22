package speakers

import (
	"path/filepath"
	"recorder/internal/diarization"
	"recorder/internal/storage"
	"reflect"
	"testing"
)

func testStore(t *testing.T) *storage.Storage {
	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func speakersOf(t *testing.T, store *storage.Storage, sessionID string) []string {
	ts, err := store.GetAllTranscripts(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tr := range ts {
		got = append(got, tr.Speaker)
	}
	return got
}

func peopleNames(t *testing.T, store *storage.Storage, workspaceID string) []string {
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

func TestRenameSpeakerRemembersPersonAndMerges(t *testing.T) {
	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess, err := store.CreateSession("", "reunião")
	if err != nil {
		t.Fatal(err)
	}
	store.SaveSpeakerVoices(sess.ID, []storage.SpeakerVoice{
		{Label: "Pessoa 1", Embedding: []float32{1, 0, 0}, Seconds: 30},
		{Label: "Pessoa 2", Embedding: []float32{0, 1, 0}, Seconds: 20},
		{Label: "Pessoa 3", Embedding: []float32{0, 0.9, 0.1}, Seconds: 10},
	})
	for _, who := range []string{"Pessoa 1", "Pessoa 2", "Pessoa 3", "Pessoa 2"} {
		store.InsertTranscript(sess.ID, "Audio Geral", who, "fala de "+who, 0)
	}

	if err := Rename(store, sess.ID, "Pessoa 2", "Ana"); err != nil {
		t.Fatal(err)
	}
	// A Pessoa 3 também era a Ana (a identificação separou a mesma voz)
	if err := Rename(store, sess.ID, "Pessoa 3", "Ana"); err != nil {
		t.Fatal(err)
	}

	ts, _ := store.GetAllTranscripts(sess.ID)
	var got []string
	for _, tr := range ts {
		got = append(got, tr.Speaker)
	}
	if want := []string{"Pessoa 1", "Ana", "Ana", "Ana"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("falas: esperava %v, veio %v", want, got)
	}
	voices, _ := store.GetSpeakerVoices(sess.ID)
	if len(voices) != 2 {
		t.Fatalf("esperava 2 vozes na sessão depois de juntar, veio %d", len(voices))
	}
	people, _ := store.GetPeopleVoices(sess.WorkspaceID)
	if len(people) != 1 || people[0].Label != "Ana" {
		t.Fatalf("esperava a Ana guardada entre as pessoas conhecidas, veio %+v", people)
	}

	// Numa sessão nova, a voz da Ana é reconhecida pelo nome
	other, _ := store.CreateSession("", "outra reunião")
	known := Load(store, other.ID)
	if got := known.MatchBlock([]diarization.Voice{{Embedding: []float32{0, 1, 0.05}, Seconds: 12}}); got[0] != "Ana" {
		t.Fatalf("esperava reconhecer a Ana em outra sessão, veio %q", got[0])
	}

	// Renomear para outro rótulo automático só junta as vozes, sem guardar pessoa
	if err := Rename(store, sess.ID, "Pessoa 1", "Pessoa 9"); err != nil {
		t.Fatal(err)
	}
	if people, _ := store.GetPeopleVoices(sess.WorkspaceID); len(people) != 1 {
		t.Fatalf("rótulo automático não deveria virar pessoa conhecida: %+v", people)
	}
}

func TestSaveRelabelsMergedSpeaker(t *testing.T) {
	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess, _ := store.CreateSession("", "reunião")
	store.SaveSpeakerVoices(sess.ID, []storage.SpeakerVoice{
		{Label: "Pessoa 1", Embedding: []float32{1, 0, 0}, Seconds: 30},
		{Label: "Pessoa 2", Embedding: []float32{0, 1, 0}, Seconds: 20},
		{Label: "Pessoa 3", Embedding: []float32{0, 0.5, 0.866}, Seconds: 10},
	})
	for _, who := range []string{"Pessoa 1", "Pessoa 2", "Pessoa 3"} {
		store.InsertTranscript(sess.ID, "Audio Geral", who, "fala de "+who, 0)
	}
	var notified string
	OnMerge = func(sessionID string) { notified = sessionID }
	defer func() { OnMerge = nil }()

	// Com mais áudio, fica claro que a Pessoa 3 é a Pessoa 2
	known := Load(store, sess.ID)
	known.MatchBlock([]diarization.Voice{
		{Embedding: []float32{0, 1, 0}, Seconds: 10},
		{Embedding: []float32{0, 0.8, 0.6}, Seconds: 30},
	})
	Save(store, sess.ID, known)

	ts, _ := store.GetAllTranscripts(sess.ID)
	var got []string
	for _, tr := range ts {
		got = append(got, tr.Speaker)
	}
	if want := []string{"Pessoa 1", "Pessoa 2", "Pessoa 2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("falas: esperava %v, veio %v", want, got)
	}
	if voices, _ := store.GetSpeakerVoices(sess.ID); len(voices) != 2 {
		t.Fatalf("esperava 2 vozes na sessão, veio %d", len(voices))
	}
	if notified != sess.ID {
		t.Fatalf("esperava o aviso da junção para a sessão %s, veio %q", sess.ID, notified)
	}
}

func TestPeopleAreKnownOnlyInTheirWorkspace(t *testing.T) {
	store := testStore(t)
	sess, _ := store.CreateSession("", "reunião no emprego A")
	store.SaveSpeakerVoices(sess.ID, []storage.SpeakerVoice{{Label: "Pessoa 1", Embedding: []float32{0, 1, 0}, Seconds: 30}})
	if err := Rename(store, sess.ID, "Pessoa 1", "Ana"); err != nil {
		t.Fatal(err)
	}

	other, _ := store.CreateWorkspace("Emprego B")
	there, _ := store.CreateSession(other.ID, "reunião no emprego B")
	known := Load(store, there.ID)
	if got := known.MatchBlock([]diarization.Voice{{Embedding: []float32{0, 1, 0.05}, Seconds: 12}}); got[0] == "Ana" {
		t.Fatal("a Ana é conhecida só no workspace em que foi nomeada")
	}
	if names := peopleNames(t, store, other.ID); len(names) != 0 {
		t.Fatalf("o outro workspace não deveria conhecer ninguém, conhece %v", names)
	}
}

func TestRenamePersonFixesNameInWorkspaceMeetings(t *testing.T) {
	store := testStore(t)
	first, _ := store.CreateSession("", "reunião 1")
	ws := first.WorkspaceID
	second, _ := store.CreateSession(ws, "reunião 2")
	store.SaveSpeakerVoices(first.ID, []storage.SpeakerVoice{
		{Label: "Diego", Embedding: []float32{1, 0, 0}, Seconds: 30},
		{Label: "Pessoa 2", Embedding: []float32{0, 1, 0}, Seconds: 20},
	})
	store.InsertTranscript(first.ID, "Audio Geral", "Diego", "bom dia", 0)
	store.InsertTranscript(first.ID, "Audio Geral", "Pessoa 2", "oi", 5)
	store.ReplaceTasks(first.ID, []storage.Task{{Text: "subir o deploy", Owner: "Diego", Offset: -1}})
	store.InsertTranscript(second.ID, "Audio Geral", "Diego", "vamos começar", 0)
	store.SavePersonVoice(ws, storage.SpeakerVoice{Label: "Diego", Embedding: []float32{1, 0, 0}, Seconds: 30})

	// Em outro workspace há um Diego de verdade, que não muda
	otherWS, _ := store.CreateWorkspace("Emprego B")
	elsewhere, _ := store.CreateSession(otherWS.ID, "reunião no emprego B")
	store.InsertTranscript(elsewhere.ID, "Audio Geral", "Diego", "olá", 0)
	store.SavePersonVoice(otherWS.ID, storage.SpeakerVoice{Label: "Diego", Embedding: []float32{0, 0, 1}, Seconds: 30})

	changed, err := RenamePerson(store, ws, "Diego", "Diogo")
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 2 {
		t.Fatalf("esperava 2 reuniões alteradas, veio %v", changed)
	}
	if got, want := speakersOf(t, store, first.ID), []string{"Diogo", "Pessoa 2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("falas da reunião 1: esperava %v, veio %v", want, got)
	}
	if got := speakersOf(t, store, second.ID); !reflect.DeepEqual(got, []string{"Diogo"}) {
		t.Fatalf("falas da reunião 2: veio %v", got)
	}
	if tasks, _ := store.GetTasks(first.ID); tasks[0].Owner != "Diogo" {
		t.Fatalf("a tarefa deveria passar para o Diogo, está com %q", tasks[0].Owner)
	}
	if voices, _ := store.GetSpeakerVoices(first.ID); voices[0].Label != "Diogo" {
		t.Fatalf("a voz da reunião deveria passar a ser do Diogo: %+v", voices)
	}
	if names := peopleNames(t, store, ws); !reflect.DeepEqual(names, []string{"Diogo"}) {
		t.Fatalf("vozes conhecidas: %v", names)
	}
	if got := speakersOf(t, store, elsewhere.ID); !reflect.DeepEqual(got, []string{"Diego"}) {
		t.Fatalf("o Diego do outro workspace não deveria mudar, veio %v", got)
	}
	if names := peopleNames(t, store, otherWS.ID); !reflect.DeepEqual(names, []string{"Diego"}) {
		t.Fatalf("vozes conhecidas do outro workspace: %v", names)
	}

	// A voz renomeada é reconhecida pelo nome novo
	next, _ := store.CreateSession(ws, "reunião 3")
	if got := Load(store, next.ID).MatchBlock([]diarization.Voice{{Embedding: []float32{1, 0.05, 0}, Seconds: 12}}); got[0] != "Diogo" {
		t.Fatalf("esperava reconhecer o Diogo, veio %q", got[0])
	}

	if _, err := RenamePerson(store, ws, "Diogo", "Pessoa 4"); err == nil {
		t.Fatal("um rótulo automático não deveria ser aceito como nome")
	}
	if _, err := RenamePerson(store, ws, "Ninguém", "Alguém"); err == nil {
		t.Fatal("renomear uma voz que não existe deveria dar erro")
	}
}

func TestRenamePersonToExistingNameMergesVoices(t *testing.T) {
	store := testStore(t)
	sess, _ := store.CreateSession("", "reunião")
	ws := sess.WorkspaceID
	store.SaveSpeakerVoices(sess.ID, []storage.SpeakerVoice{
		{Label: "Diego", Embedding: []float32{1, 0, 0}, Seconds: 30},
		{Label: "Diogo", Embedding: []float32{0.9, 0.1, 0}, Seconds: 20},
	})
	store.InsertTranscript(sess.ID, "Audio Geral", "Diego", "um", 0)
	store.InsertTranscript(sess.ID, "Audio Geral", "Diogo", "dois", 5)
	store.SavePersonVoice(ws, storage.SpeakerVoice{Label: "Diego", Embedding: []float32{1, 0, 0}, Seconds: 30})
	store.SavePersonVoice(ws, storage.SpeakerVoice{Label: "Diogo", Embedding: []float32{0.9, 0.1, 0}, Seconds: 20})

	if _, err := RenamePerson(store, ws, "Diego", "Diogo"); err != nil {
		t.Fatal(err)
	}
	people, _ := store.GetPeopleVoices(ws)
	if len(people) != 1 || people[0].Label != "Diogo" || people[0].Seconds != 50 {
		t.Fatalf("esperava uma só voz do Diogo com os 50s das duas, veio %+v", people)
	}
	if voices, _ := store.GetSpeakerVoices(sess.ID); len(voices) != 1 {
		t.Fatalf("as duas vozes da reunião deveriam virar uma, veio %+v", voices)
	}
	if got := speakersOf(t, store, sess.ID); !reflect.DeepEqual(got, []string{"Diogo", "Diogo"}) {
		t.Fatalf("falas: %v", got)
	}
}

func TestMoveTeachesNamedVoicesToDestination(t *testing.T) {
	store := testStore(t)
	sess, _ := store.CreateSession("", "reunião do emprego B")
	store.SaveSpeakerVoices(sess.ID, []storage.SpeakerVoice{
		{Label: "Carlos", Embedding: []float32{1, 0, 0}, Seconds: 30},
		{Label: "Pessoa 2", Embedding: []float32{0, 1, 0}, Seconds: 20},
	})
	dest, _ := store.CreateWorkspace("Emprego B")

	if err := Move(store, sess.ID, dest.ID); err != nil {
		t.Fatal(err)
	}
	if moved, _ := store.GetSession(sess.ID); moved.WorkspaceID != dest.ID {
		t.Fatalf("a reunião deveria estar no workspace %s, está no %s", dest.ID, moved.WorkspaceID)
	}
	if names := peopleNames(t, store, dest.ID); !reflect.DeepEqual(names, []string{"Carlos"}) {
		t.Fatalf("o destino deveria conhecer só o Carlos (Pessoa 2 não tem nome), conhece %v", names)
	}
	if err := Move(store, sess.ID, "nao-existe"); err == nil {
		t.Fatal("mover para um workspace que não existe deveria dar erro")
	}
}

// O nome dado à voz errada numa reunião não estraga a voz conhecida: ao
// corrigir, a voz guardada volta a ser só a das outras reuniões.
func TestWrongNameCanBeUndone(t *testing.T) {
	store := testStore(t)
	first, _ := store.CreateSession("", "reunião 1")
	ws := first.WorkspaceID
	store.SaveSpeakerVoices(first.ID, []storage.SpeakerVoice{{Label: "Pessoa 1", Embedding: []float32{1, 0, 0}, Seconds: 60}})
	if err := Rename(store, first.ID, "Pessoa 1", "Diogo"); err != nil {
		t.Fatal(err)
	}

	// Na reunião 2, a voz de outra pessoa recebe o nome do Diogo por engano
	second, _ := store.CreateSession(ws, "reunião 2")
	store.SaveSpeakerVoices(second.ID, []storage.SpeakerVoice{
		{Label: "Pessoa 1", Embedding: []float32{0, 1, 0}, Seconds: 600},
		{Label: "Pessoa 2", Embedding: []float32{0, 0, 1}, Seconds: 40},
	})
	store.InsertTranscript(second.ID, "Audio Geral", "Pessoa 1", "olá", 0)
	if err := Rename(store, second.ID, "Pessoa 1", "Diogo"); err != nil {
		t.Fatal(err)
	}
	// Desfazer devolve um rótulo automático livre e tira a reunião da voz guardada
	label, err := Unname(store, second.ID, "Diogo")
	if err != nil || label != "Pessoa 3" {
		t.Fatalf("Unname: %q %v", label, err)
	}
	if got := speakersOf(t, store, second.ID); !reflect.DeepEqual(got, []string{"Pessoa 3"}) {
		t.Fatalf("falas: %v", got)
	}
	people, _ := store.GetPeopleVoices(ws)
	if len(people) != 1 || people[0].Embedding[0] < 0.999 || people[0].Seconds != 60 {
		t.Fatalf("a voz do Diogo deveria ser só a da reunião 1, veio %+v", people)
	}

	// Corrigir para o nome certo também tira a voz da pessoa errada
	if err := Rename(store, second.ID, "Pessoa 2", "Diogo"); err != nil {
		t.Fatal(err)
	}
	if err := Rename(store, second.ID, "Diogo", "Ana"); err != nil {
		t.Fatal(err)
	}
	if names := peopleNames(t, store, ws); !reflect.DeepEqual(names, []string{"Ana", "Diogo"}) {
		t.Fatalf("vozes conhecidas: %v", names)
	}
	people, _ = store.GetPeopleVoices(ws)
	if people[1].Embedding[0] < 0.999 {
		t.Fatalf("a voz do Diogo voltou a ter a voz da Ana: %+v", people[1])
	}

	// Era a única reunião com a voz da Ana: sem ela, a Ana deixa de ser conhecida
	if _, err := Unname(store, second.ID, "Ana"); err != nil {
		t.Fatal(err)
	}
	if names := peopleNames(t, store, ws); !reflect.DeepEqual(names, []string{"Diogo"}) {
		t.Fatalf("vozes conhecidas: %v", names)
	}
}

// Quem teve a voz esquecida volta a ser reconhecido com o que as reuniões
// anteriores já sabiam dele, menos a reunião em que o nome estava em outra voz.
func TestRememberBringsBackEarlierMeetings(t *testing.T) {
	store := testStore(t)
	a, _ := store.CreateSession("", "reunião A")
	ws := a.WorkspaceID
	b, _ := store.CreateSession(ws, "reunião B")
	c, _ := store.CreateSession(ws, "reunião C")
	store.SaveSpeakerVoices(a.ID, []storage.SpeakerVoice{
		{Label: "Caio", Embedding: []float32{0.9, 0.436, 0}, Seconds: 100},
		{Label: "Pessoa 2", Embedding: []float32{1, 0, 0}, Seconds: 70}, // a mesma voz, mas sem nome: não conta
	})
	store.SaveSpeakerVoices(b.ID, []storage.SpeakerVoice{{Label: "Caio", Embedding: []float32{0, 0, 1}, Seconds: 900}}) // outra pessoa
	store.SaveSpeakerVoices(c.ID, []storage.SpeakerVoice{{Label: "Pessoa 1", Embedding: []float32{1, 0, 0}, Seconds: 50}})

	// O nome agora vem com sobrenome: a reunião A, em que a voz era "Caio", vale
	if err := Rename(store, c.ID, "Pessoa 1", "Caio Ramos"); err != nil {
		t.Fatal(err)
	}
	people, _ := store.GetPeopleVoices(ws)
	if len(people) != 1 || people[0].Label != "Caio Ramos" || people[0].Seconds != 150 || people[0].Embedding[2] > 0.01 {
		t.Fatalf("esperava a voz do Caio Ramos com as reuniões A e C (150s), veio %+v", people)
	}

	if err := Forget(store, ws, "Caio Ramos"); err != nil {
		t.Fatal(err)
	}
	if names := peopleNames(t, store, ws); len(names) != 0 {
		t.Fatalf("depois de esquecer: %v", names)
	}
	if samples, _ := store.GetPersonSamples(ws, "Caio Ramos"); len(samples) != 0 {
		t.Fatalf("as vozes por reunião deveriam ser esquecidas também: %+v", samples)
	}
}
