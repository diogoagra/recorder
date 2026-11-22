package interpreter

// Enquanto o atalho está apertado, a fala é cortada em frases pelas pausas:
// assim o inglês da primeira frase começa sem esperar você soltar.
//
// Apertar o atalho já diz que você vai falar: nada do que é gravado é
// descartado por "não parecer voz" (quem decide se havia fala é o Whisper).
// Só não vai o que nem som tem: o resto do aperto em silêncio, com um estalo
// (a tecla do atalho, um clique) no meio. Disso o Whisper não devolve
// silêncio: ele inventa uma frase, e ela ia para a reunião na sua voz.
// As pausas são medidas pela sua própria fala durante o aperto, e não pelo
// ruído de antes: com o Meet tocando a voz dos outros pelo alto-falante logo
// antes de apertar, o ruído medido ficava alto e a sua fala nunca contava.
const (
	frameLen     = WhisperRate * 30 / 1000 // 30 ms
	endSilence   = 0.6                     // pausa que fecha uma frase (s)
	earlySilence = 0.3                     // pausa a partir da qual a frase já vai sendo entendida (s)
	minSpeech    = 0.4                     // voz mínima numa frase cortada pela pausa (s)
	minAudible   = 0.15                    // som mínimo numa frase que não foi cortada pela pausa (s)
	maxUtterance = 15.0                    // frase longa demais é cortada (s)
	pauseBelow   = 15.0                    // pausa: dB abaixo da sua fala no aperto
	minVoiceDB   = -60.0                   // mais baixo que isto é silêncio digital
	loudWindow   = 100                     // trechos de 30 ms (3 s) para medir a sua fala
	minDynamics  = 10.0                    // fala varia de volume; ruído constante, não (dB)
)

// A frase cortada pela pausa leva earlySilence de pausa no fim (cut). Com
// earlySilence de pausa, então, o áudio dela já é o que vai ser cortado se a
// pausa chegar a endSilence (ou se o atalho for solto): o Segmenter avisa
// (segEarly) e o Intérprete já manda esse áudio para o Whisper, sem esperar
// a pausa se confirmar. Se a fala volta antes (segResumed), o que foi
// adiantado é descartado.
type segKind int

const (
	segPhrase  segKind = iota // uma frase terminou: audio é ela
	segEarly                  // pausa de earlySilence: audio é a frase, se a pausa se confirmar
	segResumed                // a fala voltou: o áudio adiantado não vale mais
)

type segEvent struct {
	kind  segKind
	audio []int16
}

// earlyFrames é a pausa de earlySilence em trechos de 30 ms.
const earlyFrames = int(earlySilence * 1000 / 30)

// Segmenter recebe o áudio a 16 kHz do aperto e devolve as frases.
type Segmenter struct {
	pending []int16   // resto que não completou 30 ms
	speech  []int16   // a frase em andamento
	recent  []float64 // volume dos últimos trechos, para saber o volume da sua fala
	voiced  int       // trechos com voz na frase em andamento
	audible int       // trechos da frase em andamento acima do silêncio digital
	silent  int       // trechos de pausa seguidos, no fim da frase
	heard   float64   // volume mais alto de todo o aperto (dB)
	hi, lo  float64   // volume mais alto e mais baixo da frase em andamento (dB)
	early   bool      // a frase em andamento já foi adiantada nesta pausa (segEarly)
}

func NewSegmenter() *Segmenter {
	return &Segmenter{heard: -100, hi: -100, lo: 0}
}

// Feed recebe mais áudio e devolve as frases que terminaram nele.
func (s *Segmenter) Feed(samples []int16) [][]int16 {
	var done [][]int16
	for _, ev := range s.feed(samples) {
		if ev.kind == segPhrase {
			done = append(done, ev.audio)
		}
	}
	return done
}

// feed recebe mais áudio e devolve o que aconteceu nele, em ordem: frases que
// terminaram, a frase adiantada por uma pausa e a fala que voltou.
func (s *Segmenter) feed(samples []int16) []segEvent {
	s.pending = append(s.pending, samples...)
	var events []segEvent
	for len(s.pending) >= frameLen {
		frame := s.pending[:frameLen]
		s.pending = s.pending[frameLen:]
		events = s.frame(frame, events)
	}
	return events
}

