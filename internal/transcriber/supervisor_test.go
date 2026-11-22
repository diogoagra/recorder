package transcriber

import (
	"errors"
	"os/exec"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func fastSupervisor(t *testing.T) {
	t.Helper()
	oldDelay, oldMax, oldPoll := restartDelay, maxRestartDelay, externalPoll
	restartDelay, maxRestartDelay, externalPoll = 5*time.Millisecond, 20*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { restartDelay, maxRestartDelay, externalPoll = oldDelay, oldMax, oldPoll })
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("não aconteceu a tempo: %s", what)
}

func TestSuperviseRestartsServerThatDies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("usa o sleep do sistema")
	}
	fastSupervisor(t)
	var starts atomic.Int32
	s := supervise(func() (*exec.Cmd, error) {
		// As duas primeiras execuções morrem logo; a terceira fica de pé
		duration := "0.01"
		if starts.Add(1) >= 3 {
			duration = "60"
		}
		cmd := exec.Command("sleep", duration)
		return cmd, cmd.Start()
	}, func() bool { return false })

	waitFor(t, "o servidor ser iniciado de novo depois de cair duas vezes", func() bool { return starts.Load() == 3 })
	time.Sleep(50 * time.Millisecond)
	if n := starts.Load(); n != 3 {
		t.Fatalf("o servidor de pé foi reiniciado (%d partidas)", n)
	}

	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	s.Stop()
	s.Wait(5 * time.Second)
	select {
	case <-s.done:
	default:
		t.Fatal("o supervisor não terminou depois do Stop")
	}
	if cmd == nil || cmd.ProcessState == nil {
		t.Fatal("o Stop não encerrou o servidor")
	}
	if n := starts.Load(); n != 3 {
		t.Fatalf("o servidor foi iniciado de novo depois do Stop (%d partidas)", n)
	}
}

func TestSuperviseRetriesWhenStartFails(t *testing.T) {
	fastSupervisor(t)
	var starts atomic.Int32
	s := supervise(func() (*exec.Cmd, error) {
		starts.Add(1)
		return nil, errors.New("porta ocupada por outro programa")
	}, func() bool { return false })
	defer s.Stop()
	waitFor(t, "novas tentativas de subir o servidor", func() bool { return starts.Load() >= 3 })
}

func TestSuperviseTakesOverWhenExternalServerLeaves(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("usa o sleep do sistema")
	}
	fastSupervisor(t)
	var external atomic.Bool
	external.Store(true)
	var starts atomic.Int32
	s := supervise(func() (*exec.Cmd, error) {
		starts.Add(1)
		if external.Load() {
			return nil, nil // outro servidor já atende na porta
		}
		cmd := exec.Command("sleep", "60")
		return cmd, cmd.Start()
	}, external.Load)
	defer s.Stop()

	time.Sleep(50 * time.Millisecond)
	if n := starts.Load(); n != 1 {
		t.Fatalf("com um servidor de fora atendendo, não deveria tentar subir outro (%d partidas)", n)
	}
	external.Store(false)
	waitFor(t, "o app subir o próprio servidor quando o de fora sai", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.cmd != nil
	})
}

// Fechar o app logo depois de abrir: o Stop chega com o servidor ainda
// subindo. Ele tem que ser encerrado assim mesmo (um órfão segurava a saída
// do app, e o `wails build` travava esperando por ela).
func TestStopKillsAServerThatIsStillStarting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("usa o sleep do sistema")
	}
	fastSupervisor(t)
	spawned := make(chan *exec.Cmd, 1)
	s := supervise(func() (*exec.Cmd, error) {
		cmd := exec.Command("sleep", "60")
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		spawned <- cmd
		time.Sleep(200 * time.Millisecond) // StartServer espera o servidor subir
		return cmd, nil
	}, func() bool { return false })

	cmd := <-spawned // o processo existe, mas o supervisor ainda não o recebeu
	s.Stop()
	select {
	case <-s.done:
	default:
		t.Fatal("o Stop deveria esperar o supervisor terminar")
	}
	if cmd.ProcessState == nil {
		t.Fatal("o servidor que estava subindo ficou vivo depois do Stop")
	}
	s.Stop() // duas vezes não quebra
}

func TestSuperviseStartsTheServerOnlyWhenTheModelIsReady(t *testing.T) {
	fastSupervisor(t)
	ready := make(chan struct{})
	var starts atomic.Int32
	s := superviseWhen(ready, func() (*exec.Cmd, error) {
		starts.Add(1)
		return nil, errors.New("sem servidor neste teste")
	}, func() bool { return false })
	defer s.Stop()

	time.Sleep(50 * time.Millisecond)
	if n := starts.Load(); n != 0 {
		t.Fatalf("o servidor foi iniciado %d vez(es) sem o modelo", n)
	}
	close(ready)
	waitFor(t, "o servidor ser iniciado depois de o modelo chegar", func() bool { return starts.Load() >= 1 })
}

// Fechar o app com o modelo ainda sendo baixado não pode ficar esperando.
func TestStopWhileWaitingForTheModel(t *testing.T) {
	s := superviseWhen(make(chan struct{}), func() (*exec.Cmd, error) {
		t.Error("o servidor não deveria ser iniciado sem o modelo")
		return nil, nil
	}, func() bool { return false })

	stopped := make(chan struct{})
	go func() {
		s.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("o Stop ficou esperando o modelo")
	}
}
