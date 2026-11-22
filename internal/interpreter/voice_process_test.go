//go:build !windows

package interpreter

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Endereço em que ninguém atende: o estado da voz vem só do processo.
const nobody = "http://127.0.0.1:1"

// fakePython põe um script de shell no lugar do Python da voz
// (dir/venv/bin/python) e marca a voz como instalada. O script recebe os
// argumentos do servidor de verdade: -I voice_server.py PORTA MODELO PASTA.
func fakePython(t *testing.T, dir, script string) {
	t.Helper()
	bin := filepath.Join(dir, "venv", "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "python"), []byte("#!/bin/sh\n"+script+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "venv", ".recorder-instalado"), nil, 0644); err != nil {
		t.Fatal(err)
	}
}

// untilRead espera o arquivo ter o conteúdo que ok aceita.
func untilRead(t *testing.T, path string, ok func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(path)
		if ok(string(b)) {
			return string(b)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s ficou em %q", filepath.Base(path), b)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// healthServer responde /health com o estado que state devolver.
func healthServer(t *testing.T, state func() VoiceHealth) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(state())
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestVoiceStartWaitsForThePreviousServerToExit(t *testing.T) {
	dir := t.TempDir()
	// Fica de pé até a entrada fechar (como o servidor de verdade) e ainda
	// demora 0,3 s para sair, com a "porta" ocupada
	fakePython(t, dir, `echo sobe >> "$5/vidas"; cat > /dev/null; sleep 0.3; echo sai >> "$5/vidas"`)
	v := NewVoice(dir)
	v.Endpoint = nobody
	var exits atomic.Int32
	v.OnExit = func(error) { exits.Add(1) }

	if h := v.Health(); h.State != "parado" || v.Running() {
		t.Fatalf("antes do Start: %+v", h)
	}
	if err := v.Start(); err != nil {
		t.Fatal(err)
	}
	if h := v.Health(); h.State != "carregando" || !v.Running() || v.Ready() {
		t.Fatalf("subindo o Python: %+v", h)
	}
	if err := v.Start(); err != nil { // já está de pé: não sobe outro
		t.Fatal(err)
	}
	vidas := filepath.Join(dir, "vidas")
	untilRead(t, vidas, func(s string) bool { return s == "sobe\n" })

	v.Stop()
	if h := v.Health(); h.State != "parado" || v.Running() {
		t.Fatalf("depois do Stop: %+v", h)
	}
	if err := v.Start(); err != nil { // o anterior ainda está saindo
		t.Fatal(err)
	}
	if got := untilRead(t, vidas, func(s string) bool { return strings.Count(s, "sobe") == 2 }); got != "sobe\nsai\nsobe\n" {
		t.Fatalf("o novo servidor deveria esperar o anterior sair, veio %q", got)
	}
	v.Stop()
	untilRead(t, vidas, func(s string) bool { return strings.Count(s, "sai") == 2 })
	if exits.Load() != 0 {
		t.Fatal("parar de propósito não é queda")
	}
}

func TestVoiceCrashIsReportedUntilTheNextStart(t *testing.T) {
	dir := t.TempDir()
	fakePython(t, dir, `echo $$ > "$5/pid"; exec cat > /dev/null`)
	ready := healthServer(t, func() VoiceHealth { return VoiceHealth{State: "pronto"} })
	v := NewVoice(dir)
	v.Endpoint = ready.URL
	crashed := make(chan error, 1)
	v.OnExit = func(err error) { crashed <- err }
	if err := v.Start(); err != nil {
		t.Fatal(err)
	}
	if h := v.Health(); h.State != "pronto" || !v.Ready() {
		t.Fatalf("a voz deveria estar pronta: %+v", h)
	}

	// Ocupado gerando uma fala, o servidor não responde ao /health: enquanto
	// o processo vive, ele continua pronto (o atalho não é recusado)
	ready.Close()
	if h := v.Health(); h.State != "pronto" || !v.Ready() {
		t.Fatalf("sem resposta, mas com o processo de pé, continua pronta: %+v", h)
	}

	// O processo morre sozinho (o macOS matou por memória, o MLX caiu)
	pid, _ := strconv.Atoi(strings.TrimSpace(untilRead(t, filepath.Join(dir, "pid"), func(s string) bool { return strings.TrimSpace(s) != "" })))
	if process, err := os.FindProcess(pid); err == nil {
		process.Kill()
	}
	select {
	case <-crashed:
	case <-time.After(5 * time.Second):
		t.Fatal("a queda do servidor deveria ser avisada (OnExit)")
	}
	if v.Ready() || v.Running() {
		t.Fatal("caído, não está pronto")
	}
	for _, h := range []VoiceHealth{v.LastHealth(), v.Health()} {
		if h.State != "erro" || !strings.Contains(h.Error, "parou de funcionar") || !strings.Contains(h.Error, "servidor.log") {
			t.Fatalf("a queda deveria aparecer como erro, com o motivo: %+v", h)
		}
	}

	// Subir de novo limpa o erro; parar de propósito é "parado"
	if err := v.Start(); err != nil {
		t.Fatal(err)
	}
	if h := v.Health(); h.State != "carregando" {
		t.Fatalf("subindo de novo: %+v", h)
	}
	v.Stop()
	if h := v.Health(); h.State != "parado" {
		t.Fatalf("parado de propósito: %+v", h)
	}
}

func TestVoiceLogKeepsWhatThePreviousServerPrinted(t *testing.T) {
	dir := t.TempDir()
	fakePython(t, dir, `echo "erro do MLX"; cat > /dev/null`)
	v := NewVoice(dir)
	v.Endpoint = nobody
	logPath := filepath.Join(dir, "servidor.log")
	for range 2 {
		if err := v.Start(); err != nil {
			t.Fatal(err)
		}
		v.Stop()
	}
	got := untilRead(t, logPath, func(s string) bool { return strings.Count(s, "erro do MLX") == 2 })
	if strings.Count(got, "servidor de voz iniciado") != 2 {
		t.Fatalf("cada início deveria ficar marcado no log: %q", got)
	}

	// Grande demais, o log vira servidor.log.1 e começa outro
	if err := os.WriteFile(logPath, make([]byte, maxVoiceLog+1), 0644); err != nil {
		t.Fatal(err)
	}
	if err := v.Start(); err != nil {
		t.Fatal(err)
	}
	v.Stop()
	if old, err := os.Stat(logPath + ".1"); err != nil || old.Size() != maxVoiceLog+1 {
		t.Fatalf("o log antigo deveria virar servidor.log.1: %v", err)
	}
	if cur, _ := os.Stat(logPath); cur.Size() > 1000 {
		t.Fatalf("o log novo deveria começar vazio, tem %d bytes", cur.Size())
	}
}

func TestSpeakOnlySucceedsWithTheEndFrame(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Text string }
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Text {
		case "inteira":
			writeFrame(w, 'A', pcm(1200, 1))
			writeFrame(w, 'A', nil) // um quadro vazio não vira pedaço
			writeFrame(w, 'A', pcm(1200, 2))
			writeFrame(w, 'F', nil)
		case "morre no meio": // o processo caiu: a conexão fecha sem o quadro de fim
			writeFrame(w, 'A', pcm(1200, 1))
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.(*net.TCPConn).Close()
		case "falha no meio": // o servidor avisa o erro da geração
			writeFrame(w, 'A', pcm(1200, 1))
			writeFrame(w, 'E', []byte("faltou memória no Metal"))
		case "sem quadros": // um servidor antigo, mandando o áudio cru
			w.Write(pcm(1200, 0))
		case "trava": // gera um pedaço e para de responder
			writeFrame(w, 'A', pcm(1200, 1))
			<-r.Context().Done()
		}
	}))
	defer srv.Close()
	v := NewVoice(t.TempDir())
	v.Endpoint = srv.URL
	speak := func(ctx context.Context, text string) (samples int, err error) {
		err = v.Speak(ctx, text, func(c []int16) { samples += len(c) })
		return samples, err
	}

	if n, err := speak(context.Background(), "inteira"); err != nil || n != 2400 {
		t.Fatalf("fala inteira: %d amostras, %v", n, err)
	}
	if n, err := speak(context.Background(), "morre no meio"); err == nil || !strings.Contains(err.Error(), "no meio da fala") || n != 1200 {
		t.Fatalf("a fala cortada deveria ser erro (com o áudio que chegou): %d amostras, %v", n, err)
	}
	if n, err := speak(context.Background(), "falha no meio"); err == nil || !strings.Contains(err.Error(), "faltou memória no Metal") || n != 1200 {
		t.Fatalf("o erro da geração deveria chegar: %d amostras, %v", n, err)
	}
	if _, err := speak(context.Background(), "sem quadros"); err == nil {
		t.Fatal("áudio fora do formato não é uma fala inteira")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if n, err := speak(ctx, "trava"); !errors.Is(err, context.DeadlineExceeded) || n != 1200 {
		t.Fatalf("o prazo de quem pediu deveria interromper a fala: %d amostras, %v", n, err)
	}
}