// loud é o volume da sua fala: o mais alto dos últimos 3 s do aperto.
func (s *Segmenter) loud() float64 {
	l := -100.0
	for _, db := range s.recent {
		l = max(l, db)
	}
	return l
}

func (s *Segmenter) frame(frame []int16, events []segEvent) []segEvent {
	db := levelDB(frame)
	s.heard = max(s.heard, db)
	s.hi, s.lo = max(s.hi, db), min(s.lo, db)
	s.recent = append(s.recent, db)
	if len(s.recent) > loudWindow {
		s.recent = s.recent[1:]
	}
	s.speech = append(s.speech, frame...)
	if db > minVoiceDB {
		s.audible++
	}
	if db > minVoiceDB && db > s.loud()-pauseBelow {
		s.voiced++
		s.silent = 0
		if s.early {
			s.early = false
			events = append(events, segEvent{kind: segResumed})
		}
	} else {
		s.silent++
	}
	seconds := float64(len(s.speech)) / WhisperRate
	pause := float64(s.silent*frameLen)/WhisperRate >= endSilence
	enoughVoice := float64(s.voiced*frameLen)/WhisperRate >= minSpeech
	if pause && enoughVoice {
		return append(events, segEvent{kind: segPhrase, audio: s.cut()})
	}
	if seconds >= maxUtterance {
		// Segurando o atalho sem falar, só o ruído: não vai para o Whisper,
		// que inventaria frases sobre ele
		if !s.hasSpeech() {
			if s.early {
				events = append(events, segEvent{kind: segResumed})
			}
			s.reset()
			return events
		}
		return append(events, segEvent{kind: segPhrase, audio: s.cut()})
	}
	if s.silent == earlyFrames && enoughVoice && !s.early {
		// Com exatamente earlySilence de pausa, a frase inteira até aqui é o
		// que cut devolveria
		s.early = true
		events = append(events, segEvent{kind: segEarly, audio: append([]int16(nil), s.speech...)})
	}
	return events
}

// speechLike diz se a frase em andamento varia de volume como fala (e não é
// só um ruído constante, como o ventilador).
func (s *Segmenter) speechLike() bool {
	return s.hi > minVoiceDB && s.hi-s.lo >= minDynamics
}

// hasSpeech diz se a frase em andamento pode ter fala: varia de volume como
// fala e tem som por minAudible. Um estalo no meio do silêncio varia de
// volume, mas dura um trecho ou dois.
func (s *Segmenter) hasSpeech() bool {
	return s.speechLike() && float64(s.audible*frameLen)/WhisperRate >= minAudible
}

func (s *Segmenter) reset() {
	s.speech, s.voiced, s.silent, s.audible = s.speech[:0], 0, 0, 0
	s.hi, s.lo = -100, 0
	s.early = false
}

// cut fecha a frase em andamento, sem a pausa do fim.
func (s *Segmenter) cut() []int16 {
	end := len(s.speech) - max(s.silent-earlyFrames, 0)*frameLen // deixa earlySilence de pausa
	out := append([]int16(nil), s.speech[:max(end, 0)]...)
	s.reset()
	return out
}

// Flush fecha a frase em andamento ao soltar o atalho: vai tudo o que foi
// gravado desde a última pausa, mesmo baixo (o Whisper decide se havia fala).
// Devolve nil se não sobrou nada parecido com fala: silêncio (mesmo com um
// estalo no meio), ruído constante ou um toque curto demais.
func (s *Segmenter) Flush() []int16 {
	// Solto numa pausa, o resto que não completou 30 ms é silêncio depois do
	// corte: fica de fora, e a frase sai igual à que foi adiantada
	if s.silent < earlyFrames {
		s.speech = append(s.speech, s.pending...)
	}
	s.pending = nil
	if len(s.speech) < int(0.3*WhisperRate) || !s.hasSpeech() {
		s.reset()
		return nil
	}
	return s.cut()
}

// Heard é o volume mais alto ouvido no aperto (dB; -100 se nada).
func (s *Segmenter) Heard() float64 {
	return s.heard
}
