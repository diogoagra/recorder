package interpreter

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
)

// RouterRate é a taxa do áudio que passa pelo roteador (microfone e
// microfone virtual): a do Meet, para a sua voz sair com a qualidade normal.
const RouterRate = 48000

// WhisperRate é a taxa que o Whisper espera.
const WhisperRate = 16000

// wavBytes monta um wav mono de 16 bits.
func wavBytes(samples []int16, rate int) []byte {
	var b bytes.Buffer
	dataLen := uint32(len(samples) * 2)
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, 36+dataLen)
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&b, binary.LittleEndian, uint16(1)) // mono
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(rate*2))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, dataLen)
	binary.Write(&b, binary.LittleEndian, samples)
	return b.Bytes()
}

func writeWav(path string, samples []int16, rate int) error {
	return os.WriteFile(path, wavBytes(samples, rate), 0644)
}

// downsample3 passa de 48 kHz para 16 kHz pela média de cada 3 amostras
// (filtra o que o Whisper não usa e evita o serrilhado).
func downsample3(in []float32) []int16 {
	out := make([]int16, len(in)/3)
	for i := range out {
		s := (in[3*i] + in[3*i+1] + in[3*i+2]) / 3
		out[i] = toInt16(s)
	}
	return out
}

// upsample2 passa de 24 kHz para 48 kHz, interpolando entre as amostras.
// prev é a última amostra do pedaço anterior, para não haver degrau entre eles.
func upsample2(in []int16, prev float32) (out []float32, last float32) {
	out = make([]float32, 2*len(in))
	for i, s := range in {
		cur := float32(s) / 32768
		out[2*i] = (prev + cur) / 2
		out[2*i+1] = cur
		prev = cur
	}
	return out, prev
}

func toInt16(s float32) int16 {
	switch {
	case s >= 1:
		return math.MaxInt16
	case s <= -1:
		return math.MinInt16
	}
	return int16(s * 32767)
}

// levelDB é o volume de um trecho, em dB (0 é o máximo).
func levelDB(samples []int16) float64 {
	if len(samples) == 0 {
		return -100
	}
	var sum float64
	for _, s := range samples {
		f := float64(s) / 32768
		sum += f * f
	}
	return 10 * math.Log10(sum/float64(len(samples))+1e-12)
}
