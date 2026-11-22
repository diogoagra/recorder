// Package transcript transcreve um trecho de áudio com o Whisper e, ao mesmo
// tempo, identifica quem fala, devolvendo as falas de cada pessoa.
package transcript

import (
	"recorder/internal/diarization"
	"recorder/internal/speakers"
	"recorder/internal/storage"
	"recorder/internal/transcriber"
	"strings"
	"sync"
	"unicode"
)

// Utterance é uma fala contínua de uma mesma pessoa.
type Utterance struct {
	Speaker string // "Pessoa 1", "Pessoa 2"... vazio se não foi possível identificar
	Text    string
	Start   float64 // onde a fala começa no áudio transcrito, em segundos
}

// Transcribe transcreve o WAV e, em paralelo, identifica quem fala
// em cada trecho de samples (o mesmo áudio do WAV). previous é o áudio gravado
// logo antes, usado como contexto, ou nil. As vozes ficam salvas na sessão
// para que a mesma pessoa mantenha o rótulo nos blocos seguintes.
// Devolve nil se o áudio não tiver fala relevante. Quem chama deve segurar
// speakers.Lock até inserir as falas.
func Transcribe(whisper *transcriber.WhisperClient, diarizer *diarization.Diarizer, store *storage.Storage, sessionID, wavPath string, samples, previous []int16) ([]Utterance, error) {
	known := speakers.Load(store, sessionID)

	var diarized []diarization.Segment
	var heard bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		diarized, heard = diarizer.Process(samples, previous, known)
	}()
	segs, err := whisper.TranscribeWav(wavPath, whisperPrompt(store, sessionID))
	wg.Wait()
	if err != nil {
		return nil, err
	}

	if len(diarized) > 0 {
		speakers.Save(store, sessionID, known)
	}
	// Sem o detector de fala (ex: modelos ainda baixando), resta a lista de
	// frases que o Whisper costuma inventar em silêncio
	if !heard && !isRelevantSpeech(transcriber.Text(segs)) {
		return nil, nil
	}
	return groupBySpeaker(segs, diarized, heard), nil
}

// groupBySpeaker atribui cada frase transcrita a quem mais falou nela e junta
// as frases seguidas da mesma pessoa numa fala só. A decisão é por frase, e
// não por palavra, porque as trocas de pessoa quase sempre acontecem entre
// frases e o tempo de cada palavra que o Whisper informa é aproximado.
//
// heard diz que speakers veio do detector de fala: nesse caso as frases em
// que ele não ouviu ninguém são descartadas. Em ruído ou silêncio o Whisper
// inventa frases ("Obrigado por assistir!") com a mesma confiança de uma fala
// real, então o detector é o sinal confiável para separá-las.
func groupBySpeaker(segs []transcriber.Segment, speakers []diarization.Segment, heard bool) []Utterance {
	timed := false
	for _, s := range segs {
		if s.End > 0 {
			timed = true
			break
		}
	}
	// Sem os tempos da transcrição só dá para afirmar quem falou se foi uma pessoa só
	untimedSpeaker := ""
	if !timed && len(speakers) > 0 {
		untimedSpeaker = speakers[0].Speaker
		for _, s := range speakers[1:] {
			if s.Speaker != untimedSpeaker {
				untimedSpeaker = ""
				break
			}
		}
	}

	if heard && !timed && len(speakers) == 0 {
		return nil
	}

	var out []Utterance
	for _, sent := range sentences(segs) {
		if heard && timed && !speechNear(speakers, sent.Start, sent.End) {
			continue
		}
		speaker := untimedSpeaker
		if timed {
			speaker = diarization.SpeakerAt(speakers, sent.Start, sent.End)
		}
		if n := len(out); n > 0 && out[n-1].Speaker == speaker {
			out[n-1].Text += " " + sent.Text
			continue
		}
		out = append(out, Utterance{Speaker: speaker, Text: sent.Text, Start: sent.Start})
	}
	return out
}

