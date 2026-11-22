//go:build windows

package chat

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideConsole impede que cada chamada ao agy abra uma janela de console: o
// app é de janela, e o Windows criaria um console novo para o agy.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
