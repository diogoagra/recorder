//go:build darwin

package screen

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework CoreGraphics -framework Foundation
#import <CoreGraphics/CoreGraphics.h>
#import <Foundation/Foundation.h>

// screenAccess diz se o macOS deixa o app ver a tela. Com ask, mostra o
// pedido de permissão do sistema (só aparece na primeira vez).
static int screenAccess(int ask) {
    if (@available(macOS 10.15, *)) {
        if (CGPreflightScreenCaptureAccess()) {
            return 1;
        }
        return ask && CGRequestScreenCaptureAccess() ? 1 : 0;
    }
    return 1;
}
*/
import "C"

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Supported diz se a captura de tela existe neste sistema.
const Supported = true

// Allowed diz se o macOS já deixa o Recorder capturar a tela (permissão de
// gravação de tela, em Privacidade e Segurança).
func Allowed() bool {
	return C.screenAccess(0) == 1
}

// RequestAccess diz se o macOS deixa o Recorder capturar a tela e, se ainda
// não deixar, mostra o pedido de permissão do sistema (só na primeira vez).
func RequestAccess() bool {
	return C.screenAccess(1) == 1
}

// Capture deixa o usuário escolher o que guardar, como o ⌘⇧4 do macOS:
// arrastando uma área ou, depois de apertar Espaço, clicando numa janela. A
// imagem vai para path, em PNG. Devolve ErrCanceled se ele desistir (Esc).
// Sem a permissão de gravação de tela (RequestAccess), o macOS só entrega o
// fundo de tela, sem as janelas.
func Capture(ctx context.Context, path string) error {
	return capture(ctx, path, true)
}

// capture roda o screencapture do macOS. Sem interactive, guarda a tela
// principal inteira.
func capture(ctx context.Context, path string, interactive bool) error {
	// -x: sem o som do obturador, que entraria na gravação do áudio do
	// computador; -o: janela sem a sombra em volta
	args := []string{"-x", "-o", "-t", "png"}
	if interactive {
		args = append(args, "-i")
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "/usr/sbin/screencapture", append(args, path)...)
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return nil
	}
	os.Remove(path)
	// Sem arquivo e sem mensagem: o usuário apertou Esc na seleção (ou o
	// tempo dela acabou)
	if message := bytes.TrimSpace(stderr.Bytes()); len(message) > 0 && ctx.Err() == nil {
		return fmt.Errorf("screencapture: %s", message)
	}
	if runErr != nil && !interactive {
		return fmt.Errorf("screencapture: %w", runErr)
	}
	return ErrCanceled
}
