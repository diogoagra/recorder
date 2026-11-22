package audio

import (
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
)

// stereoWav grava 1s de um tom de 440Hz só no canal direito, a 44,1kHz.
func stereoWav(t *testing.T, path string) {
	const rate = 44100
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	e := wav.NewEncoder(f, rate, 16, 2, 1)
	buf := &audio.IntBuffer{Data: make([]int, 2*rate), Format: &audio.Format{SampleRate: rate, NumChannels: 2}, SourceBitDepth: 16}
	for i := range rate {
		buf.Data[2*i+1] = int(16000 * math.Sin(2*math.Pi*440*float64(i)/rate))
	}
	if err := e.Write(buf); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// checkMixed confere que veio 1s a 16kHz com o tom do canal direito, que
// não pode ter sido descartado.
func checkMixed(t *testing.T, samples []int16) {
	t.Helper()
	if math.Abs(float64(len(samples)-sampleRate)) > 50 {
		t.Fatalf("esperava ~%d amostras, veio %d", sampleRate, len(samples))
	}
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	// O tom tem RMS 16000/√2. Juntando com o canal esquerdo mudo, fica com
	// metade (média dos canais) ou 1/√2 disso (o afconvert preserva a potência)
	rms, tone := math.Sqrt(sum/float64(len(samples))), 16000/math.Sqrt2
	if rms < 0.45*tone || rms > 0.75*tone {
		t.Fatalf("esperava RMS entre %.0f e %.0f (os dois canais juntos), veio %.0f", 0.45*tone, 0.75*tone, rms)
	}
}

func TestDecodeWavJoinsChannelsAndResamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stereo.wav")
	stereoWav(t, path)
	samples, err := Decode(path)
	if err != nil {
		t.Fatal(err)
	}
	checkMixed(t, samples)
}

func TestDecodeCompressedKeepsBothChannels(t *testing.T) {
	if _, err := exec.LookPath("afconvert"); err != nil {
		t.Skip("sem afconvert")
	}
	dir := t.TempDir()
	wavPath, m4a := filepath.Join(dir, "stereo.wav"), filepath.Join(dir, "celular.m4a")
	stereoWav(t, wavPath)
	if out, err := exec.Command("afconvert", "-f", "m4af", "-d", "aac", wavPath, m4a).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	samples, err := Decode(m4a)
	if err != nil {
		t.Fatal(err)
	}
	// O AAC acrescenta um pouco de silêncio no começo
	checkMixed(t, samples[:min(len(samples), sampleRate)])
}

// Um WAV que declara uma taxa absurda no cabeçalho é recusado sem ler o
// áudio: com 1 Hz, cada amostra viraria 16 mil na memória; com bilhões, a
// primeira leitura já pediria gigas.
func TestDecodeRejectsAbsurdWavHeaders(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "bom.wav")
	if err := SaveWav(good, make([]int16, sampleRate), sampleRate); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadAnyWav(good); err != nil {
		t.Fatalf("o WAV de partida deveria ser lido: %v", err)
	}
	// No cabeçalho de 44 bytes: canais em 22-23, taxa em 24-27
	for name, patch := range map[string]func(b []byte){
		"taxa de 1 Hz":         func(b []byte) { binary.LittleEndian.PutUint32(b[24:], 1) },
		"taxa de 4 bilhões":    func(b []byte) { binary.LittleEndian.PutUint32(b[24:], 4000000000) },
		"sessenta mil canais":  func(b []byte) { binary.LittleEndian.PutUint16(b[22:], 60000) },
		"taxa zero e 0 canais": func(b []byte) { binary.LittleEndian.PutUint32(b[24:], 0); binary.LittleEndian.PutUint16(b[22:], 0) },
	} {
		bad := append([]byte(nil), data...)
		patch(bad)
		path := filepath.Join(dir, "ruim.wav")
		if err := os.WriteFile(path, bad, 0644); err != nil {
			t.Fatal(err)
		}
		if samples, err := loadAnyWav(path); err == nil {
			t.Errorf("%s: deveria ser recusado, leu %d amostras", name, len(samples))
		}
	}
}
