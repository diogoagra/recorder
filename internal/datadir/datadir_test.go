package datadir

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDirFollowsHomeByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvVar, "")
	if got, want := Dir(), filepath.Join(home, ".recorder"); got != want {
		t.Fatalf("Dir() = %q, quero %q", got, want)
	}
	if got, want := DB(), filepath.Join(home, ".recorder", "transcripts.db"); got != want {
		t.Fatalf("DB() = %q, quero %q", got, want)
	}
}

func TestEnvVarReplacesTheDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvVar, dir)
	if got := Dir(); got != dir {
		t.Fatalf("Dir() = %q, quero %q", got, dir)
	}
	if got, want := Path("sessions_audio", "a.m4a"), filepath.Join(dir, "sessions_audio", "a.m4a"); got != want {
		t.Fatalf("Path() = %q, quero %q", got, want)
	}
}

// A pasta de dados é só do dono, mesmo a que já existia aberta aos outros
// usuários do computador (como as criadas pelas versões anteriores).
func TestEnsureClosesTheDataDirToOtherUsers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permissões de arquivo do Unix")
	}
	dir := filepath.Join(t.TempDir(), "dados")
	t.Setenv(EnvVar, dir)
	for _, before := range []string{"nova", "antiga"} {
		if before == "antiga" {
			if err := os.Chmod(dir, 0755); err != nil {
				t.Fatal(err)
			}
		}
		if err := Ensure(); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0700 {
			t.Errorf("pasta %s: permissão %o, esperava 700", before, mode)
		}
	}
}
