package diarization

import (
	"fmt"
	"math"
	"sort"
)

// Similaridade de cosseno mínima para considerar que uma voz é de alguém que
// já falou na sessão. Numa conversa de 2 pessoas gravada no Mac, as duas
// ficavam entre 0.1 e 0.35; numa reunião de 6 pelo Meet (19/11/2026), pessoas
// diferentes chegaram a 0.49, e os trechos de 4s ou mais da mesma pessoa
// ficaram quase todos acima de 0.66. Com 0.4, quem falava pouco era engolido
// pela voz de quem falava muito.
const matchThreshold = 0.5

// Vozes de uma mesma pessoa acumuladas em vários blocos ficam acima disto; as
// de pessoas diferentes, bem abaixo. Numa conversa de 2 pessoas gravada no Mac,
// os pedaços da mesma pessoa ficaram entre 0.56 e 0.81 e as duas pessoas entre
// 0.1 e 0.35.
const sameSpeakerThreshold = 0.6

// Com menos áudio que isso a "impressão digital" da voz é pouco confiável:
// o trecho fica com a voz conhecida mais parecida em vez de virar uma pessoa
// nova. Com 2 a 4 segundos, 10 a 25% dos trechos de uma pessoa ficavam abaixo
// de matchThreshold com ela mesma e viravam pessoas que não existiam.
const minNewSpeakerSeconds = 4.0

// No fim da sessão, uma "Pessoa N" com menos áudio que isso é juntada à pessoa
// mais parecida (ver Consolidate).
const minSpeakerSeconds = 15.0

// Para reconhecer alguém de outras reuniões a exigência é maior: errar o nome
// é pior do que deixar "Pessoa N". A voz precisa ter áudio bastante, parecer
// com a da pessoa e se destacar da segunda mais parecida, porque num time há
// vozes parecidas: de uma reunião para outra, a mesma pessoa fica entre 0.6 e
// 0.87 e pessoas diferentes chegam a 0.56. Com 0.5 de mínimo, 0.05 de folga e
// a decisão tomada nos primeiros 4s de alguém, uma pessoa passou uma reunião
// inteira com o nome de outra.
const (
	personThreshold  = 0.6
	personMargin     = 0.1
	minPersonSeconds = 10.0
	// Uma voz do bloco só fica com alguém reconhecido de outras reuniões se
	// também lembrar a voz guardada dessa pessoa, e não só a média da sessão,
	// que pode ter começado com um trecho de duas pessoas misturadas. Em
	// 18/11/2026, a reunião começou com um trecho assim, reconhecido como uma
	// pessoa; a voz seguinte (de outra, 0.37 a 0.43 com a guardada) parecia com
	// essa média e ficou a reunião inteira com o nome errado.
	anchorThreshold = 0.45
)

// Na voz guardada de uma pessoa, a voz dela em cada reunião pesa no máximo
// como esta quantidade de áudio, para uma reunião longa não valer por todas.
const maxRememberedSeconds = 300.0

// A voz que recebeu o nome de uma pessoa numa reunião só entra na voz guardada
// dela se parecer com a das outras reuniões pelo menos isto. De uma reunião
// para outra a mesma pessoa fica entre 0.6 e 0.87 (0.46 a 0.55 quando o
// microfone muda muito); abaixo disso, o nome foi dado à voz de outra pessoa.
const sampleThreshold = 0.5

// Speaker é uma voz já identificada na sessão.
type Speaker struct {
	Label     string    // "Pessoa 1", "Pessoa 2"...
	Embedding []float32 // média normalizada das vozes atribuídas a esta pessoa
	Seconds   float64   // quanto áudio já entrou na média
}

// Voice é uma voz encontrada num bloco de áudio.
type Voice struct {
	Embedding []float32 // nil se o trecho foi curto demais para calcular
	Seconds   float64   // quanto áudio foi usado para calcular o embedding
}

