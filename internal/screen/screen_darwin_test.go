//go:build darwin

package screen

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Captura de verdade a tela principal. Só roda a pedido, porque fotografa a
// tela de quem está testando e precisa da permissão de gravação de tela do
// macOS para o terminal:
//
//	RECORDER_SCREEN_TEST=1 go test ./internal/screen -run TestCapturesTheScreen -v
func TestCapturesTheScreen(t *testing.T) {
	if os.Getenv("RECORDER_SCREEN_TEST") == "" {
		t.Skip("defina RECORDER_SCREEN_TEST=1 para capturar a tela de verdade")
	}
	if !Allowed() {
		t.Skip("o terminal não tem a permissão de gravação de tela")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "tela.png")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := capture(ctx, path, false); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	config, err := png.DecodeConfig(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if config.Width < 640 || config.Height < 400 {
		t.Fatalf("captura de %d x %d: pequena demais para ser a tela", config.Width, config.Height)
	}
	thumb := filepath.Join(dir, "thumbs", "tela.jpg")
	if err := Thumbnail(path, thumb); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(thumb)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := os.Stat(path)
	t.Logf("captura de %d x %d (%d KB), miniatura de %d KB", config.Width, config.Height, original.Size()/1024, info.Size()/1024)
}

// Com o tempo esgotado antes de escolher a área, vale como desistência e não
// sobra arquivo.
func TestCaptureCanceledLeavesNoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tela.png")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := capture(ctx, path, true); err != ErrCanceled {
		t.Fatalf("esperava ErrCanceled, veio %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("não deveria sobrar arquivo")
	}
}
