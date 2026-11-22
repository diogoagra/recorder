package assistant

import (
	"fmt"
	"log"
	"recorder/internal/diarization"
	"recorder/internal/roster"
	"recorder/internal/storage"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// NameGuess é o nome descoberto na conversa para uma voz da sessão.
type NameGuess struct {
	storage.SpeakerSuggestion
	// Sure: pistas bastantes para dar o nome à voz sem perguntar ao usuário
	Sure bool
}

// As IAs que recebem a reunião inteira numa chamada (as da nuvem) analisam
// todas as pistas de uma vez (namesFromClues); um modelo local pequeno se
// perde nisso e segue o caminho em duas etapas (SuggestNames).
const wholeMeetingChars = 100000

// IdentifySpeakers descobre o nome de cada "Pessoa N" pela própria conversa.
// people é a lista de participantes do workspace: quem estiver nela sai com o
// nome escrito como na lista. Devolve nil se não houver "Pessoa N" na sessão.
func (a *Assistant) IdentifySpeakers(transcripts []storage.Transcript, people []roster.Person) []NameGuess {
	pending, size := false, 0
	for _, t := range transcripts {
		pending = pending || diarization.IsAutoLabel(t.Speaker)
		size += len(transcriptLine(t))
	}
	if !pending {
		return nil
	}
	if budget := a.LLM.ContextChars(); budget >= wholeMeetingChars && size <= budget {
		guesses, err := a.namesFromClues(transcripts, people)
		if err == nil {
			return guesses
		}
		log.Printf("Erro ao procurar os nomes das pessoas: %v", err)
	}
	guesses := []NameGuess{}
	for _, sg := range a.suggestNames(transcripts, people) {
		guesses = append(guesses, NameGuess{SpeakerSuggestion: sg})
	}
	return guesses
}

const nameCluesPrompt = `Esta é a transcrição de uma reunião. Cada fala começa com o momento do áudio e com quem falou, como "[12:34] Pessoa 3: ...". Os rótulos "Pessoa N" são vozes que ainda não têm nome. Sua tarefa é descobrir, pela própria conversa, o nome de cada "Pessoa N".

PASSO 1 (clues). Liste TODAS as falas em que aparece o nome de alguém que pode estar na reunião. Para cada uma:
- time: o momento da fala, exatamente como aparece (ex: "07:40");
- speaker: o rótulo de quem falou essa fala, como aparece;
- name: o nome, exatamente como está escrito nessa fala;
- kind: "chamou" (alguém chama a pessoa pelo nome, pede algo a ela ou passa a palavra), "apresentou" (a pessoa diz o próprio nome: "aqui é o João") ou "citou" (alguém fala da pessoa em terceira pessoa: "como o João falou", "o que a Ana perguntou", "preciso disso do Pedro");
- about: o rótulo ("Pessoa N" ou um nome que já é rótulo) da voz que É a pessoa com esse nome, ou "" se não dá para saber ou se ela não fala na reunião. Para decidir, olhe as falas vizinhas: quem responde logo depois de ser chamado, falando de si ou do próprio trabalho; quem tinha acabado de dizer aquilo que é citado ("como o João falou" vem depois de o João falar); quem assume o que foi pedido.
- note: uma frase curta com o motivo.

Cuidados no passo 1:
- Quem diz um nome quase nunca é o dono dele. "Ana, pode contar...?" é dito por outra pessoa; a Ana é quem responde.
- A separação das vozes não é perfeita: uma fala costuma trazer, no começo ou no fim, uma frase curta de OUTRA pessoa. Em "[08:40] Pessoa 2: Pode falar, Ana. Desculpa. O que eu queria dizer é que...", o "Pode falar, Ana." foi dito por outra pessoa e quem continua falando é a Ana: about é "Pessoa 2". Do mesmo jeito, uma pergunta no fim de uma fala pode já ser de quem responde.
- A transcrição escreve o mesmo nome de várias formas (Antoni, Anthony, Antony): é a mesma pessoa.
- Quando dois nomes são chamados juntos ("Luis, Diogo, querem falar algo?") e uma voz só responde, não dá para saber qual dos dois ela é: about fica "".

PASSO 2 (people). Para cada "Pessoa N" da transcrição, conclua:
- speaker: o rótulo;
- name: o nome, na grafia mais comum na conversa, ou "" se as pistas não bastam.
- confidence: "alta" se pelo menos duas pistas de falas diferentes apontam para o mesmo nome e nenhuma aponta para outro; "media" se há uma pista só; "baixa" se foi por eliminação ou palpite.
- reason: uma frase curta.
Não dê nome a um rótulo cujas falas parecem ser de mais de uma pessoa (respondem a nomes diferentes, trocam de idioma no meio).`

var nameCluesSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"clues": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"time":    map[string]any{"type": "string"},
					"speaker": map[string]any{"type": "string"},
					"name":    map[string]any{"type": "string"},
					"kind":    map[string]any{"type": "string", "enum": []string{"chamou", "apresentou", "citou"}},
					"about":   map[string]any{"type": "string"},
					"note":    map[string]any{"type": "string"},
				},
				"required": []string{"time", "speaker", "name", "kind", "about", "note"},
			},
		},
		"people": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"speaker":    map[string]any{"type": "string"},
					"name":       map[string]any{"type": "string"},
					"confidence": map[string]any{"type": "string", "enum": []string{"alta", "media", "baixa"}},
					"reason":     map[string]any{"type": "string"},
				},
				"required": []string{"speaker", "name", "confidence", "reason"},
			},
		},
	},
	"required": []string{"clues", "people"},
}