// Registry guarda as vozes de uma sessão para que a mesma pessoa receba o
// mesmo rótulo em todos os blocos de áudio.
type Registry struct {
	Speakers []Speaker
	// Pessoas que o usuário já nomeou em alguma sessão (Label é o nome). Uma
	// voz nova que se pareça com uma delas recebe o nome em vez de "Pessoa N".
	People []Speaker
	// Vozes que MatchBlock ou Consolidate juntaram a outra, em ordem, para
	// quem guardou falas com o rótulo antigo trocá-lo
	Merged []Merge
}

// Merge é uma voz juntada a outra da mesma pessoa.
type Merge struct {
	From, To string
}

// MatchBlock devolve o rótulo de cada voz de um mesmo bloco de áudio, na
// ordem recebida. Vozes novas ganham o próximo "Pessoa N". Depois, vozes da
// sessão que se revelam da mesma pessoa viram uma só (ver Merged).
//
// O agrupamento de cada bloco separa demais a mesma pessoa: numa conversa de
// 2 pessoas ele achava umas 5 vozes por bloco de 30s, a maioria com poucos
// segundos. Por isso duas vozes do mesmo bloco podem ficar com o mesmo rótulo.
// Antes não podiam, e um trecho curto de alguém que já tinha falado no bloco
// ia para uma "Pessoa N" inexistente, que assim nunca deixava de existir.
//
// A exceção são duas vozes longas do bloco bem diferentes entre si: aí são
// duas pessoas, e a menos parecida com a pessoa só fica com ela se for
// claramente dela (sameSpeakerThreshold).
func (r *Registry) MatchBlock(voices []Voice) []string {
	labels := make([]string, len(voices))
	embs := make([][]float32, len(voices))
	sims := make([]float64, len(voices))
	order := make([]int, 0, len(voices))
	for i, v := range voices {
		if len(v.Embedding) > 0 {
			embs[i] = normalized(v.Embedding)
			_, sims[i] = r.closest(embs[i], v.Seconds)
			order = append(order, i)
		}
	}
	// As vozes longas primeiro, para que uma pessoa nova do bloco já exista
	// quando os trechos curtos dela forem casados; entre elas, as mais parecidas
	// com alguém da sessão, que ficam com essa pessoa antes de uma menos parecida
	sort.SliceStable(order, func(a, b int) bool {
		la, lb := voices[order[a]].Seconds >= minNewSpeakerSeconds, voices[order[b]].Seconds >= minNewSpeakerSeconds
		if la != lb {
			return la
		}
		return sims[order[a]] > sims[order[b]]
	})

	for n, i := range order {
		emb, seconds := embs[i], voices[i].Seconds
		best, sim := r.closest(emb, seconds)
		if best >= 0 && sim < sameSpeakerThreshold && seconds >= minNewSpeakerSeconds {
			for _, k := range order[:n] {
				if labels[k] == r.Speakers[best].Label && voices[k].Seconds >= minNewSpeakerSeconds && dot(embs[k], emb) < matchThreshold {
					best = -1
					break
				}
			}
		}
		switch {
		case best >= 0 && sim >= matchThreshold:
			// Uma voz só parecida (entre matchThreshold e sameSpeakerThreshold)
			// fica com a pessoa, mas não entra na média dela: costuma ser um
			// trecho com duas pessoas misturadas, e a média ia aos poucos virando
			// a voz de outra pessoa
			if sim >= sameSpeakerThreshold {
				r.Speakers[best].add(emb, seconds)
			}
			labels[i] = r.Speakers[best].Label
		case best >= 0 && seconds < minNewSpeakerSeconds:
			labels[i] = r.Speakers[best].Label
		default:
			labels[i] = r.newSpeaker(emb, seconds)
		}
	}
	r.mergeSame(labels)
	r.identify(labels)
	return labels
}

