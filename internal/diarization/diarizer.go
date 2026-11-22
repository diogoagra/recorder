// Package diarization identifica quem está falando no áudio ("Pessoa 1",
// "Pessoa 2"...) usando o sherpa-onnx, localmente e sem Python.
package diarization

import (
	"fmt"
	"log"
	"runtime"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

const (
	sampleRate = 16000
	// Tamanho dos blocos analisados de cada vez, o mesmo da gravação ao vivo
	blockSeconds = 30
	// Áudio anterior ao bloco analisado junto com ele, para dar contexto
	contextSeconds = 10
	// Áudio máximo de cada pessoa usado para calcular a "impressão digital" da voz
	maxEmbedSeconds = 30.0
)

type Diarizer struct {
	mu sync.Mutex // o sherpa-onnx não garante uso concorrente do mesmo objeto
	sd *sherpa.OfflineSpeakerDiarization
	ex *sherpa.SpeakerEmbeddingExtractor
}

// Load prepara o diarizador em segundo plano, baixando os modelos para
// modelsDir na primeira vez. Enquanto não estiver pronto (ou se falhar),
// Process não identifica ninguém e a transcrição segue sem locutores.
func Load(modelsDir string) *Diarizer {
	d := &Diarizer{}
	go func() {
		if err := d.init(modelsDir); err != nil {
			log.Printf("Aviso: identificação de locutores desativada: %v", err)
			return
		}
		log.Println("-> Identificação de locutores pronta.")
	}()
	return d
}

func (d *Diarizer) init(modelsDir string) error {
	segmentation, err := segmentationModel.ensure(modelsDir)
	if err != nil {
		return err
	}
	embedding, err := embeddingModel.ensure(modelsDir)
	if err != nil {
		return err
	}

	threads := min(max(runtime.NumCPU()/2, 1), 4)
	embeddingConfig := sherpa.SpeakerEmbeddingExtractorConfig{
		Model:      embedding,
		NumThreads: threads,
		Provider:   "cpu",
	}

	config := sherpa.OfflineSpeakerDiarizationConfig{}
	config.Segmentation.Pyannote.Model = segmentation
	config.Segmentation.NumThreads = threads
	config.Segmentation.Provider = "cpu"
	config.Embedding = embeddingConfig
	// O número de pessoas não é conhecido, então o agrupamento usa o limiar
	config.Clustering.Threshold = 0.5
	config.MinDurationOn = 0.3
	config.MinDurationOff = 0.5

	sd := sherpa.NewOfflineSpeakerDiarization(&config)
	if sd == nil {
		return fmt.Errorf("falha ao carregar os modelos de segmentação/embedding")
	}
	ex := sherpa.NewSpeakerEmbeddingExtractor(&embeddingConfig)
	if ex == nil {
		sherpa.DeleteOfflineSpeakerDiarization(sd)
		return fmt.Errorf("falha ao carregar o modelo de embedding %s", embedding)
	}

	d.mu.Lock()
	d.sd, d.ex = sd, ex
	d.mu.Unlock()
	return nil
}

// Process diz quem fala em cada trecho do áudio (mono, 16kHz). As vozes são
// casadas com as que já falaram na sessão (known), que recebe as vozes novas;
// assim a "Pessoa 1" de um bloco continua sendo a "Pessoa 1" no seguinte.
// context é o áudio gravado logo antes de samples (ex: o bloco anterior), ou
// nil. ready é false enquanto o diarizador não estiver pronto (ex: baixando os
// modelos); pronto e sem trechos quer dizer que ninguém falou.
//
// Áudios longos (ex: reprocessar uma sessão) são analisados em blocos de 30s,
// como na gravação ao vivo. Nos testes isso identificou as pessoas tão bem
// quanto analisar o arquivo inteiro de uma vez, que separa demais a mesma voz.
func (d *Diarizer) Process(samples, context []int16, known *Registry) (segs []Segment, ready bool) {
	if d == nil {
		return nil, false
	}
	d.mu.Lock()
	ready = d.sd != nil
	d.mu.Unlock()
	if !ready {
		return nil, false
	}

	for off := 0; off < len(samples); off += blockSeconds * sampleRate {
		end := min(off+blockSeconds*sampleRate, len(samples))

		// Os segundos anteriores ao bloco entram junto na análise e depois são
		// descartados: sem eles, quem estava falando na virada do bloco vira um
		// trecho curto e solto, fácil de confundir com outra pessoa
		before := context
		if off > 0 {
			before = samples[:off]
		}
		before = before[max(len(before)-contextSeconds*sampleRate, 0):]
		window := make([]int16, 0, len(before)+end-off)
		window = append(append(window, before...), samples[off:end]...)

		shift := float64(off-len(before)) / sampleRate
		blockStart := float64(off) / sampleRate
		for _, s := range d.processBlock(window, known) {
			s.Start += shift
			s.End += shift
			if s.End <= blockStart {
				continue
			}
			s.Start = max(s.Start, blockStart)
			segs = append(segs, s)
		}
	}
	// Um bloco seguinte pode ter juntado a voz de um anterior a outra
	for i := range segs {
		segs[i].Speaker = known.Resolve(segs[i].Speaker)
	}
	return segs, true
}

func (d *Diarizer) processBlock(samples []int16, known *Registry) []Segment {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sd == nil || len(samples) == 0 {
		return nil
	}

	audio := make([]float32, len(samples))
	for i, s := range samples {
		audio[i] = float32(s) / 32768
	}

	raw := d.sd.Process(audio)

	// Agrupa os trechos de cada locutor do bloco, na ordem em que cada um
	// falou pela primeira vez, para numerar as pessoas novas nessa ordem
	var order []int
	bySpeaker := map[int][]sherpa.OfflineSpeakerDiarizationSegment{}
	for _, s := range raw {
		if _, ok := bySpeaker[s.Speaker]; !ok {
			order = append(order, s.Speaker)
		}
		bySpeaker[s.Speaker] = append(bySpeaker[s.Speaker], s)
	}

	voices := make([]Voice, len(order))
	for i, local := range order {
		voices[i].Embedding, voices[i].Seconds = d.embed(audio, bySpeaker[local])
	}
	matched := known.MatchBlock(voices)

	labels := map[int]string{}
	for i, local := range order {
		labels[local] = matched[i]
	}
	segs := make([]Segment, len(raw))
	for i, s := range raw {
		segs[i] = Segment{Start: float64(s.Start), End: float64(s.End), Speaker: labels[s.Speaker]}
	}
	return segs
}

// embed calcula a "impressão digital" da voz a partir dos trechos de um
// locutor e devolve também quantos segundos de áudio foram usados.
func (d *Diarizer) embed(audio []float32, segs []sherpa.OfflineSpeakerDiarizationSegment) ([]float32, float64) {
	stream := d.ex.CreateStream()
	defer sherpa.DeleteOnlineStream(stream)

	var seconds float64
	for _, s := range segs {
		if seconds >= maxEmbedSeconds {
			break
		}
		start := min(max(int(s.Start*sampleRate), 0), len(audio))
		end := min(max(int(s.End*sampleRate), start), len(audio))
		if end > start {
			stream.AcceptWaveform(sampleRate, audio[start:end])
			seconds += float64(end-start) / sampleRate
		}
	}
	stream.InputFinished()

	if !d.ex.IsReady(stream) {
		return nil, seconds
	}
	return d.ex.Compute(stream), seconds
}