func TestWaitReady(t *testing.T) {
	states := []VoiceHealth{{State: "baixando", Progress: 40}, {State: "carregando", Progress: 100}, {State: "pronto"}}
	var asked atomic.Int32
	srv := healthServer(t, func() VoiceHealth {
		return states[min(int(asked.Add(1))-1, len(states)-1)]
	})
	v := NewVoice(t.TempDir())
	v.Endpoint = srv.URL
	var seen []string
	if err := v.WaitReady(context.Background(), time.Millisecond, func(h VoiceHealth) { seen = append(seen, h.State) }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, ",") != "baixando,carregando" || !v.Ready() {
		t.Fatalf("enquanto esperava: %q", seen)
	}

	// O servidor avisa um erro (ex: o download do modelo falhou)
	failed := NewVoice(t.TempDir())
	failed.Endpoint = healthServer(t, func() VoiceHealth { return VoiceHealth{State: "erro", Error: "sem internet"} }).URL
	if err := failed.WaitReady(context.Background(), time.Millisecond, nil); err == nil || !strings.Contains(err.Error(), "sem internet") {
		t.Fatalf("o erro da voz deveria encerrar a espera: %v", err)
	}

	// A voz foi desligada (ou nem subiu): não adianta esperar
	stopped := NewVoice(t.TempDir())
	stopped.Endpoint = nobody
	if err := stopped.WaitReady(context.Background(), time.Millisecond, nil); err == nil || !strings.Contains(err.Error(), "desligada") {
		t.Fatalf("com a voz parada a espera deveria acabar: %v", err)
	}

	// Nunca fica pronta: a espera acaba no prazo
	never := NewVoice(t.TempDir())
	never.Endpoint = healthServer(t, func() VoiceHealth { return VoiceHealth{State: "carregando"} }).URL
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := never.WaitReady(ctx, time.Millisecond, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a espera deveria acabar no prazo: %v", err)
	}
}

