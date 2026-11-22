package audio

import (
	"math"
	"testing"
)

func constSignal(frames, channels int, v float32) []float32 {
	in := make([]float32, frames*channels)
	for i := range in {
		in[i] = v
	}
	return in
}

func TestResamplerDownsample48k(t *testing.T) {
	r := newResampler(16000)
	out := r.process(constSignal(4800, 1, 0.5), 1, 48000)
	if len(out) != 1600 {
		t.Fatalf("esperava 1600 amostras, veio %d", len(out))
	}
	for _, s := range out {
		if s != 16383 {
			t.Fatalf("esperava 16383, veio %d", s)
		}
	}
}

func TestResamplerStereoDownmix(t *testing.T) {
	r := newResampler(16000)
	in := make([]float32, 0, 32)
	for i := 0; i < 16; i++ {
		in = append(in, 1, 0) // L=1, R=0
	}
	out := r.process(in, 2, 16000)
	if len(out) != 16 || out[0] != 16383 {
		t.Fatalf("downmix incorreto: len=%d out[0]=%d", len(out), out[0])
	}
}

func TestResamplerKeepsRateAcrossChunks(t *testing.T) {
	// 44.1kHz não divide 16kHz: a fração precisa ser carregada entre callbacks
	r := newResampler(16000)
	total := 0
	for i := 0; i < 441; i++ {
		total += len(r.process(constSignal(100, 1, 0.1), 1, 44100))
	}
	if total < 15999 || total > 16001 {
		t.Fatalf("esperava ~16000 amostras em 1s, veio %d", total)
	}
}

func TestResamplerUpsample(t *testing.T) {
	r := newResampler(16000)
	out := r.process(constSignal(800, 1, -0.25), 1, 8000)
	if len(out) != 1600 {
		t.Fatalf("esperava 1600 amostras, veio %d", len(out))
	}
}

func TestMixSystemAudio(t *testing.T) {
	c := &Capture{}
	c.pushSystemAudio([]int16{100, math.MaxInt16, -5})

	mic := []int16{1, 1, 0, 7}
	sys := c.takeSystemAudio(len(mic))
	if len(c.sysBuf) != 0 {
		t.Fatalf("buffer do sistema deveria ter sido consumido, sobrou %d", len(c.sysBuf))
	}
	got := mixWithSystem(mic, sys)
	want := []int16{101, math.MaxInt16, -5, 7}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mix[%d] = %d, esperava %d", i, got[i], want[i])
		}
	}
	if c.takeSystemAudio(4) != nil {
		t.Fatal("sem áudio do sistema no buffer deveria devolver nil")
	}
}

func TestSystemBacklogIsBounded(t *testing.T) {
	c := &Capture{}
	samples := make([]int16, maxSystemBacklog)
	samples[len(samples)-1] = 42
	c.pushSystemAudio(samples)
	c.pushSystemAudio([]int16{1, 2, 3})

	if len(c.sysBuf) != maxSystemBacklog {
		t.Fatalf("backlog = %d, esperava %d", len(c.sysBuf), maxSystemBacklog)
	}
	// As amostras mais antigas são descartadas, as mais novas ficam no fim
	if got := c.sysBuf[len(c.sysBuf)-4:]; got[0] != 42 || got[3] != 3 {
		t.Fatalf("ordem incorreta no fim do buffer: %v", got)
	}
}