// Pistas de falas diferentes, ditas por outras pessoas, para dar o nome sem
// perguntar. Com 2, numa das transcrições da reunião de 19/11/2026 as três
// respostas da IA deram a uma voz o nome de quem quase não falou ("Anthony,
// Dominic, amanhã vocês..." duas vezes, e quem falou em seguida foi outra
// pessoa); só não virou nome porque havia uma pista contrária.
const sureClues = 3

// Cada fala que aponta a voz para outro nome anula esta quantidade das que
// apontam para o nome escolhido; se não sobrar nenhuma, a voz fica sem nome.
const againstWeight = 3

// A mesma pergunta é feita três vezes, ao mesmo tempo, e vale o que a maioria
// das respostas concluir: de uma resposta para outra a IA lista mais ou menos
// pistas e, uma vez em cada seis nos testes, se confunde (troca duas pessoas,
// dá a uma voz o nome de quem só foi citado). Com uma resposta só, a mesma
// reunião dava o nome de alguém numa vez e na outra não.
const nameAsks = 3

// nameAnswer é a resposta da IA a nameCluesPrompt.
type nameAnswer struct {
	Clues []struct {
		Time, Speaker, Name, Kind, About string
	} `json:"clues"`
	People []struct {
		Speaker, Name, Confidence string
	} `json:"people"`
}

