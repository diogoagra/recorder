//go:build windows

package audio

import (
	"path/filepath"
	"syscall"
)

// locks mantém abertos os arquivos de trava: fechá-los solta a trava.
var locks []syscall.Handle

// LockDir marca dir como usado por este processo até ele terminar (o sistema
// solta a trava inclusive se o processo travar). Devolve false se outro
// processo, como outra instância do app, já o usa.
func LockDir(dir string) bool {
	name, err := syscall.UTF16PtrFromString(filepath.Join(dir, ".lock"))
	if err != nil {
		return false
	}
	// Aberto sem compartilhamento: enquanto ficar aberto, ninguém mais abre
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return false
	}
	locks = append(locks, h)
	return true
}
