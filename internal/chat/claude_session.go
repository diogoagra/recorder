package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// ErrClaudeSessionClosed é o erro de Start e Send depois do Close.
var ErrClaudeSessionClosed = errors.New("a conversa com o claude foi encerrada")

// ClaudeSession é uma conversa longa com o claude: o processo fica aberto e
// cada mensagem vai pela entrada padrão (--input-format stream-json). Abrir
// um processo por pergunta custa ~2,5 s; com ele aberto, o Haiku responde
// uma frase curta em ~1,5 s. A conversa acumula as mensagens anteriores (o
// que ajuda a manter os mesmos termos) e o processo é renovado a cada
// MaxTurns mensagens, para ela não crescer sem fim: o novo é aberto logo
// depois da última resposta do antigo, e não na mensagem seguinte, que
// esperaria ele subir.
//
// As flags são as mesmas do ClaudeClient (sem ferramentas, sem as
// personalizações do usuário, sem guardar a conversa).
type ClaudeSession struct {
	Path     string
	Model    string
	System   string        // instruções, no lugar das de agente de programação
	MaxTurns int           // mensagens por processo
	Timeout  time.Duration // por mensagem
	// Prime, se não for vazio, é uma mensagem de exemplo mandada a cada
	// processo novo (no Start e na renovação por MaxTurns), com a resposta
	// descartada. A primeira mensagem de um processo leva ~2,7 s (o claude
	// monta a conversa e a API guarda as instruções); as seguintes, ~1 s. Com
	// Prime, quem paga a primeira é o aquecimento, e não a primeira frase de
	// verdade.
	Prime string

	mu sync.Mutex // uma mensagem de cada vez

	// cur e closed têm um lock só deles: o Close não espera a mensagem em
	// andamento (que pode levar até Timeout), ele a interrompe
	state  sync.Mutex
	cur    *claudeProcess
	closed bool
}

type claudeProcess struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	lines      chan []byte // saída, uma linha por evento; fecha quando o processo termina
	stderr     *lockedTail
	dir        string
	systemFile string
	turns      int
	primed     bool // já recebeu a mensagem de exemplo (ClaudeSession.Prime)
	closing    sync.Once
	stdout     io.Closer     // a ponta de leitura da saída do processo
	done       chan struct{} // fecha quando o processo é encerrado por close
}

// lockedTail é um tailBuffer que pode ser lido enquanto o processo escreve.
type lockedTail struct {
	mu sync.Mutex
	t  tailBuffer
}

func (l *lockedTail) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.t.Write(p)
}

func (l *lockedTail) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.t.String()
}

func NewClaudeSession(path, model, system string) *ClaudeSession {
	return &ClaudeSession{Path: path, Model: model, System: system, MaxTurns: 50, Timeout: 20 * time.Second}
}

// Start abre o processo, se ainda não estiver aberto, para a primeira
// mensagem não esperar o claude subir.
func (s *ClaudeSession) Start() error {
	p, err := s.process()
	if err != nil {
		return err
	}
	s.prime(p)
	return nil
}

// prime manda a mensagem de exemplo ao processo p, se ele é o atual e ainda
// não recebeu nenhuma. Uma mensagem de verdade que chegue no meio espera
// (como esperaria a primeira resposta, mais lenta, sem o aquecimento).
func (s *ClaudeSession) prime(p *claudeProcess) {
	if s.Prime == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Lock()
	current := s.cur == p && !s.closed
	s.state.Unlock()
	if !current || p.turns > 0 || p.primed {
		return
	}
	p.primed = true
	// Se falhar, a próxima mensagem de verdade mostra o erro
	s.exchange(context.Background(), s.Prime, false)
}

// process devolve o processo aberto, abrindo um se preciso.
func (s *ClaudeSession) process() (*claudeProcess, error) {
	s.state.Lock()
	defer s.state.Unlock()
	if s.closed {
		return nil, ErrClaudeSessionClosed
	}
	if s.cur == nil {
		p, err := s.open()
		if err != nil {
			return nil, err
		}
		s.cur = p
	}
	return s.cur, nil
}

func (s *ClaudeSession) open() (*claudeProcess, error) {
	if s.Path == "" {
		return nil, fmt.Errorf("o claude não foi encontrado neste computador")
	}
	dir, err := os.MkdirTemp("", "recorder-claude-*")
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp("", "recorder-claude-instrucoes-*.txt")
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	_, err = f.WriteString(s.System)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	p := &claudeProcess{dir: dir, systemFile: f.Name(), stderr: &lockedTail{}, lines: make(chan []byte, 64), done: make(chan struct{})}
	if err != nil {
		p.cleanup()
		return nil, err
	}
	// Sem as mensagens parciais: só a resposta inteira interessa
	var args []string
	for _, a := range (claudeDialect{}).args(s.Model, p.systemFile, nil) {
		if a != "--include-partial-messages" {
			args = append(args, a)
		}
	}
	args = append(args, "--input-format", "stream-json")
	p.cmd = exec.Command(s.Path, args...)
	p.cmd.Dir = dir
	hideConsole(p.cmd)
	p.cmd.Stderr = p.stderr
	if p.stdin, err = p.cmd.StdinPipe(); err != nil {
		p.cleanup()
		return nil, err
	}
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		p.cleanup()
		return nil, err
	}
	p.stdout = stdout
	if err := p.cmd.Start(); err != nil {
		p.cleanup()
		return nil, fmt.Errorf("não foi possível rodar o claude (%s): %w", s.Path, err)
	}
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			p.lines <- append([]byte(nil), scanner.Bytes()...)
		}
		close(p.lines)
		p.cmd.Wait()
		p.cleanup()
	}()
	return p, nil
}