// namesFromClues pede à IA as pistas de nomes da reunião e o nome de cada voz,
// e confere as conclusões dela. Uma voz ganha um nome quando a maioria das
// respostas conclui por ele e as pistas dessas respostas sobre a voz são quase
// todas desse nome; só conta a pista cuja fala existe e tem o nome escrito. O
// nome é dado como certo (Sure) quando pelo menos sureClues falas de outras
// pessoas apontam para a voz; com menos (e pelo menos uma), vira uma sugestão.
func (a *Assistant) namesFromClues(transcripts []storage.Transcript, people []roster.Person) ([]NameGuess, error) {
	a.status("Procurando os nomes das pessoas...")
	// A lista de participantes não vai para a IA: com ela, a IA tentava encaixar
	// todas as vozes na lista e errava mais. A grafia da lista é aplicada
	// depois, ao nome que a conversa mostrou (ver spelling)
	content := "--- TRANSCRIÇÃO ---\n" + strings.Join(transcriptLines(transcripts), "")
	asked := make([]nameAnswer, nameAsks)
	errs := make([]error, nameAsks)
	var wg sync.WaitGroup
	for i := range asked {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = a.LLM.AskJSON(nameCluesPrompt, content, nameCluesSchema, &asked[i])
		}()
	}
	wg.Wait()
	var answers []nameAnswer
	var failed error
	for i, err := range errs {
		if err != nil {
			failed = err
			continue
		}
		answers = append(answers, asked[i])
	}
	if len(answers) == 0 {
		return nil, failed
	}
	// Quantas respostas precisam concordar
	quorum := len(answers)/2 + 1

	labels := map[string]bool{}
	for _, t := range transcripts {
		labels[t.Speaker] = true
	}
	// same diz se dois nomes são da mesma pessoa: o mesmo nome escrito de outro
	// jeito, um apelido, ou dois jeitos de chamar alguém da lista de participantes
	same := func(x, y string) bool {
		if roster.Alike(x, y) {
			return true
		}
		px, py := roster.Guess(people, x), roster.Guess(people, y)
		return px != nil && px == py
	}

	type clue struct {
		name   string
		line   int
		strong bool // dita por outra pessoa, ou uma apresentação
	}
	// O que cada resposta diz de cada voz: o nome que concluiu e as pistas
	type opinion struct {
		name  string
		clues []clue
	}
	opinions := map[string][]opinion{} // rótulo -> uma opinião por resposta que deu nome à voz
	for _, answer := range answers {
		// Quando a fala chama mais de uma pessoa ("Ana, Pedro, amanhã vocês..."),
		// não dá para saber qual delas é a voz que respondeu: a pista é fraca
		called := map[string][]string{} // momento e quem falou -> nomes chamados na fala
		for _, c := range answer.Clues {
			if c.Kind == "chamou" {
				key := strings.TrimSpace(c.Time) + "|" + strings.TrimSpace(c.Speaker)
				called[key] = append(called[key], strings.TrimSpace(c.Name))
			}
		}
		several := func(time, speaker, name string) bool {
			for _, other := range called[strings.TrimSpace(time)+"|"+speaker] {
				if !same(other, name) {
					return true
				}
			}
			return false
		}
		clues := map[string][]clue{}
		for _, c := range answer.Clues {
			name, about, speaker := strings.TrimSpace(c.Name), strings.TrimSpace(c.About), strings.TrimSpace(c.Speaker)
			if name == "" || !diarization.IsAutoLabel(about) || !labels[about] {
				continue
			}
			if line := findLine(transcripts, speaker, c.Time, name); line >= 0 {
				strong := c.Kind == "apresentou" || (speaker != about && !(c.Kind == "chamou" && several(c.Time, speaker, name)))
				clues[about] = append(clues[about], clue{name, line, strong})
			}
		}
		named := map[string]bool{}
		for _, p := range answer.People {
			speaker, name := strings.TrimSpace(p.Speaker), strings.Join(strings.Fields(p.Name), " ")
			if name != "" && p.Confidence != "baixa" && diarization.IsAutoLabel(speaker) && labels[speaker] && !named[speaker] &&
				utf8.RuneCountInString(name) <= 60 && !diarization.IsAutoLabel(name) {
				named[speaker] = true
				opinions[speaker] = append(opinions[speaker], opinion{name, clues[speaker]})
			}
		}
	}

	type candidate struct {
		NameGuess
		strong, weak int
	}
	var candidates []candidate
	for speaker, all := range opinions {
		// O nome com mais respostas a favor
		var agreeing []opinion
		for _, o := range all {
			var with []opinion
			for _, other := range all {
				if same(o.name, other.name) {
					with = append(with, other)
				}
			}
			if len(with) > len(agreeing) {
				agreeing = with
			}
		}
		c := candidate{NameGuess: NameGuess{SpeakerSuggestion: storage.SpeakerSuggestion{Speaker: speaker}}}
		// Uma pista vale quando aparece em mais de uma das respostas que
		// concordam: a que só uma resposta viu costuma ser engano dela
		need := min(2, len(agreeing))
		// Em quantas respostas a fala é pista a favor do nome (forte ou fraca)
		// e em quantas é pista de outro nome; uma fala pode ser as duas coisas
		type tally struct {
			name, otherName      string
			strong, weak, others int
		}
		lines := map[int]*tally{}
		var names []string
		spellings := map[string]int{}
		for _, o := range agreeing {
			names = append(names, o.name)
			favor, other := map[int]bool{}, map[int]bool{}
			for _, cl := range o.clues {
				if lines[cl.line] == nil {
					lines[cl.line] = &tally{}
				}
				l := lines[cl.line]
				if !same(cl.name, o.name) {
					if cl.strong && !other[cl.line] {
						other[cl.line] = true
						l.others++
						l.otherName = cl.name
					}
					continue
				}
				if favor[cl.line] {
					continue
				}
				favor[cl.line] = true
				l.name = cl.name
				spellings[cl.name]++
				if cl.strong {
					l.strong++
				} else {
					l.weak++
				}
			}
		}
		order := make([]int, 0, len(lines))
		for line := range lines {
			order = append(order, line)
		}
		sort.Ints(order)
		var against, evidence []string
		for _, line := range order {
			l := lines[line]
			if l.others >= need {
				against = append(against, l.otherName)
			}
			switch {
			case l.strong >= need:
				c.strong++
			case l.strong+l.weak >= need:
				c.weak++
			default:
				continue
			}
			if len(evidence) < 2 {
				evidence = append(evidence, excerpt(transcripts[line], l.name))
			}
		}
		c.Name = spelling(names, spellings, people)
		// O nome já é de outra voz da sessão: não dá para dizer que são a mesma
		taken := false
		for label := range labels {
			taken = taken || (!diarization.IsAutoLabel(label) && same(label, c.Name))
		}
		// Fica no log o que a IA concluiu e o que as pistas sustentam: é por
		// onde dá para entender depois por que uma voz ficou sem nome
		log.Printf("Nomes pela conversa: %s seria %q para %d de %d respostas: %d pistas de outras pessoas, %d da própria voz, pistas para outro nome: %v, nome já usado na sessão: %v",
			speaker, c.Name, len(agreeing), len(answers), c.strong, c.weak, against, taken)
		// Uma pista solta para outro nome é ruído da IA; várias, diante das
		// que apontam para este, é sinal de duas pessoas na mesma voz
		net := c.strong - len(against)*againstWeight
		mixed := len(against) > 0 && net <= 0
		// Sem nenhuma fala de outra pessoa apontando para a voz, não há o que sugerir
		if len(agreeing) < quorum || mixed || taken || c.strong == 0 {
			continue
		}
		c.Evidence = strings.Join(evidence, " … ")
		c.Sure = net >= sureClues && len(answers) > 1
		candidates = append(candidates, c)
	}

	// Uma voz por nome: fica a que tem mais pistas
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].strong != candidates[j].strong {
			return candidates[i].strong > candidates[j].strong
		}
		if candidates[i].weak != candidates[j].weak {
			return candidates[i].weak > candidates[j].weak
		}
		return candidates[i].Speaker < candidates[j].Speaker
	})
	guesses := []NameGuess{}
	for _, c := range candidates {
		taken := false
		for _, g := range guesses {
			taken = taken || same(g.Name, c.Name)
		}
		if !taken {
			guesses = append(guesses, c.NameGuess)
		}
	}
	sort.SliceStable(guesses, func(i, j int) bool { return guesses[i].Speaker < guesses[j].Speaker })
	return guesses, nil
}

