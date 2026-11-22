//go:build !windows

package transcriber

import "os/exec"

func hideConsole(cmd *exec.Cmd) {}

// bindToApp não faz nada fora do Windows: lá os servidores órfãos são
// encerrados por KillOrphans na próxima execução.
func bindToApp(cmd *exec.Cmd) {}
