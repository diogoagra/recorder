package audio

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func needSessionAudio(t *testing.T) {
	if _, err := exec.LookPath("afconvert"); err != nil && runtime.GOOS != "windows" {
		t.Skip("afconvert indisponível")
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestBlockFileKeepsSessionAndOffset(t *testing.T) {
	path := BlockFile(t.TempDir(), "3f2a9c1e-5b7d-4e8a-9c06-1d2e3f4a5b6c", 754.3216)
	id, offset, ok := parseBlockFile(path)
	if !ok || id != "3f2a9c1e-5b7d-4e8a-9c06-1d2e3f4a5b6c" || offset != 754.322 {
		t.Fatalf("parseBlockFile(%s) = %q, %v, %v", filepath.Base(path), id, offset, ok)
	}
	// Nome das versões antigas, sem a sessão
	if _, _, ok := parseBlockFile("block_1789992175123456789.wav"); ok {
		t.Fatal("nome sem sessão não deveria ser aceito")
	}
}

func TestRecoverSavesInterruptedRecording(t *testing.T) {
	needSessionAudio(t)
	tmpDir, sessionsDir := t.TempDir(), t.TempDir()

	// A sessão já tinha 20s salvos; o app fechou no meio dos 10s seguintes,
	// com dois blocos ainda na fila de transcrição
	if _, err := AppendSession(sessionsDir, "s1", speechWithPauses(20, nil)); err != nil {
		t.Fatal(err)
	}
	raw := RawFile(tmpDir, "s1")
	if err := AppendRaw(raw, speechWithPauses(10, nil)); err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, 9, 21, 14, 9, 0, 0, time.Local)
	os.Chtimes(raw, end, end)
	SaveWav(BlockFile(tmpDir, "s1", 25), speechWithPauses(5, nil), sampleRate)
	SaveWav(BlockFile(tmpDir, "s1", 20), speechWithPauses(5, nil), sampleRate)
	// Restos que não servem: bloco de uma sessão apagada, bloco de uma versão
	// antiga e a conversão interrompida do áudio da sessão
	SaveWav(BlockFile(tmpDir, "apagada", 0), speechWithPauses(5, nil), sampleRate)
	SaveWav(filepath.Join(tmpDir, "block_1789992175123456789.wav"), speechWithPauses(5, nil), sampleRate)
	os.WriteFile(filepath.Join(sessionsDir, "s1_tmp.m4a"), []byte("lixo"), 0644)

	got := Recover(tmpDir, sessionsDir, func(id string) bool { return id == "s1" }, func(time.Time) (string, error) {
		t.Fatal("não deveria criar sessão para uma sessão que existe")
		return "", nil
	})

	if len(got) != 1 || got[0].SessionID != "s1" || len(got[0].Blocks) != 2 ||
		got[0].Blocks[0].Offset != 20 || got[0].Blocks[1].Offset != 25 {
		t.Fatalf("Recover = %+v, esperava os dois blocos da s1 em ordem", got)
	}
	if d := SessionDuration(sessionsDir, "s1"); math.Abs(d-30) > 0.3 {
		t.Fatalf("áudio da sessão com %.2fs, esperava 30s", d)
	}
	start, _, err := Info(SessionFile(sessionsDir, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	if want := end.Add(-30 * time.Second); start.Sub(want).Abs() > time.Second {
		t.Fatalf("gravação começando em %v, esperava %v (pelo fim da gravação, não pela hora da recuperação)", start, want)
	}
	for _, path := range []string{raw, BlockFile(tmpDir, "apagada", 0), filepath.Join(tmpDir, "block_1789992175123456789.wav"), filepath.Join(sessionsDir, "s1_tmp.m4a")} {
		if fileExists(path) {
			t.Errorf("%s deveria ter sido apagado", filepath.Base(path))
		}
	}
}

func TestRecoverCreatesSessionForUnknownRecording(t *testing.T) {
	needSessionAudio(t)
	tmpDir, sessionsDir := t.TempDir(), t.TempDir()

	// Áudio bruto de uma versão antiga do app, sem a sessão no nome
	raw := filepath.Join(tmpDir, "raw_1789992175.pcm")
	if err := AppendRaw(raw, speechWithPauses(70, nil)); err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, 9, 21, 14, 9, 0, 0, time.Local)
	os.Chtimes(raw, end, end)

	var created time.Time
	sessions := map[string]bool{}
	got := Recover(tmpDir, sessionsDir, func(id string) bool { return sessions[id] }, func(start time.Time) (string, error) {
		created = start
		sessions["nova"] = true
		return "nova", nil
	})

	if want := end.Add(-70 * time.Second); !created.Equal(want) {
		t.Fatalf("sessão criada com início em %v, esperava %v", created, want)
	}
	if len(got) != 1 || got[0].SessionID != "nova" {
		t.Fatalf("Recover = %+v, esperava a sessão nova", got)
	}
	// Nada foi transcrito: o áudio todo vira blocos, um depois do outro
	var next float64
	for _, b := range got[0].Blocks {
		samples, err := LoadWav(b.Path)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(b.Offset-next) > 0.001 {
			t.Fatalf("bloco em %.3fs, esperava em %.3fs", b.Offset, next)
		}
		next += float64(len(samples)) / sampleRate
	}
	if len(got[0].Blocks) < 2 || math.Abs(next-70) > 0.001 {
		t.Fatalf("%d blocos somando %.2fs, esperava os 70s em blocos de ~30s", len(got[0].Blocks), next)
	}
	if d := SessionDuration(sessionsDir, "nova"); math.Abs(d-70) > 0.3 {
		t.Fatalf("áudio da sessão com %.2fs, esperava 70s", d)
	}
	if fileExists(raw) || fileExists(RawFile(tmpDir, "nova")) {
		t.Fatal("o áudio bruto deveria ter sido apagado depois de salvo")
	}
}

func TestLockDirRejectsSecondInstance(t *testing.T) {
	dir := t.TempDir()
	if !LockDir(dir) {
		t.Fatal("a primeira trava deveria funcionar")
	}
	if LockDir(dir) {
		t.Fatal("a segunda trava na mesma pasta deveria falhar")
	}
}
