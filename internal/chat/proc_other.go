//go:build !windows

package chat

import "os/exec"

func hideConsole(cmd *exec.Cmd) {}
