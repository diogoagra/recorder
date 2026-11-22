package audio

import (
	"bytes"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestAppendSessionKeepsAudioWhenResuming(t *testing.T) {
	if _, err := exec.LookPath("afconvert"); err != nil && runtime.GOOS != "windows" {
		t.Skip("afconvert indisponível")
	}
	dir := t.TempDir()
	// Primeira gravação, pausa e depois mais um trecho
	if _, err := AppendSession(dir, "sessao", make([]int16, 20*16000)); err != nil {
		t.Fatal(err)
	}
	if got := SessionDuration(dir, "sessao"); math.Abs(got-20) > 0.2 {
		t.Fatalf("depois da primeira parte: %.2fs, esperava 20s", got)
	}
	if _, err := AppendSession(dir, "sessao", make([]int16, 10*16000)); err != nil {
		t.Fatal(err)
	}
	if got := SessionDuration(dir, "sessao"); math.Abs(got-30) > 0.3 {
		t.Fatalf("depois de retomar: %.2fs, esperava 30s (a primeira parte não pode se perder)", got)
	}
	samples, err := LoadFile(SessionFile(dir, "sessao"))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(len(samples))/16000-30) > 0.3 {
		t.Fatalf("áudio lido com %.2fs, esperava 30s", float64(len(samples))/16000)
	}
}

// mdat devolve os pacotes de áudio comprimido de um m4a/mp4.
func mdat(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, _ := f.Stat()
	start, end, err := findBox(f, 0, info.Size(), "mdat")
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, end-start)
	if _, err := f.ReadAt(data, start); err != nil {
		t.Fatal(err)
	}
	return data
}

// Retomar a sessão não pode recomprimir o que já estava gravado: os pacotes
// de áudio de antes continuam no arquivo, byte a byte, a cada retomada.
func TestAppendSessionDoesNotReencodeEarlierAudio(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("o áudio comprimido das sessões só existe no macOS")
	}
	needSessionAudio(t)
	dir := t.TempDir()
	if _, err := AppendSession(dir, "s", speechWithPauses(20, nil)); err != nil {
		t.Fatal(err)
	}
	first := mdat(t, SessionFile(dir, "s"))

	// Segunda parte pelo caminho da gravação ao vivo (arquivo bruto)
	raw := filepath.Join(dir, "segunda.pcm")
	if err := AppendRaw(raw, speechWithPauses(10, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendSessionRaw(dir, "s", raw); err != nil {
		t.Fatal(err)
	}
	second := mdat(t, SessionFile(dir, "s"))
	if !bytes.Contains(second, first) {
		t.Fatal("a primeira parte foi recomprimida ao retomar a sessão")
	}
	before, err := LoadFile(SessionFile(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}

	// Terceira parte: agora o arquivo já é resultado de uma junção. A
	// exportação refaz um pacote (64ms) na emenda anterior, então a comparação
	// é pelo áudio decodificado: recomprimido, ele mudaria inteiro.
	if _, err := AppendSession(dir, "s", speechWithPauses(15, nil)); err != nil {
		t.Fatal(err)
	}
	after, err := LoadFile(SessionFile(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	changed := 0
	for i, sample := range before {
		if i >= len(after) || after[i] != sample {
			changed++
		}
	}
	if changed > len(before)/100 {
		t.Fatalf("%d das %d amostras já gravadas mudaram na segunda retomada: o áudio foi recomprimido", changed, len(before))
	}

	start, duration, err := Info(SessionFile(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(duration.Seconds()-45) > 0.1 {
		t.Fatalf("sessão com %.2fs, esperava 45s", duration.Seconds())
	}
	if want := time.Now().Add(-duration); start.Sub(want).Abs() > 5*time.Second {
		t.Fatalf("gravação começando em %v, esperava perto de %v (agora menos a duração)", start, want)
	}
	// O áudio decodificado ganha no máximo 0,2s de enchimento em cada emenda
	samples, err := LoadFile(SessionFile(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	if got := float64(len(samples)) / sampleRate; got < 45 || got > 45.7 {
		t.Fatalf("áudio decodificado com %.2fs, esperava entre 45s e 45,7s", got)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*_tmp.*"))
	if len(leftovers) > 0 {
		t.Fatalf("arquivos temporários deixados para trás: %v", leftovers)
	}
}

// Se o áudio já salvo não pode ser lido, o trecho novo não o sobrescreve: vai
// para um arquivo à parte.
func TestAppendSessionKeepsUnreadableAudio(t *testing.T) {
	needSessionAudio(t)
	dir := t.TempDir()
	target := SessionFile(dir, "s")
	if err := os.WriteFile(target, []byte("isto não é áudio"), 0644); err != nil {
		t.Fatal(err)
	}
	saved, err := AppendSession(dir, "s", speechWithPauses(5, nil))
	if err != nil {
		t.Fatal(err)
	}
	if saved == target {
		t.Fatal("o trecho novo deveria ir para um arquivo à parte")
	}
	if got, _ := os.ReadFile(target); string(got) != "isto não é áudio" {
		t.Fatal("o arquivo que já existia foi alterado")
	}
	if _, duration, err := Info(saved); err != nil || math.Abs(duration.Seconds()-5) > 0.3 {
		t.Fatalf("parte nova com %v (%v), esperava 5s", duration, err)
	}
}
