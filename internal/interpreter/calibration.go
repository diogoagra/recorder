package interpreter

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Calibração da voz: o modelo de voz não aprende com gravações; ele imita, na
// hora, um trecho de referência de 10-25 s. O que muda o resultado é qual
// trecho da sua voz serve de referência. Então a calibração grava uns
// minutos, separa os melhores trechos, gera a mesma frase em inglês com cada
// um e você escolhe, de ouvido, a que mais parece com você.

// PreviewText é a frase gerada com cada opção para você comparar.
const PreviewText = "Hi everyone, quick update on the deployment. The tests are passing, so we can release it on Friday. Does anyone have any questions?"

const (
	maxOptions     = 4
	minRefSeconds  = 9.0
	maxRefSeconds  = 20.0
	idealRefSecond = 14.0
)

// CalibrationDir é onde ficam as opções: ~/.recorder/voz/calibracao.
func (v *Voice) CalibrationDir() string {
	return filepath.Join(v.Dir, "calibracao")
}

// VoiceOption é uma voz para você escolher.
type VoiceOption struct {
	ID        string  `json:"id"`        // "atual" ou "opcao-N"
	Label     string  `json:"label"`     // "Voz atual", "Opção 1"...
	Text      string  `json:"text"`      // o que você disse no trecho usado
	Reference string  `json:"reference"` // arquivo do trecho usado (na pasta da calibração)
	Preview   string  `json:"preview"`   // arquivo com a frase de teste nessa voz
	Seconds   float64 `json:"seconds"`   // duração do trecho
}

// segment é uma frase transcrita, com o momento em que foi dita.
type segment struct {
	Start, End float64
	Text       string
}

// transcriptionTimeout é o prazo para o Whisper transcrever a gravação
// inteira da calibração: cresce com ela (até 6 minutos de áudio), e o Whisper
// do app pode estar no meio de um bloco da reunião sendo gravada. Com os 30 s
// de antes, uma gravação longa falhava e era preciso ler tudo de novo.
func transcriptionTimeout(seconds float64) time.Duration {
	return time.Minute + time.Duration(seconds*float64(time.Second))
}

// previewTimeout é o prazo para a voz gerar a frase de teste de uma opção.
const previewTimeout = 2 * time.Minute

// transcribeSegments transcreve a gravação em inglês, com o tempo de cada frase.
func transcribeSegments(ctx context.Context, endpoint string, audio []int16) ([]segment, error) {
	seconds := float64(len(audio)) / WhisperRate
	ctx, cancel := context.WithTimeout(ctx, transcriptionTimeout(seconds))
	defer cancel()
	out, err := whisperRequest(ctx, endpoint, audio, map[string]string{
		"response_format": "verbose_json", "temperature": "0", "language": "en"})
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(out["segments"])
	var segs []segment
	if err := json.Unmarshal(raw, &segs); err != nil {
		return nil, err
	}
	return cleanSegments(segs, seconds), nil
}

// cleanSegments tira os segmentos vazios e os que o Whisper pôs além do fim
// do áudio (ele às vezes inventa frases no silêncio do fim, com tempos que
// não existem na gravação); o que passa do fim é encurtado.
func cleanSegments(segs []segment, seconds float64) []segment {
	var clean []segment
	for _, s := range segs {
		s.Text = strings.Join(strings.Fields(s.Text), " ")
		s.End = math.Min(s.End, seconds)
		if s.Text != "" && s.Start >= 0 && s.End > s.Start {
			clean = append(clean, s)
		}
	}
	return clean
}

// reference é um trecho candidato: frases seguidas da gravação.
type reference struct {
	start, end float64
	text       string
	score      float64
}

