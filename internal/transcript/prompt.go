package transcript

import (
	"recorder/internal/roster"
	"recorder/internal/storage"
	"strings"
)

// O Whisper só aproveita uns 220 tokens do prompt; acima disso o começo é ignorado.
const maxWhisperPrompt = 600

// whisperPrompt monta as palavras esperadas que o Whisper recebe para acertar
// a grafia: os nomes da lista de participantes e das pessoas com voz conhecida
// e o glossário (sistemas, clientes, siglas...) do workspace da sessão.
func whisperPrompt(store *storage.Storage, sessionID string) string {
	workspaceID := store.SessionWorkspace(sessionID)
	participants, _ := store.GetParticipants(workspaceID)
	names := roster.Names(roster.Parse(participants))
	if people, err := store.GetPeopleVoices(workspaceID); err == nil {
		for _, p := range people {
			listed := false
			for _, name := range names {
				listed = listed || roster.Same(name, p.Label)
			}
			if !listed {
				names = append(names, p.Label)
			}
		}
	}
	glossary, _ := store.GetGlossary(workspaceID)

	var b strings.Builder
	add := func(label string, words []string) {
		for i, w := range words {
			piece := ", " + w
			if i == 0 {
				piece = label + ": " + w
				if b.Len() > 0 {
					piece = ". " + piece
				}
			}
			if b.Len()+len(piece) > maxWhisperPrompt {
				return
			}
			b.WriteString(piece)
		}
	}
	add("Participantes", names)
	add("Termos", glossaryTerms(glossary))
	if b.Len() > 0 {
		b.WriteString(".")
	}
	return b.String()
}

// glossaryTerms separa o glossário (um termo por linha ou separados por
// vírgula), sem repetições.
func glossaryTerms(glossary string) []string {
	seen := map[string]bool{}
	var terms []string
	for _, t := range strings.FieldsFunc(glossary, func(r rune) bool { return r == '\n' || r == ',' || r == ';' }) {
		t = strings.TrimSpace(t)
		if t != "" && !seen[strings.ToLower(t)] {
			seen[strings.ToLower(t)] = true
			terms = append(terms, t)
		}
	}
	return terms
}