// closest devolve a voz da sessão mais parecida com emb e a similaridade, ou
// -1. Uma voz com áudio bastante para ser comparada (seconds) não fica com
// alguém reconhecido de outras reuniões se não lembrar a voz guardada dessa
// pessoa (anchorThreshold).
func (r *Registry) closest(emb []float32, seconds float64) (int, float64) {
	best, bestSim := -1, -2.0
	for j, s := range r.Speakers {
		sim := dot(s.Embedding, emb)
		if sim <= bestSim {
			continue
		}
		if p := r.known(s.Label); p != nil && seconds >= minNewSpeakerSeconds && dot(p.Embedding, emb) < anchorThreshold {
			continue
		}
		best, bestSim = j, sim
	}
	return best, bestSim
}

// known devolve a pessoa conhecida com esse nome, ou nil.
func (r *Registry) known(name string) *Speaker {
	for i := range r.People {
		if r.People[i].Label == name {
			return &r.People[i]
		}
	}
	return nil
}

// newSpeaker guarda uma voz que não é de ninguém da sessão e devolve o rótulo
// dela: o nome de uma pessoa conhecida, se for claramente ela, ou o próximo
// "Pessoa N".
func (r *Registry) newSpeaker(emb []float32, seconds float64) string {
	if name := r.person(emb, seconds); name != "" {
		if k := r.index(name); k >= 0 {
			r.Speakers[k].add(emb, seconds)
			return name
		}
		r.Speakers = append(r.Speakers, Speaker{Label: name, Embedding: emb, Seconds: seconds})
		return name
	}
	label := r.NextLabel()
	r.Speakers = append(r.Speakers, Speaker{Label: label, Embedding: emb, Seconds: seconds})
	return label
}

// mergeSame junta as vozes da sessão que são da mesma pessoa: com o áudio
// acumulado de vários blocos fica claro que duas vozes separadas no começo são
// a mesma pessoa. labels (os rótulos do bloco atual) passa a usar os rótulos
// que ficaram.
func (r *Registry) mergeSame(labels []string) {
	for {
		from, to := r.samePair()
		if from < 0 {
			return
		}
		r.merge(from, to, labels)
	}
}

// merge junta a voz from à voz to e anota a junção em Merged.
func (r *Registry) merge(from, to int, labels []string) {
	f, t := r.Speakers[from].Label, r.Speakers[to].Label
	r.Rename(f, t)
	r.Merged = append(r.Merged, Merge{From: f, To: t})
	for i, l := range labels {
		if l == f {
			labels[i] = t
		}
	}
}

// Consolidate, chamado no fim da sessão, junta cada "Pessoa N" com menos de
// minSpeakerSeconds de áudio à pessoa mais parecida entre as que falaram mais.
// São trechos de fala sobreposta, murmúrio ou ruído que o modelo de voz não
// reconhece como de ninguém: numa conversa de 2 pessoas, sobravam 7 assim, com
// 2 a 28 segundos cada e pouco parecidas com as duas. Alguém que de fato falou
// menos que isso na sessão inteira acaba junto com outra pessoa. Uma sessão em
// que ninguém falou tanto fica como está.
func (r *Registry) Consolidate() {
	for {
		from, to, bestSim := -1, -1, -2.0
		for i, s := range r.Speakers {
			if !r.minor(s) {
				continue
			}
			for j, o := range r.Speakers {
				if sim := dot(s.Embedding, o.Embedding); !r.minor(o) && sim > bestSim {
					from, to, bestSim = i, j, sim
				}
			}
		}
		if from < 0 {
			break
		}
		r.merge(from, to, nil)
	}
	r.identify(nil)
}

// minor diz se a voz tem pouco áudio para ser uma pessoa (ver Consolidate).
// Nomes nunca são juntados a outra pessoa.
func (r *Registry) minor(s Speaker) bool {
	return IsAutoLabel(s.Label) && s.Seconds < minSpeakerSeconds
}

