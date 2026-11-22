package assistant

import (
	"fmt"
	"log"
	"math"
	"recorder/internal/chat"
	"recorder/internal/storage"
	"sort"
	"strings"
)

// Busca por relevância do chat: em vez de só as falas mais recentes, vão para
// o Ollama os trechos da transcrição mais ligados à pergunta, de qualquer parte
// da reunião (ou de todas as reuniões do workspace). Cada trecho junta falas
// seguidas e guarda o seu embedding no banco; o índice é completado a cada
// pergunta.
//
// Os limites abaixo são os do Ollama (16k tokens); com uma IA de contexto
// maior (agy) eles crescem junto com ContextChars.
const (
	passageSize = 700
	// Quanto texto dos trechos encontrados vai para a IA
	retrievalBudget = 14000
	// Falas mais recentes que vão junto (ex: "o que eu falei agora há pouco?")
	recentBudget = 6000
	// Histórico da conversa que vai junto, das mensagens mais novas para as mais antigas
	historyBudget = 8000
)

// Prefixos recomendados pelo embeddinggemma para buscas
const (
	queryPrefix    = "task: search result | query: "
	documentPrefix = "title: none | text: "
)

// Ask responde à pergunta com o resumo, os trechos encontrados pela busca
// (desta sessão ou, com allSessions, de todas as do workspace dela), as falas
// mais recentes e o histórico da conversa. onText recebe a resposta enquanto
// ela é escrita.
func (a *Assistant) Ask(sessionID, question string, allSessions bool, onText func(string)) (string, error) {
	contextText := a.chatContext(sessionID, question, allSessions)
	a.status("")
	history, _ := a.Store.GetChatMessages(sessionID)
	return a.LLM.Answer(question, contextText, recentMessages(history, max(historyBudget, a.LLM.ContextChars()/10)), onText)
}

// chatContext monta o contexto da pergunta: o resumo da sessão e a
// transcrição inteira, se couber; senão, os trechos encontrados pela busca e
// as falas mais recentes. Sem a busca (ex: Ollama parado ou sem o modelo de
// busca), vão só as falas mais recentes.
func (a *Assistant) chatContext(sessionID, question string, allSessions bool) string {
	var b strings.Builder
	if sess, err := a.Store.GetSession(sessionID); err == nil && sess.Summary != "" {
		fmt.Fprintf(&b, "--- RESUMO DA SESSÃO ---\n%s\n------------------------\n\n", sess.Summary)
	}
	all, _ := a.Store.GetAllTranscripts(sessionID)

	// A reunião inteira cabe numa chamada (no agy, quase sempre): vai toda,
	// sem depender da busca
	budget := a.LLM.ContextChars()
	if !allSessions && transcriptsSize(all) <= budget {
		WriteTranscripts(&b, "TRANSCRIÇÃO DA SESSÃO", all)
		return b.String()
	}

	found, err := a.searchPassages(sessionID, question, allSessions, max(retrievalBudget, budget/2))
	if err != nil {
		log.Printf("Busca indisponível, usando só as falas recentes: %v", err)
		WriteTranscripts(&b, "TRANSCRIÇÕES RECENTES", recentWithin(all, budget))
		return b.String()
	}
	if len(found) > 0 {
		b.WriteString("--- TRECHOS DA TRANSCRIÇÃO RELACIONADOS À PERGUNTA ---\n")
		lastSession := sessionID
		for _, p := range found {
			if p.SessionID != lastSession {
				fmt.Fprintf(&b, "(Reunião: %s)\n", p.SessionName)
				lastSession = p.SessionID
			}
			b.WriteString(p.Text)
		}
		b.WriteString("--------------------\n\n")
	}
	WriteTranscripts(&b, "FALAS MAIS RECENTES DESTA SESSÃO", recentWithin(all, max(recentBudget, budget/4)))
	return b.String()
}

// transcriptsSize é quanto texto as falas ocupam no contexto.
func transcriptsSize(transcripts []storage.Transcript) int {
	size := 0
	for _, t := range transcripts {
		size += len(transcriptLine(t))
	}
	return size
}

