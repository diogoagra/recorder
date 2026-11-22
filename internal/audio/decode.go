package audio

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
)

// Decode lê um arquivo de áudio gravado fora do app (ex: no celular) como
// amostras mono de 16kHz, juntando os canais. Lê WAV direto; os outros
// formatos (m4a, mp3, aac, ogg/opus, flac, amr, vídeos mp4/mov...) passam pelo
// afconvert no macOS e, no Windows ou no que o afconvert não ler, pelo ffmpeg,
// se estiver instalado.
func Decode(path string) ([]int16, error) {
	if strings.EqualFold(filepath.Ext(path), ".wav") {
		if samples, err := loadAnyWav(path); err == nil {
			return samples, nil
		}
	}

	tmp, err := os.CreateTemp("", "recorder-import-*.wav")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	var failures []string
	if runtime.GOOS == "darwin" {
		// Sem o "--mix", o "-c 1" fica só com o primeiro canal, e num áudio com
		// uma pessoa em cada canal a outra sumiria
		out, err := exec.Command("afconvert", "-f", "WAVE", "-d", "LEI16@16000", "-c", "1", "--mix", path, tmp.Name()).CombinedOutput()
		if err == nil {
			return LoadWav(tmp.Name())
		}
		failures = append(failures, fmt.Sprintf("afconvert: %v: %s", err, strings.TrimSpace(string(out))))
	}
	if ffmpeg := findFFmpeg(); ffmpeg != "" {
		cmd := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-y", "-i", path,
			"-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-f", "wav", tmp.Name())
		hideWindow(cmd)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return LoadWav(tmp.Name())
		}
		failures = append(failures, fmt.Sprintf("ffmpeg: %v: %s", err, strings.TrimSpace(string(out))))
	}
	if len(failures) == 0 {
		return nil, fmt.Errorf("formato não suportado: %s (converta para WAV ou instale o ffmpeg)", filepath.Base(path))
	}
	return nil, fmt.Errorf("não foi possível ler %s: %s", filepath.Base(path), strings.Join(failures, "; "))
}

// Limites do que um WAV importado pode declarar no cabeçalho.
const (
	minWavRate     = 4000
	maxWavRate     = 384000
	maxWavChannels = 16
)

// loadAnyWav lê um WAV PCM inteiro (8 a 32 bits) de qualquer taxa e número de
// canais como mono de 16kHz.
func loadAnyWav(path string) ([]int16, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	d := wav.NewDecoder(f)
	if !d.IsValidFile() {
		return nil, fmt.Errorf("arquivo WAV inválido: %s", path)
	}
	channels := int(d.NumChans)
	// A taxa e o número de canais vêm do cabeçalho de um arquivo que pode ter
	// sido feito para isso: com 1 Hz, cada amostra viraria 16 mil na memória;
	// com bilhões, o primeiro pedaço lido já pediria gigas
	if d.WavAudioFormat != 1 || d.BitDepth < 8 || d.BitDepth > 32 || channels < 1 || channels > maxWavChannels ||
		d.SampleRate < minWavRate || d.SampleRate > maxWavRate {
		return nil, fmt.Errorf("formato de WAV não suportado em %s", path)
	}
	// O go-audio devolve o WAV de 8 bits sem sinal (0 a 255)
	scale, center := float32(int64(1)<<(d.BitDepth-1)), float32(0)
	if d.BitDepth == 8 {
		center = 128
	}

	// Em pedaços de 1s, como LoadWav, para um arquivo de horas não ocupar gigas
	r := newResampler(sampleRate)
	var samples []int16
	buf := &audio.IntBuffer{Data: make([]int, int(d.SampleRate)*channels), Format: d.Format()}
	chunk := make([]float32, len(buf.Data))
	for {
		n, err := d.PCMBuffer(buf)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return samples, nil
		}
		for i, s := range buf.Data[:n] {
			chunk[i] = (float32(s) - center) / scale
		}
		samples = append(samples, r.process(chunk[:n], channels, float64(d.SampleRate))...)
	}
}

// findFFmpeg procura o ffmpeg no PATH e, como o app aberto pelo Finder não
// recebe o PATH do terminal, também onde o Homebrew o instala.
func findFFmpeg() string {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		// Ali estes caminhos seriam C:\opt\homebrew\bin e C:\usr\local\bin,
		// pastas que qualquer usuário do computador consegue criar
		return ""
	}
	for _, p := range []string{"/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
