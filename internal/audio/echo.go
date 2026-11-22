package audio

import (
	"math"
	"math/cmplx"
)

// Cancelamento de eco. Com alto-falantes, o microfone capta de novo o som do
// computador (ex: as vozes do Meet), que já entra limpo pela captura do
// sistema. Somadas, as duas cópias viram a mesma voz repetida com 50-120ms de
// diferença: um eco. Antes de mixar, estimamos como o som do sistema chega ao
// microfone (atraso + resposta acústica do alto-falante e da sala) e
// subtraímos essa estimativa do microfone.
//
// Como a gravação é processada em blocos de 30s, a estimativa usa o bloco
// inteiro de uma vez. Assim ela não se perde quando alguém fala ao mesmo tempo
// que o som do computador, que é o ponto fraco dos canceladores em tempo real.

const (
	// Maior diferença procurada entre o som do sistema e o eco no microfone
	echoMaxDelay = sampleRate * 6 / 10
	// Janelas usadas para achar o atraso (2s)
	echoDelayWindow = 32768
	// Resposta acústica modelada: echoPre amostras antes do atraso principal
	// (folga para erro na estimativa do atraso) e echoTaps no total (128ms)
	echoPre  = 256
	echoTaps = 2048
	// Quão destacado o pico da correlação precisa ser para haver eco (em desvios padrão)
	echoMinPeak = 8.0
)

// cancelEcho devolve o microfone sem o eco do áudio do sistema (sys, alinhado
// amostra a amostra com mic, com zeros onde não havia áudio do sistema). Se
// não encontrar eco, devolve mic como está.
func cancelEcho(mic, sys []int16) []int16 {
	n := min(len(mic), len(sys))
	if n < echoDelayWindow {
		return mic
	}
	m, s := toFloat64(mic[:n]), toFloat64(sys[:n])
	if rms(s) < 1e-4 {
		return mic // nada tocando no computador
	}

	delay, ok := echoDelay(s, m)
	if !ok {
		return mic // ex: com fones de ouvido o microfone não capta o som do computador
	}
	ref := shifted(s, delay-echoPre)
	echo := convolve(ref, echoResponse(ref, m), n)

	var before, after float64
	clean := make([]int16, len(mic))
	copy(clean, mic)
	for i := 0; i < n; i++ {
		e := m[i] - echo[i]
		before += m[i] * m[i]
		after += e * e
		clean[i] = floatToInt16(e)
	}
	if after >= before {
		return mic // a estimativa não ajudou; melhor não mexer
	}
	return clean
}

// echoDelay acha o atraso do eco no microfone pela correlação cruzada com
// pesos PHAT, que deixa o pico estreito mesmo com fala (sinal muito colorido).
// Devolve false se não houver um pico claro, ou seja, se não houver eco.
func echoDelay(s, m []float64) (int, bool) {
	w := hann(echoDelayWindow)
	cross := make([]complex128, echoDelayWindow)
	used := 0
	for off := 0; off+echoDelayWindow <= len(s); off += echoDelayWindow / 2 {
		if rms(s[off:off+echoDelayWindow]) < 1e-4 {
			continue
		}
		x := spectrum(s[off:off+echoDelayWindow], w, echoDelayWindow)
		y := spectrum(m[off:off+echoDelayWindow], w, echoDelayWindow)
		for k := range cross {
			c := cmplx.Conj(x[k]) * y[k]
			if a := cmplx.Abs(c); a > 0 {
				cross[k] += c / complex(a, 0)
			}
		}
		used++
	}
	if used == 0 {
		return 0, false
	}
	fft(cross, true)

	// r[lag] = correlação entre s[t] e m[t+lag]; lags negativos ficam no fim
	at := func(lag int) float64 {
		if lag < 0 {
			lag += echoDelayWindow
		}
		return real(cross[lag])
	}
	best, bestVal := 0, math.Inf(-1)
	var sum, sumSq float64
	count := 0
	for lag := -echoMaxDelay; lag <= echoMaxDelay; lag++ {
		v := at(lag)
		sum += v
		sumSq += v * v
		count++
		if v > bestVal {
			best, bestVal = lag, v
		}
	}
	mean := sum / float64(count)
	std := math.Sqrt(sumSq/float64(count) - mean*mean)
	return best, std > 0 && (bestVal-mean)/std >= echoMinPeak
}