// pickReferences escolhe até n trechos de minRefSeconds a maxRefSeconds,
// sem sobreposição. Os trechos são feitos de segmentos inteiros do Whisper (o
// texto precisa bater com o áudio) e os melhores têm bastante fala, poucas
// pausas, começam e terminam em frases inteiras e têm as palavras do texto
// lido (known; uma palavra fora dele é o Whisper ouvindo errado, e o texto
// errado atrapalha a imitação). speech(start, end) diz a fração com voz.
func pickReferences(segs []segment, n int, speech func(start, end float64) float64, known map[string]bool) []reference {
	var cands []reference
	for i := range segs {
		var text []string
		gaps := 0.0
		startsSentence := i == 0 || endsSentence(segs[i-1].Text)
		for j := i; j < len(segs); j++ {
			if j > i {
				gaps += math.Max(0, segs[j].Start-segs[j-1].End-0.6)
			}
			text = append(text, segs[j].Text)
			dur := segs[j].End - segs[i].Start
			if dur > maxRefSeconds {
				break
			}
			if dur < minRefSeconds {
				continue
			}
			joined := joinText(text)
			unknown := unknownWords(joined, known)
			if unknown >= 2 {
				continue // o Whisper ouviu errado: melhor menos opções que uma ruim
			}
			score := speech(segs[i].Start, segs[j].End) - 0.1*gaps - 0.01*math.Abs(dur-idealRefSecond) -
				0.2*float64(unknown)
			if startsSentence {
				score += 0.15
			}
			if endsSentence(segs[j].Text) {
				score += 0.15
			}
			cands = append(cands, reference{segs[i].Start, segs[j].End, joined, score})
		}
	}
	sort.SliceStable(cands, func(a, b int) bool { return cands[a].score > cands[b].score })
	var chosen []reference
	for _, c := range cands {
		if len(chosen) == n {
			break
		}
		overlap := false
		for _, o := range chosen {
			if c.start < o.end+1 && o.start < c.end+1 {
				overlap = true
			}
		}
		if !overlap {
			chosen = append(chosen, c)
		}
	}
	sort.Slice(chosen, func(a, b int) bool { return chosen[a].start < chosen[b].start })
	return chosen
}

func endsSentence(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasSuffix(text, ".") || strings.HasSuffix(text, "?") || strings.HasSuffix(text, "!")
}

// joinText junta os segmentos sem o espaço que o Whisper deixa antes da pontuação.
func joinText(parts []string) string {
	text := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	for _, p := range []string{".", ",", "?", "!"} {
		text = strings.ReplaceAll(text, " "+p, p)
	}
	return text
}

// unknownWords conta as palavras do trecho que não estão no texto lido (0
// sem texto). Números não contam: o Whisper escreve "12" onde se lê "twelve".
func unknownWords(text string, known map[string]bool) int {
	if len(known) == 0 {
		return 0
	}
	n := 0
	for w := range words(text) {
		if !known[w] && !strings.ContainsAny(w, "0123456789") {
			n++
		}
	}
	return n
}

// speechRatio é a fração de trechos de 30 ms com voz entre start e end,
// comparando com o volume da gravação inteira.
func speechRatio(audio []int16) func(start, end float64) float64 {
	const win = WhisperRate * 30 / 1000
	levels := make([]float64, len(audio)/win)
	loudest := -100.0
	for i := range levels {
		levels[i] = levelDB(audio[i*win : (i+1)*win])
		loudest = math.Max(loudest, levels[i])
	}
	return func(start, end float64) float64 {
		a, b := int(start*WhisperRate)/win, min(int(end*WhisperRate)/win, len(levels))
		if b <= a {
			return 0
		}
		voiced := 0
		for _, l := range levels[a:b] {
			if l > loudest-25 {
				voiced++
			}
		}
		return float64(voiced) / float64(b-a)
	}
}

