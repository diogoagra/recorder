package transcriber

import (
	"log"
	"os/exec"
	"sync"
	"time"
)

// Server mantém um whisper-server rodando enquanto o app está aberto: sobe o
// servidor em segundo plano (a janela do app não espera por ele) e sobe de
// novo se ele cair no meio de uma reunião. Antes, se o servidor morresse, as
// transcrições falhavam até o app ser reaberto.
type Server struct {
	start func() (*exec.Cmd, error) // sobe o servidor; nil, nil se já há um atendendo
	alive func() bool               // há um servidor atendendo (o nosso ou um de fora)
	// Se definido, o servidor só sobe depois de este canal ser fechado: é o
	// modelo, que na primeira execução ainda está sendo baixado
	ready <-chan struct{}

	mu      sync.Mutex
	cmd     *exec.Cmd
	stopped bool
	stop    chan struct{}
	done    chan struct{}
}

// Tempos do supervisor; variáveis para os testes não esperarem.
var (
	// Espera antes de subir de novo um servidor que caiu: dobra a cada queda
	// seguida, para um servidor que não consegue subir (ex: a porta ocupada
	// por outro programa) não ficar em laço.
	restartDelay    = 2 * time.Second
	maxRestartDelay = time.Minute
	// Um servidor que ficou de pé por este tempo zera a espera
	stableAfter = 30 * time.Second
	// De quanto em quanto tempo confere um servidor que não é nosso
	externalPoll = 5 * time.Second
)

// Supervise sobe o whisper-server com o modelo na porta, sem esperar por ele,
// e o mantém rodando até Stop. Se o modelo ainda está sendo baixado, o
// servidor sobe quando o download terminar.
func Supervise(model *ModelFile, port string) *Server {
	return superviseWhen(
		model.ready,
		func() (*exec.Cmd, error) {
			path, _ := model.Path()
			return StartServer(path, port)
		},
		func() bool { return whisperListening(port) },
	)
}

func supervise(start func() (*exec.Cmd, error), alive func() bool) *Server {
	return superviseWhen(nil, start, alive)
}

func superviseWhen(ready <-chan struct{}, start func() (*exec.Cmd, error), alive func() bool) *Server {
	s := &Server{start: start, alive: alive, ready: ready, stop: make(chan struct{}), done: make(chan struct{})}
	go s.run()
	return s
}

func (s *Server) run() {
	defer close(s.done)
	if s.ready != nil {
		select {
		case <-s.ready:
		case <-s.stop:
			return
		}
	}
	delay := restartDelay
	for {
		started := time.Now()
		cmd, err := s.start()

		s.mu.Lock()
		if s.stopped {
			// O Stop chegou enquanto o servidor subia: ele ainda não tinha
			// como ser encerrado por lá
			s.mu.Unlock()
			kill(cmd)
			if cmd != nil {
				cmd.Wait()
			}
			return
		}
		s.cmd = cmd
		s.mu.Unlock()

		switch {
		case err != nil:
			log.Printf("Aviso: falha ao iniciar o whisper-server (nova tentativa em %v): %v", delay, err)
		case cmd == nil:
			// Outra instância do app, ou um servidor iniciado à parte, atende
			// na porta: só acompanha, para assumir se ele sair
			for s.alive() {
				if s.sleep(externalPoll) {
					return
				}
			}
			log.Println("-> O whisper-server que atendia na porta saiu; subindo o do app.")
			continue
		default:
			waitErr := cmd.Wait()
			s.mu.Lock()
			s.cmd = nil
			stopped := s.stopped
			s.mu.Unlock()
			if stopped {
				return
			}
			if time.Since(started) > stableAfter {
				delay = restartDelay
			}
			log.Printf("Aviso: o whisper-server terminou (%v); subindo de novo em %v", waitErr, delay)
		}
		if s.sleep(delay) {
			return
		}
		delay = min(delay*2, maxRestartDelay)
	}
}

// sleep espera d e diz se o supervisor foi parado nesse meio tempo.
func (s *Server) sleep(d time.Duration) bool {
	select {
	case <-s.stop:
		return true
	case <-time.After(d):
		return false
	}
}

// stopWait é quanto o Stop espera o supervisor terminar. Subir o servidor
// (StartServer) leva uns 2 s, 4 s se houver um órfão para encerrar antes.
var stopWait = 10 * time.Second

// Stop encerra o whisper-server e não o sobe mais. Um servidor que não foi
// iniciado por este app continua rodando.
//
// Só volta depois de o supervisor terminar: um servidor que estava subindo
// neste instante ainda não é conhecido aqui, e é o supervisor que o encerra
// quando o recebe. Sem esperar, fechar o app logo depois de abrir deixava um
// whisper-server órfão.
func (s *Server) Stop() {
	s.mu.Lock()
	if !s.stopped {
		s.stopped = true
		close(s.stop)
	}
	cmd := s.cmd
	s.mu.Unlock()
	kill(cmd)
	s.Wait(stopWait)
}

// Wait espera o supervisor terminar depois de um Stop, até timeout.
func (s *Server) Wait(timeout time.Duration) {
	select {
	case <-s.done:
	case <-time.After(timeout):
	}
}

func kill(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
	}
}
