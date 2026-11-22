//go:build windows

package audio

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideWindow impede que o ffmpeg abra uma janela de console: o app é de
// janela, e o Windows criaria um console novo para ele.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