// close fecha a entrada (o claude termina sozinho) e, por garantia, mata o processo.
func (p *claudeProcess) close() {
	p.closing.Do(func() {
		p.stdin.Close()
		if p.cmd.Process != nil {
			p.cmd.Process.Kill()
		}
		// Quem espera uma resposta deste processo para de esperar já, e a
		// leitura da saída termina: um processo-filho do claude que continue
		// vivo segura a saída aberta, e só o fim do claude não bastava
		close(p.done)
		if p.stdout != nil {
			p.stdout.Close()
		}
		go func() {
			for range p.lines { // o que ainda vier, para a leitura não travar
			}
		}()
	})
}

func (p *claudeProcess) cleanup() {
	os.RemoveAll(p.dir)
	os.Remove(p.systemFile)
}

// Send manda uma mensagem e devolve a resposta. Uma de cada vez.
func (s *ClaudeSession) Send(text string) (string, error) {
	return s.SendContext(context.Background(), text)
}

// SendContext é o Send que desiste quando ctx termina (além do Timeout).
func (s *ClaudeSession) SendContext(ctx context.Context, text string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	answer, dead, err := s.exchange(ctx, text, true)
	if dead {
		// O processo tinha morrido desde a última mensagem (ou morreu nesta):
		// uma tentativa só, num processo novo
		answer, _, err = s.exchange(ctx, text, true)
	}
	return answer, err
}

// exchange manda a mensagem ao processo aberto e espera a resposta. dead diz
// que o processo morreu sem responder (e não foi o Close que o fechou).
//
// counts diz se a mensagem conta para MaxTurns: a de exemplo (Prime) não
// conta, senão um processo novo já nasceria perto de ser renovado.
func (s *ClaudeSession) exchange(ctx context.Context, text string, counts bool) (answer string, dead bool, err error) {
	p, err := s.process()
	if err != nil {
		return "", false, err
	}
	msg, _ := json.Marshal(map[string]any{
		"type": "user",
		// Sem menções a arquivos: ver neutralizeMentions
		"message": map[string]any{"role": "user", "content": neutralizeMentions(text)},
	})
	if _, err := p.stdin.Write(append(msg, '\n')); err != nil {
		return s.gone(p, "o claude fechou")
	}
	if counts {
		p.turns++
	}
	timeout := time.NewTimer(s.Timeout)
	defer timeout.Stop()
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				return s.gone(p, "o claude terminou sem responder")
			}
			_, done, result, err := claudeDialect{}.event(line)
			if !done {
				continue
			}
			if err != nil {
				s.drop(p)
				return "", false, fmt.Errorf("erro do claude: %w", err)
			}
			if counts && p.turns >= s.MaxTurns {
				if next := s.renew(p); next != nil {
					go s.prime(next) // espera esta mensagem terminar (s.mu)
				}
			}
			return result, false, nil
		case <-p.done:
			// O Close encerrou o processo no meio da mensagem
			return s.gone(p, "o claude foi encerrado")
		case <-timeout.C:
			// A resposta atrasada chegaria na próxima mensagem: começa de novo
			s.renew(p)
			return "", false, fmt.Errorf("o claude não respondeu em %v", s.Timeout)
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				s.renew(p)
			} else {
				s.drop(p) // quem chamou desistiu (ex: o Intérprete foi desligado)
			}
			return "", false, ctx.Err()
		}
	}
}

// gone trata o processo que morreu no meio da mensagem.
func (s *ClaudeSession) gone(p *claudeProcess, what string) (string, bool, error) {
	s.drop(p)
	s.state.Lock()
	closed := s.closed
	s.state.Unlock()
	if closed {
		return "", false, ErrClaudeSessionClosed
	}
	return "", true, fmt.Errorf("%s: %s", what, p.stderr.String())
}

// drop descarta o processo p; a próxima mensagem abre outro.
func (s *ClaudeSession) drop(p *claudeProcess) {
	s.state.Lock()
	if s.cur == p {
		s.cur = nil
	}
	s.state.Unlock()
	p.close()
}

// renew troca o processo p por um novo já agora, para a próxima mensagem
// encontrá-lo de pé em vez de esperar o claude subir (~2,5 s). Devolve o
// processo novo, ou nil se não abriu.
func (s *ClaudeSession) renew(p *claudeProcess) (next *claudeProcess) {
	s.state.Lock()
	if s.cur == p {
		s.cur = nil
		if !s.closed {
			if opened, err := s.open(); err == nil {
				s.cur, next = opened, opened
			}
		}
	}
	s.state.Unlock()
	p.close()
	return next
}

// Close termina a conversa, interrompendo a mensagem em andamento. Depois
// dele, Start e Send devolvem ErrClaudeSessionClosed em vez de abrir outro
// processo (que ninguém fecharia).
func (s *ClaudeSession) Close() {
	s.state.Lock()
	s.closed = true
	p := s.cur
	s.cur = nil
	s.state.Unlock()
	if p != nil {
		p.close()
	}
}