// sentences remonta as frases a partir dos trechos do Whisper, que podem ser
// palavras soltas ou várias frases juntas. Quando um trecho tem mais de uma
// frase, o tempo de cada uma é estimado pelo tamanho do texto.
func sentences(segs []transcriber.Segment) []transcriber.Segment {
	var out []transcriber.Segment
	var cur *transcriber.Segment
	depth, spoken := 0, false // marcações abertas; se a frase já tem palavras fora delas
	for _, seg := range segs {
		for _, piece := range splitSentences(seg) {
			if cur == nil {
				cur = &transcriber.Segment{Start: piece.Start, Text: piece.Text}
				spoken = false
			} else {
				cur.Text += " " + piece.Text
			}
			// A frase começa na primeira palavra fora de marcações como [Música]
			var visible string
			visible, depth = outsideMarkers(piece.Text, depth)
			if !spoken && visible != "" {
				cur.Start, spoken = piece.Start, true
			}
			cur.End = piece.End
			if endsSentence(piece.Text) {
				out = append(out, *cur)
				cur = nil
			}
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}

	// Tira as marcações do Whisper, como [Música] ou (risos). Com a transcrição
	// palavra por palavra elas chegam quebradas ("[", "Silence", "]"), então só
	// dá para reconhecê-las depois de remontar a frase.
	//
	// Também descarta frases repetidas: quando entra em loop, o Whisper repete
	// a mesma frase várias vezes. Frases curtas ("Sim.", "Tá bom.") podem se
	// repetir de verdade e ficam.
	kept := out[:0]
	seen := map[string]bool{}
	for _, sent := range out {
		if sent.Text = stripMarkers(sent.Text); sent.Text == "" {
			continue
		}
		// Compara só as palavras, sem pontuação nem maiúsculas
		key := strings.Join(strings.FieldsFunc(strings.ToLower(sent.Text), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
		}), " ")
		if len(strings.Fields(key)) >= 3 {
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		kept = append(kept, sent)
	}
	return kept
}

// stripMarkers remove os trechos entre colchetes ou parênteses.
func stripMarkers(text string) string {
	visible, _ := outsideMarkers(text, 0)
	return visible
}

// outsideMarkers devolve o texto fora de colchetes e parênteses, partindo de
// depth marcações já abertas, e quantas continuam abertas no fim.
func outsideMarkers(text string, depth int) (string, int) {
	var b strings.Builder
	for _, r := range text {
		switch {
		case r == '[' || r == '(':
			depth++
		case (r == ']' || r == ')') && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " "), depth
}

// speechNear diz se o detector ouviu fala no intervalo, com meio segundo de
// folga porque os tempos que o Whisper informa são aproximados.
func speechNear(speakers []diarization.Segment, start, end float64) bool {
	for _, s := range speakers {
		if s.Start < end+0.5 && s.End > start-0.5 {
			return true
		}
	}
	return false
}

// splitSentences divide o trecho depois de cada ".", "?", "!" ou "…" seguido
// de espaço, repartindo o intervalo do trecho proporcionalmente ao texto.
func splitSentences(seg transcriber.Segment) []transcriber.Segment {
	text := []rune(strings.TrimSpace(seg.Text))
	var out []transcriber.Segment
	from := 0
	for i := range text {
		last := i == len(text)-1
		if !last && !(endsSentence(string(text[i])) && unicode.IsSpace(text[i+1])) {
			continue
		}
		piece := strings.TrimSpace(string(text[from : i+1]))
		if piece != "" {
			out = append(out, transcriber.Segment{
				Start: seg.Start + (seg.End-seg.Start)*float64(from)/float64(len(text)),
				End:   seg.Start + (seg.End-seg.Start)*float64(i+1)/float64(len(text)),
				Text:  piece,
			})
		}
		from = i + 1
	}
	return out
}

func endsSentence(text string) bool {
	return strings.HasSuffix(text, ".") || strings.HasSuffix(text, "?") ||
		strings.HasSuffix(text, "!") || strings.HasSuffix(text, "…")
}

func isRelevantSpeech(text string) bool {
	if text == "" {
		return false
	}

	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}

	if isNonSpeechMarker(lower) {
		return false
	}
	hallucinations := []string{
		"[blank_audio]", "obrigado.", "obrigada.", "obrigado", "obrigada",
		"obrigado pela atenção", "obrigada pela atenção", "legendas pela comunidade",
		"legendado por", "amém", "amém.", "até a próxima", "até a próxima.",
		"até mais", "tchau.", "tchau",
	}
	for _, h := range hallucinations {
		if lower == h {
			return false
		}
	}
	return true
}

// isNonSpeechMarker diz se o trecho é só uma marcação do Whisper, como
// [BLANK_AUDIO], [Música] ou (risos), e não uma fala.
func isNonSpeechMarker(text string) bool {
	return (strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]")) ||
		(strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")"))
}