// Calibrate gera as opções de voz a partir da gravação. O servidor de voz
// precisa estar pronto. progress recebe o que está sendo feito. As opções são
// geradas numa pasta à parte, que só toma o lugar da calibração anterior
// quando tudo deu certo: se falhar, as opções de antes continuam lá.
func Calibrate(ctx context.Context, rec Recording, whisperEndpoint string, voice *Voice, progress func(string)) ([]VoiceOption, error) {
	final := voice.CalibrationDir()
	dir := final + ".nova" // dentro da pasta da voz: o servidor só aceita referências de lá
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir) // se não chegou a trocar de lugar

	progress("Transcrevendo a gravação...")
	segs, err := transcribeSegments(ctx, whisperEndpoint, rec.At16k)
	if err != nil {
		return nil, err
	}
	refs := pickReferences(segs, maxOptions, speechRatio(rec.At16k), words(CalibrationText))
	if len(refs) == 0 {
		return nil, fmt.Errorf("não achei trechos bons de %.0f a %.0f s (fala contínua e bem entendida); grave de novo lendo o texto com calma, sem parar muito", minRefSeconds, maxRefSeconds)
	}

	var options []VoiceOption
	if voice.HasSample() {
		current, _ := os.ReadFile(filepath.Join(voice.Dir, "amostra.wav"))
		if err := os.WriteFile(filepath.Join(dir, "atual.wav"), current, 0644); err == nil {
			options = append(options, VoiceOption{ID: "atual", Label: "Voz atual", Text: voice.SampleText(),
				Reference: "atual.wav"})
		}
	}
	n := 0 // opções novas
	for _, r := range refs {
		a, b := referenceSpan(r, len(rec.At24k))
		if float64(b-a)/SpeechRate < minRefSeconds/2 {
			continue // o trecho não está (inteiro) na gravação
		}
		n++
		id := fmt.Sprintf("opcao-%d", n)
		if err := writeWav(filepath.Join(dir, id+".wav"), rec.At24k[a:b], SpeechRate); err != nil {
			return nil, err
		}
		os.WriteFile(filepath.Join(dir, id+".txt"), []byte(r.text+"\n"), 0644)
		options = append(options, VoiceOption{ID: id, Label: fmt.Sprintf("Opção %d", n), Text: r.text,
			Reference: id + ".wav", Seconds: float64(b-a) / SpeechRate})
	}
	if n == 0 {
		return nil, fmt.Errorf("os trechos escolhidos não estão na gravação; grave de novo")
	}

	for k := range options {
		o := &options[k]
		progress(fmt.Sprintf("Gerando a voz %d de %d...", k+1, len(options)))
		var audio []int16
		refWav, refText := "", ""
		if o.ID != "atual" {
			refWav, refText = filepath.Join(dir, o.Reference), o.Text
		}
		speakCtx, cancel := context.WithTimeout(ctx, previewTimeout)
		err := voice.SpeakWith(speakCtx, PreviewText, refWav, refText, func(c []int16) { audio = append(audio, c...) })
		cancel()
		if err != nil {
			return nil, err
		}
		o.Preview = o.ID + "-ingles.wav"
		if err := writeWav(filepath.Join(dir, o.Preview), audio, SpeechRate); err != nil {
			return nil, err
		}
	}
	saved, _ := json.MarshalIndent(options, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "opcoes.json"), saved, 0644); err != nil {
		return nil, err
	}
	// Deu tudo certo: as opções novas tomam o lugar das anteriores
	os.RemoveAll(final)
	if err := os.Rename(dir, final); err != nil {
		return nil, err
	}
	return options, nil
}

// referenceSpan são as amostras (a 24 kHz) do trecho r, com uma folga nas
// pontas, sempre dentro da gravação de total amostras: um tempo do Whisper
// além do fim dela não pode virar um índice fora do áudio.
func referenceSpan(r reference, total int) (a, b int) {
	a = min(max(int((r.start-0.15)*SpeechRate), 0), total)
	b = min(max(int((r.end+0.25)*SpeechRate), a), total)
	return a, b
}

// CalibrationOptions devolve as opções da última calibração (nil se não houver).
func (v *Voice) CalibrationOptions() []VoiceOption {
	b, err := os.ReadFile(filepath.Join(v.CalibrationDir(), "opcoes.json"))
	if err != nil {
		return nil
	}
	var options []VoiceOption
	json.Unmarshal(b, &options)
	return options
}

// ChooseOption passa a usar a opção id como a sua voz. A "atual" é a amostra
// que valia quando a calibração foi feita, guardada com as outras opções:
// escolhê-la depois de ter escolhido outra traz a voz de antes de volta.
func (v *Voice) ChooseOption(id string) error {
	for _, o := range v.CalibrationOptions() {
		if o.ID != id {
			continue
		}
		audio, err := os.ReadFile(filepath.Join(v.CalibrationDir(), o.Reference))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(v.Dir, "amostra.wav"), audio, 0644); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(v.Dir, "amostra.txt"), []byte(o.Text+"\n"), 0644)
	}
	return fmt.Errorf("opção %q não encontrada", id)
}