// spelling escolhe como escrever o nome de uma voz: como está na lista de
// participantes, se a pessoa estiver nela; senão, a grafia mais frequente nas
// falas (a mais longa no empate: "Alexander" em vez de "Alex"), ou a que a IA
// concluiu, quando nenhuma fala serve.
func spelling(concluded []string, spellings map[string]int, people []roster.Person) string {
	for _, name := range concluded {
		if person := roster.Guess(people, name); person != nil {
			return person.Name
		}
	}
	best := ""
	for name, count := range spellings {
		if person := roster.Guess(people, name); person != nil {
			return person.Name
		}
		switch {
		case best == "" || count > spellings[best]:
			best = name
		case count == spellings[best] && (len(name) > len(best) || (len(name) == len(best) && name < best)):
			best = name
		}
	}
	if best == "" {
		return concluded[0]
	}
	return best
}

// participantsBlock escreve a lista de participantes do workspace para vir
// antes da transcrição; vazio se não houver lista.
func participantsBlock(people []roster.Person) string {
	if len(people) == 0 {
		return ""
	}
	return "--- PARTICIPANTES CONHECIDOS (nem todos estão nesta reunião; entre parênteses, outros jeitos de chamar a pessoa) ---\n" + roster.Text(people) + "\n"
}

// findLine devolve a fala de speaker no momento time que tem o nome escrito, ou -1.
func findLine(transcripts []storage.Transcript, speaker, time, name string) int {
	time = strings.Trim(strings.TrimSpace(time), "[]")
	for i, t := range transcripts {
		if t.Speaker == speaker && t.Offset >= 0 && FormatOffset(t.Offset) == time &&
			strings.Contains(" "+normalizeWords(t.Text)+" ", " "+normalizeWords(name)+" ") {
			return i
		}
	}
	return -1
}