// samePair devolve a voz a juntar (from) e a voz que fica (to), ou -1: a dupla
// mais parecida acima de sameSpeakerThreshold ou, sem ela, uma voz com menos
// áudio que minNewSpeakerSeconds, curta demais para ser outra pessoa, com a
// mais parecida. Fica o rótulo mais antigo, a não ser que o outro seja um nome;
// duas pessoas com nome nunca se juntam, e uma "Pessoa N" só se junta a alguém
// reconhecido de outras reuniões se lembrar a voz guardada (anchorThreshold).
func (r *Registry) samePair() (from, to int) {
	from, to = -1, -1
	best := sameSpeakerThreshold
	for i := range r.Speakers {
		for j := i + 1; j < len(r.Speakers); j++ {
			sim := dot(r.Speakers[i].Embedding, r.Speakers[j].Embedding)
			if sim < best {
				continue
			}
			f, t := j, i
			if !IsAutoLabel(r.Speakers[j].Label) {
				if !IsAutoLabel(r.Speakers[i].Label) {
					continue
				}
				f, t = i, j
			}
			// Quem é reconhecido de outras reuniões só recebe outra voz se ela
			// também lembrar a voz guardada da pessoa
			if p := r.known(r.Speakers[t].Label); p != nil && dot(p.Embedding, r.Speakers[f].Embedding) < anchorThreshold {
				continue
			}
			from, to, best = f, t, sim
		}
	}
	if from >= 0 {
		return from, to
	}

	for i, s := range r.Speakers {
		if s.Seconds >= minNewSpeakerSeconds || !IsAutoLabel(s.Label) {
			continue
		}
		bestSim := -2.0
		for j, o := range r.Speakers {
			if sim := dot(s.Embedding, o.Embedding); j != i && sim > bestSim {
				to, bestSim = j, sim
			}
		}
		if to >= 0 {
			return i, to
		}
	}
	return -1, -1
}

// Resolve devolve o rótulo que label tem agora, depois das vozes juntadas.
func (r *Registry) Resolve(label string) string {
	for _, m := range r.Merged {
		if m.From == label {
			label = m.To
		}
	}
	return label
}

// identify dá o nome de uma pessoa conhecida às "Pessoa N" cuja voz, com o
// áudio acumulado na sessão, é claramente a dela. Decidir só quando a voz
// aparece (ver newSpeaker) deixava sem nome quem começava com uma frase curta
// ou no meio de outra pessoa. Se o nome já está na sessão, as duas vozes viram
// uma. labels (os rótulos do bloco atual, ou nil) acompanha a troca.
func (r *Registry) identify(labels []string) {
	if len(r.People) == 0 {
		return
	}
	for {
		from, name := "", ""
		for _, s := range r.Speakers {
			if !IsAutoLabel(s.Label) {
				continue
			}
			if name = r.person(s.Embedding, s.Seconds); name != "" {
				from = s.Label
				break
			}
		}
		if from == "" {
			return
		}
		r.Rename(from, name)
		r.Merged = append(r.Merged, Merge{From: from, To: name})
		for i, l := range labels {
			if l == from {
				labels[i] = name
			}
		}
	}
}

// person devolve o nome da pessoa conhecida que claramente tem a voz emb, ou "".
func (r *Registry) person(emb []float32, seconds float64) string {
	if seconds < minPersonSeconds {
		return ""
	}
	best, bestSim, second := -1, -1.0, -1.0
	for j, p := range r.People {
		sim := dot(p.Embedding, emb)
		if sim > bestSim {
			best, bestSim, second = j, sim, bestSim
		} else if sim > second {
			second = sim
		}
	}
	if best < 0 || bestSim < personThreshold || bestSim-second < personMargin {
		return ""
	}
	return r.People[best].Label
}

