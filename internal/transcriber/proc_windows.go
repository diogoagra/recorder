//go:build windows

package transcriber

import (
	"log"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// hideConsole impede que o whisper-server abra uma janela de console: o app
// é de janela, e o Windows criaria um console novo para o servidor.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// bindToApp põe o whisper-server num Job Object que o encerra quando o app
// termina, mesmo fechado à força: no Windows o processo filho não morre junto
// com o pai e ficaria ocupando a porta e a memória.
func bindToApp(cmd *exec.Cmd) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		log.Printf("Aviso: não foi possível criar o job do whisper-server: %v", err)
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		log.Printf("Aviso: não foi possível configurar o job do whisper-server: %v", err)
		return
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		log.Printf("Aviso: não foi possível abrir o processo do whisper-server: %v", err)
		return
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		windows.CloseHandle(job)
		log.Printf("Aviso: não foi possível associar o whisper-server ao app: %v", err)
	}
	// O handle do job fica aberto até o app terminar; o sistema o fecha
	// quando o app morre, e aí encerra o whisper-server.
}
