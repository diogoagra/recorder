package audio

import (
	"math"
	"math/cmplx"
)

// fft calcula a transformada de Fourier de a no próprio slice (len(a) precisa
// ser potência de 2). Com inverse, calcula a inversa já dividida por len(a).
func fft(a []complex128, inverse bool) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		angle := -2 * math.Pi / float64(size)
		if inverse {
			angle = -angle
		}
		step := cmplx.Rect(1, angle)
		for start := 0; start < n; start += size {
			w := complex(1, 0)
			for k := 0; k < size/2; k++ {
				u, v := a[start+k], a[start+k+size/2]*w
				a[start+k], a[start+k+size/2] = u+v, u-v
				w *= step
			}
		}
	}
	if inverse {
		for i := range a {
			a[i] /= complex(float64(n), 0)
		}
	}
}

// spectrum devolve a FFT de x (com zeros até completar n), multiplicado pela
// janela w se ela não for nil.
func spectrum(x, w []float64, n int) []complex128 {
	a := make([]complex128, n)
	for i := 0; i < len(x) && i < n; i++ {
		v := x[i]
		if w != nil {
			v *= w[i]
		}
		a[i] = complex(v, 0)
	}
	fft(a, false)
	return a
}

func hann(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n))
	}
	return w
}
