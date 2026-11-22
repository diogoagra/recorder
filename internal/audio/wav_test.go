package audio

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestSaveWavRoundTrip(t *testing.T) {
	samples := speechWithPauses(3, nil)
	samples[0], samples[1] = -32768, 32767
	path := filepath.Join(t.TempDir(), "a.wav")
	if err := SaveWav(path, samples, sampleRate); err != nil {
		t.Fatal(err)
	}
	got, err := LoadWav(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, samples) {
		t.Fatalf("LoadWav devolveu %d amostras diferentes das %d gravadas", len(got), len(samples))
	}
}

// Salvar 1h de áudio pelo codificador do go-audio alocava 780 MB; escrevendo
// direto, só o buffer de escrita.
func TestSaveWavDoesNotCopyTheAudioInMemory(t *testing.T) {
	samples := make([]int16, 10*60*sampleRate)
	path := filepath.Join(t.TempDir(), "longo.wav")
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := SaveWav(path, samples, sampleRate); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4<<20 {
		t.Fatalf("SaveWav alocou %d MB para gravar %d MB de áudio", allocated>>20, len(samples)*2>>20)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != int64(wavHeaderSize+len(samples)*2) {
		t.Fatalf("arquivo com %d bytes, esperava %d (%v)", info.Size(), wavHeaderSize+len(samples)*2, err)
	}
}

func TestRawToWavKeepsTheAudio(t *testing.T) {
	dir := t.TempDir()
	samples := speechWithPauses(2, nil)
	raw := filepath.Join(dir, "a.pcm")
	if err := AppendRaw(raw, samples); err != nil {
		t.Fatal(err)
	}
	// Uma escrita interrompida pode deixar meia amostra no fim
	f, _ := os.OpenFile(raw, os.O_APPEND|os.O_WRONLY, 0644)
	f.Write([]byte{7})
	f.Close()

	path := filepath.Join(dir, "a.wav")
	if err := rawToWav(raw, path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadWav(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, samples) {
		t.Fatalf("o WAV tem %d amostras, esperava as %d do áudio bruto", len(got), len(samples))
	}
}

func TestConcatWavJoinsInOrder(t *testing.T) {
	dir := t.TempDir()
	a, b := speechWithPauses(2, nil), make([]int16, sampleRate)
	for i := range b {
		b[i] = int16(i)
	}
	SaveWav(filepath.Join(dir, "a.wav"), a, sampleRate)
	SaveWav(filepath.Join(dir, "b.wav"), b, sampleRate)
	out := filepath.Join(dir, "ab.wav")
	if err := concatWav([]string{filepath.Join(dir, "a.wav"), filepath.Join(dir, "b.wav")}, out); err != nil {
		t.Fatal(err)
	}
	got, err := LoadWav(out)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, append(slices.Clone(a), b...)) {
		t.Fatalf("junção com %d amostras, esperava %d (a seguido de b)", len(got), len(a)+len(b))
	}

	// Um WAV em outro formato não pode ser juntado como se fosse 16kHz mono
	other := filepath.Join(dir, "outro.wav")
	header, _ := wavHeader(4, 44100)
	os.WriteFile(other, append(header, 0, 0, 0, 0), 0644)
	if err := concatWav([]string{filepath.Join(dir, "a.wav"), other}, filepath.Join(dir, "x.wav")); err == nil {
		t.Fatal("juntar um WAV de 44,1kHz deveria falhar")
	}
}
