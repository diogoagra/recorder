package audio

// resampler converte áudio float32 (qualquer taxa e número de canais) para
// mono int16 na taxa de saída. Cada amostra de saída é a média das amostras de
// entrada da sua janela (filtro box), o que atenua o aliasing ao reduzir a taxa.
// Guarda estado entre chamadas para não criar descontinuidades entre callbacks.
type resampler struct {
	outRate float64
	pos     float64 // amostras de entrada acumuladas na janela atual
	acc     float64
	n       int
}

func newResampler(outRate float64) *resampler {
	return &resampler{outRate: outRate}
}

func (r *resampler) process(in []float32, channels int, inRate float64) []int16 {
	if channels < 1 || inRate <= 0 {
		return nil
	}
	step := inRate / r.outRate
	frames := len(in) / channels
	out := make([]int16, 0, int(float64(frames)/step)+1)

	for f := 0; f < frames; f++ {
		var v float64
		for ch := 0; ch < channels; ch++ {
			v += float64(in[f*channels+ch])
		}
		r.acc += v / float64(channels)
		r.n++
		r.pos++

		if r.pos >= step {
			avg := r.acc / float64(r.n)
			for r.pos >= step {
				out = append(out, floatToInt16(avg))
				r.pos -= step
			}
			r.acc, r.n = 0, 0
		}
	}
	return out
}

func floatToInt16(v float64) int16 {
	if v > 1 {
		v = 1
	} else if v < -1 {
		v = -1
	}
	return int16(v * 32767)
}
