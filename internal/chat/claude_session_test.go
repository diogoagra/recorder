//go:build !windows

package chat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeClaudeSession cria um claude falso que fica aberto: para cada linha da
// entrada, responde "resposta N" (N conta as mensagens de todos os processos).
// Uma mensagem com "lento" demora 2 s.
func fakeClaudeSession(t *testing.T) (path string, read func(name string) string) {
	dir := t.TempDir()
	path = filepath.Join(dir, "claude")
	script := `#!/bin/sh
printf '%s\n' "$@" > "` + dir + `/args"
echo processo >> "` + dir + `/starts"
while IFS= read -r line; do
  echo "$line" >> "` + dir + `/stdin"
  case "$line" in *lento*) sleep 2 ;; esac
  n=$(wc -l < "` + dir + `/stdin" | tr -d ' ')
  echo '{"type":"system","subtype":"init"}'
  printf '{"type":"result","subtype":"success","is_error":false,"result":"resposta %s"}\n' "$n"
done
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return path, func(name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return string(b)
	}
}

func TestClaudeSessionKeepsOneProcessOpen(t *testing.T) {
	path, read := fakeClaudeSession(t)
	s := NewClaudeSession(path, "haiku", "Traduza.")
	defer s.Close()
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"resposta 1", "resposta 2"} {
		got, err := s.Send("frase " + string(rune('a'+i)))
		if err != nil || got != want {
			t.Fatalf("mensagem %d: %q, %v", i+1, got, err)
		}
	}
	if starts := strings.Count(read("starts"), "processo"); starts != 1 {
		t.Fatalf("as mensagens deveriam ir para o mesmo processo, abriu %d", starts)
	}
	args := strings.Split(strings.TrimSuffix(read("args"), "\n"), "\n")
	if i := slices.Index(args, "--input-format"); i < 0 || args[i+1] != "stream-json" {
		t.Fatalf("a entrada deveria ser stream-json: %q", args)
	}
	if i := slices.Index(args, "--model"); i < 0 || args[i+1] != "haiku" {
		t.Fatalf("modelo: %q", args)
	}
	if slices.Contains(args, "--include-partial-messages") || !slices.Contains(args, "--safe-mode") {
		t.Fatalf("argumentos: %q", args)
	}
	if i := slices.Index(args, "--tools"); i < 0 || args[i+1] != "" {
		t.Fatalf("as ferramentas deveriam estar desligadas: %q", args)
	}
	// Cada mensagem é uma linha JSON de usuário
	first := strings.SplitN(read("stdin"), "\n", 2)[0]
	var msg struct {
		Type    string
		Message struct{ Role, Content string }
	}
	if err := json.Unmarshal([]byte(first), &msg); err != nil || msg.Type != "user" || msg.Message.Content != "frase a" {
		t.Fatalf("mensagem enviada: %s (%v)", first, err)
	}
}

// As frases do Intérprete também vão sem menções a arquivos (o claude
// anexaria o conteúdo de um "@caminho": ver neutralizeMentions).
func TestClaudeSessionGetsNoFileMentions(t *testing.T) {
	path, read := fakeClaudeSession(t)
	s := NewClaudeSession(path, "haiku", "Traduza.")
	defer s.Close()
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send("PT: mande para @/Users/ana/segredo.txt"); err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Message struct{ Content string }
	}
	first := strings.SplitN(read("stdin"), "\n", 2)[0]
	if err := json.Unmarshal([]byte(first), &msg); err != nil || msg.Message.Content != "PT: mande para \u200b@\u200b/Users/ana/segredo.txt" {
		t.Fatalf("mensagem enviada: %q (%v)", msg.Message.Content, err)
	}
}

func TestClaudeSessionRenewsAndRecovers(t *testing.T) {
	path, read := fakeClaudeSession(t)
	s := NewClaudeSession(path, "haiku", "Traduza.")
	defer s.Close()
	s.MaxTurns = 2
	for range 3 {
		if _, err := s.Send("frase"); err != nil {
			t.Fatal(err)
		}
	}
	if starts := strings.Count(read("starts"), "processo"); starts != 2 {
		t.Fatalf("depois de MaxTurns mensagens deveria abrir outro processo, abriu %d", starts)
	}

	// Sem resposta a tempo: erro, e a próxima mensagem vai para um processo novo
	s.Timeout = 300 * time.Millisecond
	if _, err := s.Send("frase lento"); err == nil {
		t.Fatal("deveria desistir da resposta atrasada")
	}
	s.Timeout = 5 * time.Second
	got, err := s.Send("depois")
	if err != nil || !strings.HasPrefix(got, "resposta") {
		t.Fatalf("depois do atraso: %q, %v", got, err)
	}
	if starts := strings.Count(read("starts"), "processo"); starts != 3 {
		t.Fatalf("o processo atrasado deveria ser trocado, abriu %d", starts)
	}
}

func TestClaudeSessionWithoutClaude(t *testing.T) {
	s := NewClaudeSession("", "haiku", "Traduza.")
	if _, err := s.Send("frase"); err == nil || !strings.Contains(err.Error(), "não foi encontrado") {
		t.Fatalf("sem o claude: %v", err)
	}
}

// starts espera o claude falso ter sido aberto n vezes (ele registra ao subir).
func waitStarts(t *testing.T, read func(string) string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(read("starts"), "processo") != n {
		if time.Now().After(deadline) {
			t.Fatalf("esperava %d processos abertos, há %d", n, strings.Count(read("starts"), "processo"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Depois do Close ninguém mais fecharia um processo novo: Start e Send
// devolvem erro em vez de reabrir (era um claude vazado até o app sair).
func TestClaudeSessionStaysClosed(t *testing.T) {
	path, read := fakeClaudeSession(t)
	s := NewClaudeSession(path, "haiku", "Traduza.")
	if _, err := s.Send("frase"); err != nil {
		t.Fatal(err)
	}
	waitStarts(t, read, 1)
	s.Close()
	if err := s.Start(); !errors.Is(err, ErrClaudeSessionClosed) {
		t.Fatalf("Start depois do Close: %v", err)
	}
	if got, err := s.Send("frase depois do Close"); !errors.Is(err, ErrClaudeSessionClosed) {
		t.Fatalf("Send depois do Close: %q, %v", got, err)
	}
	s.Close() // duas vezes não quebra
	time.Sleep(100 * time.Millisecond)
	if n := strings.Count(read("starts"), "processo"); n != 1 || s.cur != nil {
		t.Fatalf("nenhum processo deveria ser aberto depois do Close, há %d", n)
	}
}

// O Close não espera a mensagem em andamento (até Timeout): ele a interrompe.
func TestClaudeSessionCloseInterruptsSend(t *testing.T) {
	path, _ := fakeClaudeSession(t)
	s := NewClaudeSession(path, "haiku", "Traduza.")
	sent := make(chan error, 1)
	go func() {
		_, err := s.Send("frase lento") // o claude falso demora 2 s
		sent <- err
	}()
	time.Sleep(200 * time.Millisecond) // a mensagem já está com o claude
	started := time.Now()
	s.Close()
	if took := time.Since(started); took > 500*time.Millisecond {
		t.Fatalf("o Close não deveria esperar a resposta, levou %v", took)
	}
	select {
	case err := <-sent:
		if !errors.Is(err, ErrClaudeSessionClosed) {
			t.Fatalf("a mensagem interrompida deveria dizer que a conversa foi encerrada: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("o Send deveria voltar logo depois do Close")
	}
}

// Quem chamou desistiu (o prazo da frase): o Send volta na hora.
func TestClaudeSessionSendContext(t *testing.T) {
	path, read := fakeClaudeSession(t)
	s := NewClaudeSession(path, "haiku", "Traduza.")
	defer s.Close()
	if _, err := s.Send("frase"); err != nil { // o processo já está de pé
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := s.SendContext(ctx, "frase lento"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("o prazo deveria encerrar a espera: %v", err)
	}
	if took := time.Since(started); took > time.Second {
		t.Fatalf("deveria voltar no prazo, levou %v", took)
	}
	// O processo atrasado foi trocado por um novo, já aberto para a próxima
	waitStarts(t, read, 2)
	if got, err := s.Send("depois"); err != nil || !strings.HasPrefix(got, "resposta") {
		t.Fatalf("depois do prazo: %q, %v", got, err)
	}
	if n := strings.Count(read("starts"), "processo"); n != 2 {
		t.Fatalf("a mensagem seguinte deveria usar o processo já aberto, há %d", n)
	}

	// Cancelado (o Intérprete foi desligado): não abre outro
	ctx, cancel = context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if _, err := s.SendContext(ctx, "frase lento"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelado: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := strings.Count(read("starts"), "processo"); n != 2 || s.cur != nil {
		t.Fatalf("cancelado, não deveria abrir outro processo: há %d", n)
	}
}

// Chegando em MaxTurns, o processo novo é aberto logo depois da resposta, e
// não na mensagem seguinte (que esperaria o claude subir).
func TestClaudeSessionOpensTheNextProcessAhead(t *testing.T) {
	path, read := fakeClaudeSession(t)
	s := NewClaudeSession(path, "haiku", "Traduza.")
	defer s.Close()
	s.MaxTurns = 1
	if _, err := s.Send("frase"); err != nil {
		t.Fatal(err)
	}
	waitStarts(t, read, 2) // sem outro Send
	if s.cur == nil || s.cur.turns != 0 {
		t.Fatal("o processo novo deveria estar aberto, esperando a próxima mensagem")
	}
}

// O claude morreu entre uma frase e outra: a seguinte vai num processo novo,
// em vez de dar erro (e a frase ir sem correção).
func TestClaudeSessionRetriesWhenTheProcessDied(t *testing.T) {
	path, read := fakeClaudeSession(t)
	s := NewClaudeSession(path, "haiku", "Traduza.")
	defer s.Close()
	if _, err := s.Send("frase a"); err != nil {
		t.Fatal(err)
	}
	s.cur.cmd.Process.Kill()
	time.Sleep(200 * time.Millisecond)
	got, err := s.Send("frase b")
	if err != nil || !strings.HasPrefix(got, "resposta") {
		t.Fatalf("depois de o processo morrer: %q, %v", got, err)
	}
	if n := strings.Count(read("starts"), "processo"); n != 2 {
		t.Fatalf("deveria ter aberto um processo novo (2 no total), há %d", n)
	}
	if !strings.Contains(read("stdin"), "frase b") {
		t.Fatal("a mensagem deveria ter sido mandada de novo ao processo novo")
	}
}

// Com Prime, a mensagem de exemplo vai antes da primeira de verdade (que
// assim não paga a primeira resposta, mais lenta) e não conta para MaxTurns.
func TestClaudeSessionPrimesEachNewProcess(t *testing.T) {
	path, read := fakeClaudeSession(t)
	s := NewClaudeSession(path, "haiku", "Traduza.")
	defer s.Close()
	s.Prime = "frase de exemplo"
	s.MaxTurns = 2
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if got := read("stdin"); strings.Count(got, "frase de exemplo") != 1 {
		t.Fatalf("o Start deveria mandar a mensagem de exemplo: %q", got)
	}
	if err := s.Start(); err != nil || strings.Count(read("stdin"), "frase de exemplo") != 1 {
		t.Fatal("um segundo Start não manda a mensagem de exemplo de novo")
	}
	// A resposta do exemplo é descartada: a 1ª mensagem recebe a resposta dela
	if got, err := s.Send("frase a"); err != nil || got != "resposta 2" {
		t.Fatalf("1ª mensagem: %q, %v", got, err)
	}
	if n := strings.Count(read("starts"), "processo"); n != 1 {
		t.Fatalf("a mensagem de exemplo não conta para MaxTurns: 1 processo, há %d", n)
	}
	// A 2ª mensagem chega a MaxTurns: o processo novo abre e já recebe o exemplo
	if _, err := s.Send("frase b"); err != nil {
		t.Fatal(err)
	}
	waitStarts(t, read, 2)
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(read("stdin"), "frase de exemplo") < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := strings.Count(read("stdin"), "frase de exemplo"); n != 2 {
		t.Fatalf("o processo novo deveria receber a mensagem de exemplo: %d vezes", n)
	}
	time.Sleep(300 * time.Millisecond)
	if n := strings.Count(read("starts"), "processo"); n != 2 {
		t.Fatalf("o aquecimento não pode ficar abrindo processos: há %d", n)
	}
	if got, err := s.Send("frase c"); err != nil || !strings.HasPrefix(got, "resposta") {
		t.Fatalf("mensagem no processo novo: %q, %v", got, err)
	}
}

// Com MaxTurns 1 e Prime, cada mensagem abre um processo novo, aquecido: o
// aquecimento em si não pode disparar outra renovação.
func TestClaudeSessionPrimeDoesNotLoopOnRenewal(t *testing.T) {
	path, read := fakeClaudeSession(t)
	s := NewClaudeSession(path, "haiku", "Traduza.")
	defer s.Close()
	s.Prime = "frase de exemplo"
	s.MaxTurns = 1
	if _, err := s.Send("frase a"); err != nil {
		t.Fatal(err)
	}
	waitStarts(t, read, 2)
	time.Sleep(400 * time.Millisecond)
	if n := strings.Count(read("starts"), "processo"); n != 2 {
		t.Fatalf("esperava 2 processos (o da mensagem e o novo), há %d", n)
	}
}
