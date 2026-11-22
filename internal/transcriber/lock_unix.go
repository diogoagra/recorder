//go:build !windows

package transcriber

import (
	"os"
	"syscall"
)

// lockFile trava path para este processo até unlock ser chamado (o sistema
// solta a trava inclusive se o processo travar). ok é false se outro
// processo, como outra instância do app, já tem a trava.
func lockFile(path string) (unlock func(), ok bool) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false
	}
	return func() { f.Close() }, true
}