// searchPassages devolve os trechos mais relacionados à pergunta, até budget
// caracteres: primeiro os da sessão atual, depois os das outras, cada grupo
// na ordem em que foi dito.
func (a *Assistant) searchPassages(sessionID, question string, allSessions bool, budget int) ([]storage.Passage, error) {
	a.searchMu.Lock()
	defer a.searchMu.Unlock()

	if !a.searchReady {
		err := a.Ollama.EnsureModel(chat.EmbeddingModel, func(percent int) {
			a.status(fmt.Sprintf("Baixando o modelo de busca (%d%%)...", percent))
		})
		if err != nil {
			return nil, err
		}
		a.searchReady = true
	}

	// "Todas as reuniões" são as do workspace da sessão
	workspaceID := a.Store.SessionWorkspace(sessionID)
	scope := []string{sessionID}
	if allSessions {
		ids, err := a.Store.WorkspaceSessionIDs(workspaceID)
		if err != nil {
			return nil, err
		}
		scope = ids
	}
	for _, id := range scope {
		if err := a.indexSession(id); err != nil {
			return nil, err
		}
	}

	query, err := a.Ollama.Embed([]string{queryPrefix + question})
	if err != nil {
		return nil, err
	}
	var passages []storage.Passage
	if allSessions {
		passages, err = a.Store.GetWorkspacePassages(workspaceID)
	} else {
		passages, err = a.Store.GetPassages(sessionID)
	}
	if err != nil {
		return nil, err
	}
	return pickPassages(passages, query[0], sessionID, budget), nil
}

// pickPassages escolhe os trechos mais parecidos com a pergunta até budget
// caracteres e os devolve em ordem: sessão atual primeiro, depois as outras.
func pickPassages(passages []storage.Passage, query []float32, sessionID string, budget int) []storage.Passage {
	order := make([]int, len(passages))
	scores := make([]float64, len(passages))
	for i, p := range passages {
		order[i] = i
		scores[i] = cosine(query, p.Embedding)
	}
	sort.SliceStable(order, func(x, y int) bool { return scores[order[x]] > scores[order[y]] })

	var chosen []int
	used := 0
	for _, i := range order {
		if used+len(passages[i].Text) > budget {
			continue
		}
		used += len(passages[i].Text)
		chosen = append(chosen, i)
	}
	sort.Slice(chosen, func(x, y int) bool {
		px, py := passages[chosen[x]], passages[chosen[y]]
		if (px.SessionID == sessionID) != (py.SessionID == sessionID) {
			return px.SessionID == sessionID
		}
		if px.SessionID != py.SessionID {
			return px.SessionID < py.SessionID
		}
		return chosen[x] < chosen[y]
	})
	out := make([]storage.Passage, len(chosen))
	for i, c := range chosen {
		out[i] = passages[c]
	}
	return out
}

// indexSession indexa as falas da sessão que ainda não estão no índice.
func (a *Assistant) indexSession(sessionID string) error {
	// Só o id da última fala indexada: carregar os trechos de cada sessão (com
	// os embeddings) a cada pergunta em "todas as reuniões" pesava
	last, err := a.Store.LastIndexedTranscript(sessionID)
	if err != nil {
		return err
	}
	pending, err := a.Store.GetTranscriptsAfter(sessionID, last)
	if err != nil || len(pending) == 0 {
		return err
	}

	passages := buildPassages(sessionID, pending)
	const batch = 32
	for start := 0; start < len(passages); start += batch {
		if len(passages) > batch {
			a.status(fmt.Sprintf("Preparando a busca na transcrição (%d%%)...", start*100/len(passages)))
		}
		group := passages[start:min(start+batch, len(passages))]
		texts := make([]string, len(group))
		for i, p := range group {
			texts[i] = documentPrefix + p.Text
		}
		embeddings, err := a.Ollama.Embed(texts)
		if err != nil {
			return err
		}
		for i := range group {
			group[i].Embedding = embeddings[i]
		}
	}
	return a.Store.AddPassages(passages)
}

// buildPassages junta falas seguidas em trechos de até passageSize caracteres.
func buildPassages(sessionID string, transcripts []storage.Transcript) []storage.Passage {
	var out []storage.Passage
	var cur *storage.Passage
	for _, t := range transcripts {
		for _, piece := range splitLong(transcriptLine(t), passageSize) {
			if cur != nil && len(cur.Text)+len(piece) > passageSize {
				out = append(out, *cur)
				cur = nil
			}
			if cur == nil {
				cur = &storage.Passage{SessionID: sessionID, Offset: t.Offset}
			}
			cur.Text += piece
			cur.LastTranscriptID = t.ID
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// recentMessages devolve as mensagens mais novas da conversa que cabem em budget caracteres.
func recentMessages(history []storage.ChatMessage, budget int) []storage.ChatMessage {
	used := 0
	for i := len(history) - 1; i >= 0; i-- {
		used += len(history[i].Content) + 16
		if used > budget {
			return history[i+1:]
		}
	}
	return history
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return -1
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return -1
	}
	return dot / math.Sqrt(na*nb)
}