func TestInstallGivesUpOnAStuckUV(t *testing.T) {
	if !Supported() {
		t.Skip("a voz só instala em Mac com Apple Silicon")
	}
	bin := t.TempDir()
	uv := filepath.Join(bin, "uv")
	t.Setenv("PATH", bin+":/usr/bin:/bin")

	// O uv travado (a rede parou no meio do download)
	if err := os.WriteFile(uv, []byte("#!/bin/sh\nexec sleep 30\n"), 0755); err != nil {
		t.Fatal(err)
	}
	v := NewVoice(t.TempDir())
	v.InstallTimeout = 300 * time.Millisecond
	started := time.Now()
	err := v.Install(func(string) {})
	if err == nil || !strings.Contains(err.Error(), "não terminou") {
		t.Fatalf("a instalação travada deveria desistir: %v", err)
	}
	if time.Since(started) > 5*time.Second || v.Installed() {
		t.Fatalf("deveria desistir no prazo (levou %v) e não marcar como instalada", time.Since(started))
	}

	// O uv que termina: a voz fica instalada
	if err := os.WriteFile(uv, []byte("#!/bin/sh\nmkdir -p \"$(dirname \"$4\")\" 2>/dev/null\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(v.Dir, "venv"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := v.Install(func(string) {}); err != nil || !v.Installed() {
		t.Fatalf("instalação: %v", err)
	}
}