// excerpt devolve a fala com o momento e quem falou, cortada em volta do nome
// quando é longa.
func excerpt(t storage.Transcript, name string) string {
	const around = 90
	text := []rune(t.Text)
	if len(text) > 2*around {
		at := max(strings.Index(strings.ToLower(t.Text), strings.ToLower(name)), 0)
		at = utf8.RuneCountInString(t.Text[:at])
		from, to := max(at-around, 0), min(at+around, len(text))
		cut := strings.TrimSpace(string(text[from:to]))
		if from > 0 {
			cut = "…" + cut
		}
		if to < len(text) {
			cut += "…"
		}
		t.Text = cut
	}
	return strings.TrimSpace(transcriptLine(t))
}

// No caminho em duas etapas, perguntar direto "quem é a Pessoa
// 3?" não funciona: o modelo atribui a quem fala o nome que essa pessoa diz
// ("Ana, pode contar...?" virava "Pessoa 3 = Ana", mas a Ana é quem responde).
// Então primeiro ele só aponta as falas em que alguém chama outra pessoa pelo
// nome ou se apresenta; depois, para cada chamada, olhando as falas seguintes,
// diz quem respondeu.
const nameMentionsPrompt = `Esta é a transcrição de uma reunião. Cada fala começa com o momento do áudio e com quem falou, como "[12:34] Pessoa 3: ...".
Encontre as falas em que alguém:
- chama outra pessoa pelo nome, para ela falar ou responder (ex: "[07:40] Pessoa 3: Ana, pode contar o que você fez?"): kind "chamou";
- se apresenta pelo próprio nome (ex: "[02:10] Pessoa 5: Aqui é o João"): kind "apresentou".
NÃO inclua pessoas apenas mencionadas na conversa (ex: "o Carlos me explicou").
Para cada uma, informe: time (o momento da fala, exatamente como aparece, ex: "07:40"), speaker (quem falou, ex: "Pessoa 3"), name (o nome chamado ou apresentado, como foi dito) e kind.`

var nameMentionsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"mentions": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"time":    map[string]any{"type": "string"},
					"speaker": map[string]any{"type": "string"},
					"name":    map[string]any{"type": "string"},
					"kind":    map[string]any{"type": "string", "enum": []string{"chamou", "apresentou"}},
				},
				"required": []string{"time", "speaker", "name", "kind"},
			},
		},
	},
	"required": []string{"mentions"},
}