// echoResponse estima a resposta acústica do alto-falante até o microfone por
// mínimos quadrados: os echoTaps coeficientes h que melhor explicam m a partir
// de ref. Isso é resolver R h = p, onde R é a autocorrelação de ref (uma
// matriz de Toeplitz) e p a correlação cruzada entre ref e m.
func echoResponse(ref, m []float64) []float64 {
	size := 1
	for size < len(ref)+echoTaps {
		size <<= 1
	}
	x := spectrum(ref, nil, size)
	y := spectrum(m, nil, size)
	auto := make([]complex128, size)
	cross := make([]complex128, size)
	for k := range x {
		auto[k] = complex(real(x[k])*real(x[k])+imag(x[k])*imag(x[k]), 0)
		cross[k] = cmplx.Conj(x[k]) * y[k]
	}
	fft(auto, true)
	fft(cross, true)

	r := make([]float64, echoTaps)
	p := make([]float64, echoTaps)
	for i := range r {
		r[i] = real(auto[i])
		p[i] = real(cross[i])
	}
	// Uma pequena regularização evita que frequências em que o sistema quase
	// não tem som (ex: agudos cortados pelo codec do Meet) gerem ruído no filtro
	r[0] *= 1.001
	return solveToeplitz(r, p)
}

// solveToeplitz resolve T x = b, onde T é a matriz de Toeplitz simétrica cuja
// primeira linha é r, pelo algoritmo de Levinson (Golub & Van Loan, 4.7.2).
// Devolve zeros se o sistema for mal condicionado.
func solveToeplitz(r, b []float64) []float64 {
	n := len(r)
	x := make([]float64, n)
	if r[0] <= 0 {
		return x
	}
	// Normaliza para a diagonal ser 1
	t := make([]float64, n+1)
	for i := 1; i < n; i++ {
		t[i] = r[i] / r[0]
	}
	y := make([]float64, n)
	tmp := make([]float64, n)

	x[0] = b[0] / r[0]
	y[0] = -t[1]
	beta, alpha := 1.0, -t[1]
	for k := 1; k < n; k++ {
		beta *= 1 - alpha*alpha
		if beta <= 1e-12 {
			return make([]float64, n)
		}
		var dot float64
		for i := 1; i <= k; i++ {
			dot += t[i] * x[k-i]
		}
		mu := (b[k]/r[0] - dot) / beta
		for i := 0; i < k; i++ {
			tmp[i] = x[i] + mu*y[k-1-i]
		}
		copy(x, tmp[:k])
		x[k] = mu

		if k < n-1 {
			dot = 0
			for i := 1; i <= k; i++ {
				dot += t[i] * y[k-i]
			}
			alpha = -(t[k+1] + dot) / beta
			for i := 0; i < k; i++ {
				tmp[i] = y[i] + alpha*y[k-1-i]
			}
			copy(y, tmp[:k])
			y[k] = alpha
		}
	}
	return x
}

// convolve filtra x pela resposta h (overlap-add) e devolve as n primeiras amostras.
func convolve(x, h []float64, n int) []float64 {
	size := 1
	for size < 2*len(h) {
		size <<= 1
	}
	block := size - len(h) + 1
	hf := spectrum(h, nil, size)
	out := make([]float64, n+size)
	for off := 0; off < n; off += block {
		xf := spectrum(x[off:min(off+block, len(x))], nil, size)
		for k := range xf {
			xf[k] *= hf[k]
		}
		fft(xf, true)
		for i := range xf {
			out[off+i] += real(xf[i])
		}
	}
	return out[:n]
}

// shifted devolve x atrasado em d amostras (adiantado se d < 0), com zeros nas pontas.
func shifted(x []float64, d int) []float64 {
	out := make([]float64, len(x))
	for i := range out {
		if j := i - d; j >= 0 && j < len(x) {
			out[i] = x[j]
		}
	}
	return out
}

func toFloat64(x []int16) []float64 {
	out := make([]float64, len(x))
	for i, v := range x {
		out[i] = float64(v) / 32768
	}
	return out
}

func rms(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	var sum float64
	for _, v := range x {
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(x)))
}
