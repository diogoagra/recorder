package audio

import (
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestInfoReadsRecordingStartAndDuration(t *testing.T) {
	dir := t.TempDir()
	wavPath := filepath.Join(dir, "a.wav")
	if err := SaveWav(wavPath, make([]int16, 90*sampleRate), sampleRate); err != nil {
		t.Fatal(err)
	}
	paths := []string{wavPath}
	if _, err := exec.LookPath("afconvert"); err == nil {
		m4a := filepath.Join(dir, "a.m4a")
		if out, err := exec.Command("afconvert", "-f", "m4af", "-d", "aac", "-b", "32000", wavPath, m4a).CombinedOutput(); err != nil {
			t.Fatalf("afconvert: %v %s", err, out)
		}
		paths = append(paths, m4a)
	}

	for _, p := range paths {
		start, duration, err := Info(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if math.Abs(duration.Seconds()-90) > 0.2 {
			t.Errorf("%s: duração %v, esperava 90s", filepath.Base(p), duration)
		}
		// O arquivo acabou de ser gerado: a gravação "começou" 90s atrás
		if d := time.Since(start) - 90*time.Second; d < -5*time.Second || d > 5*time.Second {
			t.Errorf("%s: início %v, esperava cerca de 90s atrás", filepath.Base(p), start)
		}
	}
}

// Um m4a com a caixa mvhd vazia (arquivo cortado, ou feito para isso) dá
// erro, e não uma pane ao ler o primeiro byte dela.
func TestInfoRejectsAnEmptyMvhdBox(t *testing.T) {
	box := func(kind string, payload []byte) []byte {
		b := make([]byte, 8, 8+len(payload))
		binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
		copy(b[4:], kind)
		return append(b, payload...)
	}
	path := filepath.Join(t.TempDir(), "vazio.m4a")
	if err := os.WriteFile(path, append(box("ftyp", []byte("M4A ")), box("moov", box("mvhd", nil))...), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Info(path); err == nil {
		t.Fatal("esperava erro para um m4a com a caixa mvhd vazia")
	}
}