const nameAnswerPrompt = `Na conversa abaixo, %s chama %q na primeira fala. Quem respondeu a esse chamado, ou seja, quem é %q?
Quem responde é quem atende ao chamado e passa a falar de si ou do próprio trabalho. Atenção: a separação das vozes não é perfeita, e quem chamou pode continuar falando com outro rótulo (por exemplo, completando a pergunta ou dando instruções); essa pessoa não é quem respondeu.
Responda "nenhum" se não estiver claro.`

const (
	// Falas seguintes a uma chamada que o modelo olha para achar quem respondeu
	answerWindowLines   = 8
	answerWindowSeconds = 45
	// Limite de chamadas conferidas por sessão (cada uma é uma chamada ao Ollama)
	maxNameChecks = 40
)

// SuggestNames descobre o nome provável de cada "Pessoa N" pela própria
// conversa e devolve as sugestões, com o trecho que as comprova, para o
// usuário confirmar.
func (a *Assistant) SuggestNames(transcripts []storage.Transcript) []storage.SpeakerSuggestion {
	return a.suggestNames(transcripts, nil)
}

func (a *Assistant) suggestNames(transcripts []storage.Transcript, people []roster.Person) []storage.SpeakerSuggestion {
	labels := map[string]bool{}
	pending := false
	for _, t := range transcripts {
		if t.Speaker != "" {
			labels[t.Speaker] = true
			pending = pending || diarization.IsAutoLabel(t.Speaker)
		}
	}
	if !pending {
		return nil
	}

	// 1. Falas em que alguém chama outra pessoa pelo nome ou se apresenta
	type mention struct {
		line       int
		name, kind string
	}
	var mentions []mention
	chunks := chunkTranscripts(transcripts, a.LLM.ContextChars())
	for c, chunk := range chunks {
		if len(chunks) > 1 {
			a.status(fmt.Sprintf("Procurando os nomes das pessoas (parte %d de %d)...", c+1, len(chunks)))
		} else {
			a.status("Procurando os nomes das pessoas...")
		}
		var out struct {
			Mentions []struct {
				Time, Speaker, Name, Kind string
			} `json:"mentions"`
		}
		if err := a.LLM.AskJSON(nameMentionsPrompt, "--- TRANSCRIÇÃO ---\n"+strings.Join(transcriptLines(transcripts[chunk[0]:chunk[1]]), ""), nameMentionsSchema, &out); err != nil {
			log.Printf("Erro ao procurar os nomes das pessoas: %v", err)
			continue
		}
		for _, m := range out.Mentions {
			name := strings.TrimSpace(m.Name)
			if name == "" || utf8.RuneCountInString(name) > 40 || diarization.IsAutoLabel(name) {
				continue
			}
			// A fala precisa existir e conter o nome
			for i := chunk[0]; i < chunk[1]; i++ {
				t := transcripts[i]
				if t.Speaker == strings.TrimSpace(m.Speaker) && t.Offset >= 0 && FormatOffset(t.Offset) == strings.Trim(strings.TrimSpace(m.Time), "[]") &&
					strings.Contains(normalizeWords(t.Text), normalizeWords(name)) {
					mentions = append(mentions, mention{i, name, m.Kind})
					break
				}
			}
		}
	}

	// 2. Quem é cada nome: quem se apresentou ou quem respondeu à chamada
	type vote struct {
		name     string
		count    int
		evidence string
	}
	votes := map[string]map[string]*vote{} // rótulo -> nome normalizado -> votos
	addVote := func(speaker, name, evidence string) {
		if person := roster.Find(people, name); person != nil {
			name = person.Name
		}
		if !diarization.IsAutoLabel(speaker) || labels[name] {
			return
		}
		if votes[speaker] == nil {
			votes[speaker] = map[string]*vote{}
		}
		// O mesmo nome escrito de outro jeito (Antoni, Anthony) é o mesmo voto.
		// Antes cada grafia era um nome, as duas empatavam e a sugestão sumia
		key := normalizeWords(name)
		for k, v := range votes[speaker] {
			if roster.Same(v.name, name) {
				key = k
			}
		}
		if votes[speaker][key] == nil {
			votes[speaker][key] = &vote{name: name, evidence: evidence}
		}
		votes[speaker][key].count++
	}
	checks := 0
	for _, m := range mentions {
		call := transcripts[m.line]
		if m.kind == "apresentou" {
			addVote(call.Speaker, m.name, strings.TrimSpace(transcriptLine(call)))
			continue
		}
		if checks >= maxNameChecks {
			continue
		}

		// As falas seguintes, e quem pode ter respondido: qualquer um menos quem
		// chamou, cuja primeira fala depois da chamada conta algo. Uma pergunta
		// costuma ser quem chamou continuando com outro rótulo, e um "Sim." ou
		// "True." não basta para dizer quem é a pessoa
		end := m.line + 1
		for end < len(transcripts) && end-m.line <= answerWindowLines && transcripts[end].Offset-call.Offset <= answerWindowSeconds {
			end++
		}
		var candidates []string
		seen := map[string]bool{call.Speaker: true}
		for _, t := range transcripts[m.line+1 : end] {
			if t.Speaker == "" || seen[t.Speaker] {
				continue
			}
			seen[t.Speaker] = true
			if looksLikeAnswer(t.Text) {
				candidates = append(candidates, t.Speaker)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		checks++
		a.status(fmt.Sprintf("Conferindo quem é quem (%d)...", checks))
		var answer struct {
			Speaker string `json:"speaker"`
		}
		schema := map[string]any{
			"type":       "object",
			"properties": map[string]any{"speaker": map[string]any{"type": "string", "enum": append(candidates, "nenhum")}},
			"required":   []string{"speaker"},
		}
		window := strings.Join(transcriptLines(transcripts[m.line:end]), "")
		if err := a.LLM.AskJSON(fmt.Sprintf(nameAnswerPrompt, call.Speaker, m.name, m.name), window, schema, &answer); err != nil {
			log.Printf("Erro ao conferir quem respondeu a %q: %v", m.name, err)
			continue
		}
		for _, t := range transcripts[m.line+1 : end] {
			if t.Speaker == answer.Speaker {
				addVote(t.Speaker, m.name, strings.TrimSpace(transcriptLine(call))+" → "+strings.TrimSpace(transcriptLine(t)))
				break
			}
		}
	}

	// Um nome por pessoa e uma pessoa por nome. Se a mesma voz recebeu nomes
	// diferentes com a mesma força, não dá para saber: fica sem sugestão
	type candidate struct {
		storage.SpeakerSuggestion
		count int
	}
	var candidates []candidate
	for speaker, names := range votes {
		var best candidate
		tie := false
		for _, v := range names {
			switch {
			case v.count > best.count:
				best, tie = candidate{storage.SpeakerSuggestion{Speaker: speaker, Name: v.name, Evidence: v.evidence}, v.count}, false
			case v.count == best.count:
				tie = true
			}
		}
		if !tie {
			candidates = append(candidates, best)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].count != candidates[j].count {
			return candidates[i].count > candidates[j].count
		}
		return candidates[i].Speaker < candidates[j].Speaker
	})
	suggestions := []storage.SpeakerSuggestion{}
	for _, c := range candidates {
		taken := false
		for _, sg := range suggestions {
			taken = taken || roster.Same(sg.Name, c.Name)
		}
		if !taken {
			suggestions = append(suggestions, c.SpeakerSuggestion)
		}
	}
	return suggestions
}

// looksLikeAnswer diz se a fala pode ser alguém atendendo a um chamado: tem
// pelo menos 4 palavras e não começa com uma pergunta.
func looksLikeAnswer(text string) bool {
	if len(strings.Fields(text)) < 4 {
		return false
	}
	first := strings.TrimSpace(text)
	if i := strings.IndexAny(first, ".!?…"); i >= 0 {
		return first[i] != '?'
	}
	return true
}
