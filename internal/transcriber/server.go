package transcriber

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Port é a porta do whisper-server do Recorder, longe das portas comuns de
// desenvolvimento. Na 8080, um servidor Java recebia as transcrições e
// respondia 404, e o app achava que ele era o Whisper.
const Port = "47823"

// Endpoint é onde o app fala com o whisper-server. É 127.0.0.1, e não
// localhost: o servidor só escuta em IPv4, e localhost vai primeiro para o
// IPv6 (::1), onde outro programa pode estar na mesma porta.
const Endpoint = "http://127.0.0.1:" + Port

// resolveModelPath procura o modelo na pasta atual ou, no app empacotado, em
// Contents/Resources (macOS) ou ao lado do Recorder.exe (Windows).
func resolveModelPath(modelName string) string {
	modelPath := modelName
	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		exe, _ := os.Executable()
		for _, p := range []string{
			filepath.Join(filepath.Dir(exe), "..", "Resources", modelName),
			filepath.Join(filepath.Dir(exe), modelName),
		} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return modelPath
}

func resolveWhisperBin(binName string) string {
	exe, _ := os.Executable()
	var commonPaths []string
	switch runtime.GOOS {
	case "windows":
		// O scripts/build-windows.sh põe o whisper-server e as DLLs dele na pasta
		// whisper, ao lado do Recorder.exe
		commonPaths = append(commonPaths, filepath.Join(filepath.Dir(exe), "whisper", binName+".exe"))
	case "darwin":
		// O scripts/build-macos.sh compila o whisper-server e o põe em Contents/MacOS,
		// ao lado do executável do app. Sem ele (ex: wails dev), usa o do Homebrew.
		commonPaths = append(commonPaths, filepath.Join(filepath.Dir(exe), binName))
	}
	if runtime.GOOS != "windows" {
		// No Windows estes caminhos seriam C:\opt\homebrew\bin e
		// C:\usr\local\bin, pastas que qualquer usuário do computador
		// consegue criar
		commonPaths = append(commonPaths,
			"/opt/homebrew/bin/"+binName,
			"/usr/local/bin/"+binName,
		)
	}
	for _, p := range commonPaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return binName
}

// StartServer sobe o whisper-server com o modelo na porta dada, só em
// 127.0.0.1. Devolve nil se já houver um whisper-server atendendo na porta
// (outra instância do app ou um iniciado à parte), e erro se ela estiver
// ocupada por outro programa.
func StartServer(modelName, port string) (*exec.Cmd, error) {
	KillOrphans(port)
	if whisperListening(port) {
		if !NewWhisperClient("http://127.0.0.1:" + port).Running() {
			return nil, fmt.Errorf("a porta %s está ocupada por outro programa", port)
		}
		// Outra instância do app, ou um servidor iniciado à parte, já atende na porta
		log.Printf("-> whisper-server já está rodando na porta %s; usando o existente.", port)
		return nil, nil
	}

	log.Printf("-> Iniciando whisper-server (%s) na porta %s...", modelName, port)

	modelPath := resolveModelPath(modelName)
	whisperBin := resolveWhisperBin("whisper-server")

	cmd := exec.Command(whisperBin, "-m", modelPath, "--host", "127.0.0.1", "--port", port)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	hideConsole(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("erro ao iniciar whisper-server (%s): %v", modelName, err)
	}
	bindToApp(cmd)

	time.Sleep(2 * time.Second) // Aguardar o servidor subir
	return cmd, nil
}

// O app encerra o whisper-server ao fechar normalmente, mas se for fechado à
// força o servidor fica órfão, ainda ouvindo na porta. O whisper-server deixa
// outro processo ouvir na mesma porta, então a próxima execução subia um
// segundo servidor e as transcrições eram divididas entre os dois.

// KillOrphans encerra os whisper-server órfãos (sem o app que os iniciou) que
// estão na porta dada. StartServer já faz isso para a porta em que sobe; o app
// chama na abertura também para a porta do Intérprete, cujo servidor só sobe
// quando o Intérprete é ligado e ficaria ocupando memória até lá.
func KillOrphans(port string) {
	if runtime.GOOS == "windows" {
		return // no Windows o bindToApp já encerra o servidor junto com o app
	}
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		return
	}
	pids := orphanWhisperPIDs(string(out), port)
	for _, pid := range pids {
		if p, err := os.FindProcess(pid); err == nil {
			log.Printf("-> Encerrando whisper-server órfão de uma execução anterior (pid %d)", pid)
			p.Signal(syscall.SIGTERM)
		}
	}
	// Espera a porta ser liberada
	for i := 0; len(pids) > 0 && i < 20 && whisperListening(port); i++ {
		time.Sleep(100 * time.Millisecond)
	}
}

// orphanWhisperPIDs lê a saída de "ps -axo pid=,ppid=,command=" e devolve os
// whisper-server na porta indicada cujo pai é o launchd/init (pid 1).
func orphanWhisperPIDs(psOutput, port string) []int {
	var pids []int
	for _, line := range strings.Split(psOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "1" {
			continue
		}
		if !strings.Contains(fields[2], "whisper-server") || !hasPortArg(fields[3:], port) {
			continue
		}
		if pid, err := strconv.Atoi(fields[0]); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// hasPortArg diz se os argumentos têm "--port <port>", com a porta exata (a
// 47823 não é a 478231).
func hasPortArg(args []string, port string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--port" && args[i+1] == port {
			return true
		}
	}
	return false
}

func whisperListening(port string) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
