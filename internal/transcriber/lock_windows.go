//go:build windows

package transcriber

import "syscall"

// lockFile trava path para este processo até unlock ser chamado (o sistema
// solta a trava inclusive se o processo travar). ok é false se outro
// processo, como outra instância do app, já tem a trava.
func lockFile(path string) (unlock func(), ok bool) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, false
	}
	// Aberto sem compartilhamento: enquanto ficar aberto, ninguém mais abre
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, false
	}
	return func() { syscall.CloseHandle(h) }, true
}