// Rename troca o rótulo from por to (ex: "Pessoa 3" -> "Ana"). Se to já
// existir na sessão, as duas vozes viram uma só. Devolve a voz resultante, ou
// false se from não existir.
func (r *Registry) Rename(from, to string) (Speaker, bool) {
	i := r.index(from)
	if i < 0 || from == to {
		return Speaker{}, false
	}
	if j := r.index(to); j >= 0 {
		r.Speakers[j].add(r.Speakers[i].Embedding, r.Speakers[i].Seconds)
		r.Speakers = append(r.Speakers[:i], r.Speakers[i+1:]...)
		return r.Speakers[r.index(to)], true
	}
	r.Speakers[i].Label = to
	return r.Speakers[i], true
}

// Combine devolve a voz guardada de uma pessoa a partir das vozes que
// receberam o nome dela, uma por reunião (samples): a média das que combinam
// com a mais central, cada uma pesando pelo áudio que tem. Uma voz que não
// combina com as outras (o nome foi dado errado numa reunião) fica de fora.
// Devolve false se não houver voz nenhuma.
func Combine(samples []Speaker) (Speaker, bool) {
	var voices []Speaker
	for _, s := range samples {
		if len(s.Embedding) == 0 || s.Seconds <= 0 || (len(voices) > 0 && len(s.Embedding) != len(voices[0].Embedding)) {
			continue
		}
		voices = append(voices, Speaker{Embedding: normalized(s.Embedding), Seconds: min(s.Seconds, maxRememberedSeconds)})
	}
	if len(voices) == 0 {
		return Speaker{}, false
	}
	// A mais central é a mais parecida com as outras; num empate, a mais nova
	center, best := 0, -1.0
	for i := range voices {
		var score float64
		for j := range voices {
			score += voices[j].Seconds * dot(voices[i].Embedding, voices[j].Embedding)
		}
		if score >= best {
			center, best = i, score
		}
	}
	out := voices[center]
	for i, v := range voices {
		if i != center && dot(voices[center].Embedding, v.Embedding) >= sampleThreshold {
			out.add(v.Embedding, v.Seconds)
		}
	}
	out.Seconds = min(out.Seconds, maxRememberedSeconds)
	return out, true
}

// SameVoice diz se duas vozes, cada uma com o áudio acumulado de uma reunião,
// são claramente da mesma pessoa.
func SameVoice(a, b []float32) bool {
	return len(a) > 0 && len(a) == len(b) && dot(normalized(a), normalized(b)) >= sameSpeakerThreshold
}

// IsAutoLabel diz se o rótulo é um dos automáticos ("Pessoa N"), e não um nome.
func IsAutoLabel(label string) bool {
	var n int
	_, err := fmt.Sscanf(label, "Pessoa %d", &n)
	return err == nil && label == fmt.Sprintf("Pessoa %d", n)
}

func (r *Registry) index(label string) int {
	for i, s := range r.Speakers {
		if s.Label == label {
			return i
		}
	}
	return -1
}

// NextLabel devolve o próximo "Pessoa N" livre. Depois de nomes e junções, o
// número seguinte pode já estar em uso.
func (r *Registry) NextLabel() string {
	for n := len(r.Speakers) + 1; ; n++ {
		if label := fmt.Sprintf("Pessoa %d", n); r.index(label) < 0 {
			return label
		}
	}
}

// add incorpora uma nova amostra da voz à média, ponderada pela duração.
func (s *Speaker) add(emb []float32, seconds float64) {
	total := s.Seconds + seconds
	if total <= 0 || len(emb) != len(s.Embedding) {
		return
	}
	mean := make([]float32, len(emb))
	for i := range mean {
		mean[i] = float32((float64(s.Embedding[i])*s.Seconds + float64(emb[i])*seconds) / total)
	}
	s.Embedding = normalized(mean)
	s.Seconds = total
}

func normalized(v []float32) []float32 {
	norm := math.Sqrt(dot(v, v))
	out := make([]float32, len(v))
	if norm == 0 {
		return out
	}
	for i, x := range v {
		out[i] = float32(float64(x) / norm)
	}
	return out
}

func dot(a, b []float32) float64 {
	if len(a) != len(b) {
		return -1
	}
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}
