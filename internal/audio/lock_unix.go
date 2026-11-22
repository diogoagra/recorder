//go:build !windows

package audio

import (
	"os"
	"path/filepath"
	"syscall"
)

// locks mantém abertos os arquivos de trava: fechá-los solta a trava.
var locks []*os.File

// LockDir marca dir como usado por este processo até ele terminar (o sistema
// solta a trava inclusive se o processo travar). Devolve false se outro
// processo, como outra instância do app, já o usa.
func LockDir(dir string) bool {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	locks = append(locks, f)
	return true
}
