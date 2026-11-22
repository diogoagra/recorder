// Package speakers guarda as vozes de cada sessão ("Pessoa 1", "Pessoa 2"...)
// e os nomes que o usuário dá a elas, que ficam guardados para reconhecer as
// mesmas pessoas nas próximas sessões.
package speakers

import (
	"fmt"
	"log"
	"recorder/internal/diarization"
	"recorder/internal/roster"
	"recorder/internal/storage"
	"strings"
	"sync"
)

// sessionLocks serializa, por sessão, a transcrição de um bloco (que lê e grava
// as vozes e insere as falas) e a troca de nomes feita pelo usuário. Sem isso,
// um bloco em andamento podia gravar de volta o rótulo antigo.
var sessionLocks sync.Map

// Lock trava a sessão até unlock ser chamado.
func Lock(sessionID string) (unlock func()) {
	m, _ := sessionLocks.LoadOrStore(sessionID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Load carrega as vozes da sessão e as pessoas com voz conhecida no workspace dela.
func Load(store *storage.Storage, sessionID string) *diarization.Registry {
	voices, err := store.GetSpeakerVoices(sessionID)
	if err != nil {
		log.Printf("Erro ao carregar as vozes da sessão %s: %v", sessionID, err)
	}
	people, err := store.GetPeopleVoices(store.SessionWorkspace(sessionID))
	if err != nil {
		log.Printf("Erro ao carregar as vozes conhecidas: %v", err)
	}
	return &diarization.Registry{Speakers: toSpeakers(voices), People: toSpeakers(people)}
}

// OnMerge, se definido, é chamado quando a identificação junta vozes que eram
// da mesma pessoa e troca o rótulo de falas já salvas da sessão.
var OnMerge func(sessionID string)

// Save grava as vozes da sessão. Se a identificação juntou vozes que eram da
// mesma pessoa (known.Merged), troca o rótulo também nas falas já salvas.
func Save(store *storage.Storage, sessionID string, known *diarization.Registry) {
	if len(known.Merged) == 0 {
		if err := store.SaveSpeakerVoices(sessionID, toVoices(known.Speakers)); err != nil {
			log.Printf("Erro ao salvar as vozes da sessão %s: %v", sessionID, err)
		}
		return
	}

	if err := store.ReplaceSpeakerVoices(sessionID, toVoices(known.Speakers)); err != nil {
		log.Printf("Erro ao salvar as vozes da sessão %s: %v", sessionID, err)
		return
	}
	for _, m := range known.Merged {
		log.Printf("%s e %s são a mesma pessoa: juntando as falas", m.From, m.To)
		if err := store.RenameTranscriptSpeaker(sessionID, m.From, m.To); err != nil {
			log.Printf("Erro ao juntar as falas de %s e %s: %v", m.From, m.To, err)
		}
		store.DeleteSpeakerSuggestion(sessionID, m.From)
	}
	// O índice da busca do chat tem o rótulo antigo nos trechos: é refeito na próxima pergunta
	store.DeleteSessionPassages(sessionID)
	known.Merged = nil
	if OnMerge != nil {
		OnMerge(sessionID)
	}
}

// Consolidate, no fim da sessão, junta as vozes com pouco áudio à pessoa mais
// parecida (ver diarization.Registry.Consolidate) e troca o rótulo nas falas.
func Consolidate(store *storage.Storage, sessionID string) {
	unlock := Lock(sessionID)
	defer unlock()
	known := Load(store, sessionID)
	known.Consolidate()
	if len(known.Merged) > 0 {
		Save(store, sessionID, known)
	}
}

func toSpeakers(voices []storage.SpeakerVoice) []diarization.Speaker {
	out := make([]diarization.Speaker, len(voices))
	for i, v := range voices {
		out[i] = diarization.Speaker{Label: v.Label, Embedding: v.Embedding, Seconds: v.Seconds}
	}
	return out
}

func toVoices(speakers []diarization.Speaker) []storage.SpeakerVoice {
	out := make([]storage.SpeakerVoice, len(speakers))
	for i, s := range speakers {
		out[i] = storage.SpeakerVoice{Label: s.Label, Embedding: s.Embedding, Seconds: s.Seconds}
	}
	return out
}

// Rename dá um nome a uma voz da sessão (ex: "Pessoa 3" -> "Ana"). Se o
// nome já existir na sessão, as duas vozes viram uma só. Um nome de verdade
// (não "Pessoa N") fica guardado com a voz para reconhecer a pessoa nas
// próximas sessões do mesmo workspace. Se a voz tinha outro nome, a voz
// guardada dessa outra pessoa deixa de contar com esta reunião.
func Rename(store *storage.Storage, sessionID, from, to string) error {
	to = strings.TrimSpace(to)
	if to == "" || to == from {
		return nil
	}
	unlock := Lock(sessionID)
	defer unlock()
	return rename(store, sessionID, from, to)
}

// Unname desfaz o nome dado a uma voz da sessão: ela volta a ser uma
// "Pessoa N" (o rótulo é devolvido) e deixa de contar para a voz guardada da
// pessoa.
func Unname(store *storage.Storage, sessionID, name string) (string, error) {
	unlock := Lock(sessionID)
	defer unlock()
	label := Load(store, sessionID).NextLabel()
	return label, rename(store, sessionID, name, label)
}

// rename faz o trabalho de Rename; a sessão precisa estar travada.
func rename(store *storage.Storage, sessionID, from, to string) error {
	known := Load(store, sessionID)
	voice, ok, err := relabel(store, sessionID, known, from, to)
	if err != nil {
		return err
	}
	store.DeleteSpeakerSuggestion(sessionID, from)
	workspaceID := store.SessionWorkspace(sessionID)
	if !diarization.IsAutoLabel(from) {
		if err := forgetSample(store, workspaceID, from, sessionID); err != nil {
			return err
		}
	}
	if !ok || diarization.IsAutoLabel(to) {
		return nil
	}
	return remember(store, workspaceID, sessionID, voice)
}

// relabel troca o rótulo from por to na sessão, que precisa estar travada: na
// voz (juntando com a de to, se a sessão já tiver), nas falas e no
// responsável pelas tarefas. Devolve a voz que ficou com o rótulo to e se a
// sessão tinha a voz from.
func relabel(store *storage.Storage, sessionID string, known *diarization.Registry, from, to string) (diarization.Speaker, bool, error) {
	voice, ok := known.Rename(from, to)
	if ok {
		if err := store.ReplaceSpeakerVoices(sessionID, toVoices(known.Speakers)); err != nil {
			return voice, ok, err
		}
	}
	if err := store.RenameTranscriptSpeaker(sessionID, from, to); err != nil {
		return voice, ok, err
	}
	if err := store.RenameTaskOwner(sessionID, from, to); err != nil {
		return voice, ok, err
	}
	// O índice da busca do chat tem o rótulo antigo nos trechos: é refeito na próxima pergunta
	store.DeleteSessionPassages(sessionID)
	return voice, ok, nil
}

// remember guarda a voz que recebeu o nome voice.Label na sessão e refaz a voz
// conhecida da pessoa no workspace. As outras reuniões do workspace em que a
// mesma voz já aparecia com esse nome (ou com ele escrito de outro jeito:
// "Caio" e "Caio Ramos") entram junto: assim, quem teve a voz esquecida volta
// a ser reconhecido com tudo o que já se sabia dele, mas uma reunião em que o
// nome estava na voz de outra pessoa fica de fora.
func remember(store *storage.Storage, workspaceID, sessionID string, voice diarization.Speaker) error {
	if len(voice.Embedding) == 0 {
		return nil
	}
	sample := storage.SpeakerVoice{Label: voice.Label, Embedding: voice.Embedding, Seconds: voice.Seconds}
	if err := store.SavePersonSample(workspaceID, sessionID, sample); err != nil {
		return err
	}
	samples, err := store.GetPersonSamples(workspaceID, voice.Label)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, sample := range samples {
		have[sample.SessionID] = true
	}
	others, err := store.SessionVoices(workspaceID, sessionID)
	if err != nil {
		return err
	}
	for _, o := range others {
		if have[o.SessionID] || diarization.IsAutoLabel(o.Label) || !roster.Same(o.Label, voice.Label) || !diarization.SameVoice(o.Embedding, voice.Embedding) {
			continue
		}
		have[o.SessionID] = true
		o.Label = voice.Label
		if err := store.SavePersonSample(workspaceID, o.SessionID, o.SpeakerVoice); err != nil {
			return err
		}
	}
	return refresh(store, workspaceID, voice.Label)
}

// forgetSample tira da voz conhecida de name o que veio da sessão (o nome
// estava errado nela, ou a voz mudou de dono). Se era a única reunião com a
// voz dessa pessoa, ela deixa de ser conhecida.
func forgetSample(store *storage.Storage, workspaceID, name, sessionID string) error {
	samples, err := store.GetPersonSamples(workspaceID, name)
	if err != nil {
		return err
	}
	found := false
	for _, sample := range samples {
		found = found || sample.SessionID == sessionID
	}
	// O nome veio do reconhecimento da voz, e não desta reunião: nada a tirar
	if !found {
		return nil
	}
	if err := store.DeletePersonSample(workspaceID, name, sessionID); err != nil {
		return err
	}
	if len(samples) == 1 {
		log.Printf("A voz de %q deixou de ser conhecida: o nome foi tirado da única reunião que a tinha", name)
		return store.DeletePersonVoice(workspaceID, name)
	}
	return refresh(store, workspaceID, name)
}

// refresh refaz a voz conhecida de name a partir das vozes que receberam o
// nome em cada reunião (ver diarization.Combine).
func refresh(store *storage.Storage, workspaceID, name string) error {
	samples, err := store.GetPersonSamples(workspaceID, name)
	if err != nil {
		return err
	}
	voices := make([]diarization.Speaker, len(samples))
	for i, sample := range samples {
		voices[i] = diarization.Speaker{Embedding: sample.Embedding, Seconds: sample.Seconds}
	}
	combined, ok := diarization.Combine(voices)
	if !ok {
		return nil
	}
	return store.SavePersonVoice(workspaceID, storage.SpeakerVoice{Label: name, Embedding: combined.Embedding, Seconds: combined.Seconds})
}

// Forget apaga a voz conhecida de uma pessoa no workspace. As transcrições já
// feitas continuam com o nome dela.
func Forget(store *storage.Storage, workspaceID, name string) error {
	log.Printf("Voz conhecida de %q esquecida a pedido do usuário", name)
	if err := store.DeletePersonSamples(workspaceID, name); err != nil {
		return err
	}
	return store.DeletePersonVoice(workspaceID, name)
}

// RenamePerson troca o nome de uma pessoa com voz conhecida no workspace (ex:
// a voz guardada como "Diego" é do Diogo). Nas reuniões do workspace, as
// falas, as vozes e as tarefas com o nome antigo passam a usar o novo. Se o
// workspace já conhecer alguém com o nome novo, as duas vozes viram uma só.
// Devolve as sessões que mudaram.
func RenamePerson(store *storage.Storage, workspaceID, from, to string) ([]string, error) {
	to = strings.TrimSpace(to)
	if to == "" || to == from {
		return nil, nil
	}
	if diarization.IsAutoLabel(to) {
		return nil, fmt.Errorf("%q é um rótulo automático; use o nome da pessoa", to)
	}
	people, err := store.GetPeopleVoices(workspaceID)
	if err != nil {
		return nil, err
	}
	var old, existing *storage.SpeakerVoice
	for i := range people {
		switch people[i].Label {
		case from:
			old = &people[i]
		case to:
			existing = &people[i]
		}
	}
	if old == nil {
		return nil, fmt.Errorf("não há voz guardada com o nome %q", from)
	}
	// Uma voz guardada por uma versão antiga do app não tem o registro por
	// reunião: ela mesma vira o primeiro
	for _, p := range []*storage.SpeakerVoice{old, existing} {
		if p == nil {
			continue
		}
		if samples, err := store.GetPersonSamples(workspaceID, p.Label); err != nil {
			return nil, err
		} else if len(samples) == 0 {
			if err := store.SavePersonSample(workspaceID, "", *p); err != nil {
				return nil, err
			}
		}
	}
	moving, err := store.GetPersonSamples(workspaceID, from)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		err = store.RenamePersonVoice(workspaceID, from, to)
	} else {
		err = store.DeletePersonVoice(workspaceID, from)
	}
	if err != nil {
		return nil, err
	}

	// Depois da voz: um bloco transcrito a partir daqui já usa o nome novo
	ids, err := store.SessionsWithSpeaker(workspaceID, from)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, id := range ids {
		unlock := Lock(id)
		_, _, err := relabel(store, id, Load(store, id), from, to)
		unlock()
		if err != nil {
			return changed, err
		}
		changed = append(changed, id)
	}

	// As vozes que tinham o nome antigo em cada reunião passam para o novo
	for _, sample := range moving {
		if err := moveSample(store, workspaceID, from, to, sample); err != nil {
			return changed, err
		}
	}
	if err := store.DeletePersonSamples(workspaceID, from); err != nil {
		return changed, err
	}
	return changed, refresh(store, workspaceID, to)
}

// moveSample passa para to a voz que tinha o nome from numa reunião. Se a
// reunião tinha as duas (viraram uma voz só ao trocar o nome), fica a voz que
// a reunião tem agora. A voz guardada sem reunião ganha uma chave própria,
// para não tomar o lugar da que to já tinha.
func moveSample(store *storage.Storage, workspaceID, from, to string, sample storage.PersonSample) error {
	voice, key := sample.SpeakerVoice, sample.SessionID
	if key == "" {
		key = "~" + from
	} else if voices, err := store.GetSpeakerVoices(sample.SessionID); err == nil {
		for _, v := range voices {
			if v.Label == to {
				voice = v
			}
		}
	}
	voice.Label = to
	return store.SavePersonSample(workspaceID, key, voice)
}

// Move passa a sessão para outro workspace. As pessoas nomeadas nela passam a
// ser conhecidas também lá, para serem reconhecidas nas próximas reuniões
// desse workspace.
func Move(store *storage.Storage, sessionID, workspaceID string) error {
	unlock := Lock(sessionID)
	defer unlock()
	if err := store.MoveSession(sessionID, workspaceID); err != nil {
		return err
	}
	known := Load(store, sessionID)
	for _, voice := range known.Speakers {
		if diarization.IsAutoLabel(voice.Label) {
			continue
		}
		if err := remember(store, workspaceID, sessionID, voice); err != nil {
			return err
		}
	}
	return nil
}
