package audio

import (
	"context"
	"math/rand"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// speechWithPauses gera seconds de "fala" alta com silêncio nos intervalos dados.
func speechWithPauses(seconds float64, pauses [][2]float64) []int16 {
	rng := rand.New(rand.NewSource(1))
	out := make([]int16, int(seconds*sampleRate))
	for i := range out {
		t := float64(i) / sampleRate
		v := rng.NormFloat64() * 3000
		for _, p := range pauses {
			if t >= p[0] && t < p[1] {
				v = rng.NormFloat64() * 20 // ruído de fundo
			}
		}
		out[i] = clampInt16(int32(v))
	}
	return out
}

// feed entrega o áudio para a captura como o callback do microfone faria
func feed(c *Capture, samples []int16) {
	for i := 0; i < len(samples); i += 160 {
		c.ch <- frame{mic: samples[i:min(i+160, len(samples))]}
	}
}

func TestRecordBlockCutsAtPauseWithoutLosingAudio(t *testing.T) {
	input := speechWithPauses(70, [][2]float64{{27, 27.6}, {58, 58.5}})
	c := &Capture{ch: make(chan frame, len(input)/160+1), isStarted: true}
	feed(c, input)
	dir := t.TempDir()

	first, err := c.RecordBlock(context.Background(), 30*time.Second, filepath.Join(dir, "1.wav"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.RecordBlock(context.Background(), 30*time.Second, filepath.Join(dir, "2.wav"))
	if err != nil {
		t.Fatal(err)
	}

	// Corta no meio das pausas
	if got := float64(len(first)) / sampleRate; got < 27 || got > 27.6 {
		t.Fatalf("primeiro corte em %.2fs, esperava dentro da pausa (27-27,6s)", got)
	}
	if end := float64(len(first)+len(second)) / sampleRate; end < 58 || end > 58.5 {
		t.Fatalf("segundo corte em %.2fs, esperava dentro da pausa (58-58,5s)", end)
	}
	// Os blocos emendados são exatamente o áudio de entrada, sem lacunas
	joined := append(append([]int16{}, first...), second...)
	if !reflect.DeepEqual(joined, input[:len(joined)]) {
		t.Fatal("os blocos emendados não batem com o áudio gravado")
	}
}

func TestRecordBlockWithoutPauseCutsAtQuietestPoint(t *testing.T) {
	// Fala contínua, só um trecho mais baixo (não silencioso) em 33s
	input := speechWithPauses(40, nil)
	for i := 33 * sampleRate; i < 33*sampleRate+sampleRate/2; i++ {
		input[i] /= 4
	}
	c := &Capture{ch: make(chan frame, len(input)/160+1), isStarted: true}
	feed(c, input)

	block, err := c.RecordBlock(context.Background(), 30*time.Second, filepath.Join(t.TempDir(), "1.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if got := float64(len(block)) / sampleRate; got < 33 || got > 33.5 {
		t.Fatalf("corte em %.2fs, esperava no trecho mais baixo (33-33,5s)", got)
	}
	// O resto fica para o próximo bloco: parte já lida, parte ainda no buffer
	rest := append([]int16{}, c.pendingMic...)
	for len(c.ch) > 0 {
		rest = append(rest, (<-c.ch).mic...)
	}
	if !reflect.DeepEqual(append(append([]int16{}, block...), rest...), input) {
		t.Fatal("o áudio depois do corte deveria ficar inteiro para o próximo bloco")
	}
}

func TestSplitBlocksCutsRecordedAudioAtPauses(t *testing.T) {
	input := speechWithPauses(100, [][2]float64{{28, 28.6}, {57, 57.5}, {90, 90.4}})
	blocks := SplitBlocks(input, 30*time.Second)

	var joined []int16
	var cuts []float64
	for _, b := range blocks {
		if len(b) > 35*sampleRate {
			t.Fatalf("bloco de %.1fs, acima do máximo de 35s", float64(len(b))/sampleRate)
		}
		joined = append(joined, b...)
		cuts = append(cuts, float64(len(joined))/sampleRate)
	}
	if !reflect.DeepEqual(joined, input) {
		t.Fatal("os blocos emendados não batem com o áudio original")
	}
	// Cortes nas três pausas e o resto no último bloco
	want := [][2]float64{{28, 28.6}, {57, 57.5}, {90, 90.4}}
	if len(cuts) != 4 {
		t.Fatalf("esperava 4 blocos, veio %d (cortes em %v)", len(cuts), cuts)
	}
	for i, w := range want {
		if cuts[i] < w[0] || cuts[i] > w[1] {
			t.Fatalf("corte %d em %.2fs, esperava dentro da pausa %v", i, cuts[i], w)
		}
	}
}
