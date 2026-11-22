package audio

import (
	"math"
	"math/rand"
	"testing"
)

// fakeSpeech gera um sinal parecido com fala: ruído com cor e sílabas (~4 por
// segundo) separadas por pausas.
func fakeSpeech(rng *rand.Rand, seconds float64) []float64 {
	x := make([]float64, int(seconds*sampleRate))
	var lp float64
	for i := range x {
		lp = 0.9*lp + 0.1*rng.NormFloat64()
		syllable := math.Max(0, math.Sin(2*math.Pi*4*float64(i)/sampleRate))
		x[i] = lp * syllable * 0.5
	}
	return x
}

func toInt16(x []float64) []int16 {
	out := make([]int16, len(x))
	for i, v := range x {
		out[i] = floatToInt16(v)
	}
	return out
}

func TestCancelEchoRemovesSpeakerEcho(t *testing.T) {
	// Um bloco de 30s em que a pessoa fala no terço do meio, por cima do som
	// do computador
	rng := rand.New(rand.NewSource(1))
	sys := fakeSpeech(rng, 30)
	user := fakeSpeech(rng, 30)
	for i := range user {
		if i < len(user)/3 || i > 2*len(user)/3 {
			user[i] = 0
		}
	}

	// Alto-falante -> microfone: 50ms de atraso, som direto e um reflexo
	echo := make([]float64, len(sys))
	for i := range echo {
		if j := i - 800; j >= 0 {
			echo[i] += 0.5 * sys[j]
		}
		if j := i - 1100; j >= 0 {
			echo[i] -= 0.2 * sys[j]
		}
	}
	mic := make([]float64, len(sys))
	for i := range mic {
		mic[i] = user[i] + echo[i]
	}

	clean := toFloat64(cancelEcho(toInt16(mic), toInt16(sys)))

	var echoEnergy, residual float64
	for i := range clean {
		echoEnergy += echo[i] * echo[i]
		r := clean[i] - user[i]
		residual += r * r
	}
	if removed := 10 * math.Log10(echoEnergy/residual); removed < 20 {
		t.Fatalf("esperava remover pelo menos 20dB de eco, removeu %.1fdB", removed)
	}
}

func TestCancelEchoKeepsMicWithoutEcho(t *testing.T) {
	// Com fones de ouvido o microfone não capta o som do computador
	rng := rand.New(rand.NewSource(2))
	mic := toInt16(fakeSpeech(rng, 10))
	sys := toInt16(fakeSpeech(rng, 10))

	clean := cancelEcho(mic, sys)
	for i := range mic {
		if clean[i] != mic[i] {
			t.Fatalf("sem eco o microfone não deveria mudar (amostra %d)", i)
		}
	}
}

func TestSolveToeplitz(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	sig := make([]float64, 4000)
	for i := range sig {
		sig[i] = rng.NormFloat64()
		if i > 0 {
			sig[i] += 0.9 * sig[i-1]
		}
	}
	n := 64
	r := make([]float64, n)
	for lag := range r {
		for i := lag; i < len(sig); i++ {
			r[lag] += sig[i] * sig[i-lag]
		}
	}
	want := make([]float64, n)
	for i := range want {
		want[i] = rng.NormFloat64()
	}
	b := make([]float64, n)
	for i := range b {
		for j := range want {
			d := i - j
			if d < 0 {
				d = -d
			}
			b[i] += r[d] * want[j]
		}
	}
	got := solveToeplitz(r, b)
	for i := range got {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Fatalf("x[%d] = %v, esperava %v", i, got[i], want[i])
		}
	}
}
