package interpreter

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// voiceServerDriver sobe o voice_server.py de verdade (o mesmo arquivo que
// vai embutido no app), só que com um gerador falso no lugar do modelo: sem o
// MLX nem os 4 GB, dá para testar o que o servidor responde. Uso:
// python3 -I driver.py voice_server.py PASTA_DA_VOZ; imprime a porta.
const voiceServerDriver = `
import importlib.util
import sys
import threading

spec = importlib.util.spec_from_file_location("voice_server", sys.argv[1])
vs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(vs)
vs.VOICE_DIR = sys.argv[2]


def fake_generate(text, on_chunk, ref=None):
    on_chunk(b"\x01\x00" * 1200)
    if "quebra" in text:
        raise RuntimeError("faltou memória no Metal")
    on_chunk(b"\x02\x00" * 1200)


vs.generate = fake_generate
vs.TOKEN = "segredo-do-teste"
vs.model = object()
vs.state.update(state="pronto")
threading.Thread(target=vs.exit_with_app, daemon=True).start()
server = vs.serve(0)
print(server.server_address[1], flush=True)
server.serve_forever()
`

func TestVoiceServerScript(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("sem python3 para rodar o voice_server.py")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "voice_server.py")
	driver := filepath.Join(dir, "driver.py")
	for path, content := range map[string][]byte{
		script: voiceServerScript, driver: []byte(voiceServerDriver),
		filepath.Join(dir, "amostra.wav"): wavBytes(make([]int16, 2400), SpeechRate),
		filepath.Join(dir, "amostra.txt"): []byte("Hi everyone.\n"),
	} {
		if err := os.WriteFile(path, content, 0644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(python, "-I", driver, script, dir)
	// O que o servidor imprime vai para um arquivo, como no app (servidor.log)
	logFile, err := os.Create(filepath.Join(dir, "servidor.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd.Stderr = logFile
	printed := func() string {
		b, _ := os.ReadFile(logFile.Name())
		return string(b)
	}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	defer func() {
		cmd.Process.Kill()
		<-exited
	}()
	port := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		port <- strings.TrimSpace(line)
		io.Copy(io.Discard, stdout)
		exited <- cmd.Wait()
	}()
	v := NewVoice(dir)
	select {
	case p := <-port:
		if p == "" {
			t.Fatalf("o servidor não subiu: %s", printed())
		}
		v.Endpoint = "http://127.0.0.1:" + p
		v.token = "segredo-do-teste"
	case <-time.After(20 * time.Second):
		t.Fatalf("o servidor não subiu: %s", printed())
	}
	speak := func(text, refWav string) (samples []int16, err error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = v.SpeakWith(ctx, text, refWav, "texto do trecho", func(c []int16) { samples = append(samples, c...) })
		return samples, err
	}

	if h := v.Health(); h.State != "pronto" {
		t.Fatalf("health: %+v", h)
	}
	// A fala inteira chega em quadros e termina com o quadro de fim
	got, err := speak("Hello there.", "")
	if err != nil || len(got) != 2400 || got[0] != 1 || got[2399] != 2 {
		t.Fatalf("fala inteira: %d amostras, %v", len(got), err)
	}
	// A geração falha no meio: o erro chega ao app, com o áudio que já tinha saído
	got, err = speak("esta quebra no meio", "")
	if err == nil || !strings.Contains(err.Error(), "faltou memória no Metal") || len(got) != 1200 {
		t.Fatalf("o erro da geração deveria chegar ao app: %d amostras, %v", len(got), err)
	}
	if !strings.Contains(printed(), "RuntimeError") {
		t.Fatalf("o erro deveria ir também para o log do servidor: %q", printed())
	}
	// Depois do erro o servidor continua atendendo
	if got, err = speak("Still here.", ""); err != nil || len(got) != 2400 {
		t.Fatalf("depois do erro: %d amostras, %v", len(got), err)
	}
	// A referência precisa estar na pasta da voz
	inside := filepath.Join(dir, "calibracao.nova", "opcao-1.wav")
	os.MkdirAll(filepath.Dir(inside), 0755)
	os.WriteFile(inside, wavBytes(make([]int16, 2400), SpeechRate), 0644)
	if got, err = speak("Hello there.", inside); err != nil || len(got) != 2400 {
		t.Fatalf("referência da calibração: %d amostras, %v", len(got), err)
	}
	outside := filepath.Join(t.TempDir(), "fora.wav")
	os.WriteFile(outside, wavBytes(make([]int16, 2400), SpeechRate), 0644)
	for _, ref := range []string{outside, filepath.Join(dir, "..", filepath.Base(filepath.Dir(outside)), "fora.wav"), filepath.Join(dir, "nao-existe.wav")} {
		if _, err = speak("Hello there.", ref); err == nil || !strings.Contains(err.Error(), "referência inválido") {
			t.Fatalf("referência %s deveria ser recusada: %v", ref, err)
		}
	}
	// Só o app fala com a voz do usuário. Um pedido sem o segredo, com o
	// segredo errado, que não se diz JSON (o que uma página do navegador
	// consegue mandar sem pedir permissão) ou feito por outro nome é recusado,
	// sem gerar fala nem derrubar o servidor
	post := func(contentType, token, host, body string) int {
		req, err := http.NewRequest("POST", v.Endpoint+"/speak", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", contentType)
		if token != "" {
			req.Header.Set(voiceTokenHeader, token)
		}
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	hello := `{"text": "Hello there."}`
	for _, c := range []struct {
		what, contentType, token, host, body string
		want                                 int
	}{
		{"o pedido do app", "application/json", "segredo-do-teste", "", hello, http.StatusOK},
		{"sem o segredo", "application/json", "", "", hello, http.StatusForbidden},
		{"com o segredo errado", "application/json", "outro-segredo", "", hello, http.StatusForbidden},
		{"como texto puro", "text/plain", "segredo-do-teste", "", hello, http.StatusForbidden},
		{"feito por outro nome", "application/json", "segredo-do-teste", "atacante.example:47824", hello, http.StatusForbidden},
		{"que não é JSON", "application/json", "segredo-do-teste", "", "oi", http.StatusBadRequest},
		{"grande demais", "application/json", "segredo-do-teste", "", `{"text": "` + strings.Repeat("a", 70000) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		if got := post(c.contentType, c.token, c.host, c.body); got != c.want {
			t.Errorf("pedido %s: status %d, esperava %d", c.what, got, c.want)
		}
	}

	// O app terminou (a entrada fechou): o servidor sai sozinho
	stdin.Close()
	select {
	case err := <-exited:
		exited <- err // para o defer
	case <-time.After(10 * time.Second):
		t.Fatal("com a entrada fechada o servidor deveria sair")
	}
	if h := v.Health(); h.State != "parado" {
		t.Fatalf("depois de sair: %+v", h)
	}
}

// O voice_server.py como o app o sobe (python -I voice_server.py PORTA MODELO
// PASTA), mas sem o ambiente da voz: o modelo não carrega, e ainda assim o
// servidor atende, diz o erro e sai quando o app termina.
func TestVoiceServerStartsAnswersAndExitsWithTheApp(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("sem python3 para rodar o voice_server.py")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "voice_server.py")
	if err := os.WriteFile(script, voiceServerScript, 0644); err != nil {
		t.Fatal(err)
	}
	// Uma porta livre
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	l.Close()

	cmd := exec.Command(python, "-I", script, port, "recorder-teste/modelo-que-nao-existe@0000000000000000000000000000000000000000", dir)
	cmd.Env = append(os.Environ(), "HF_HUB_OFFLINE=1", "HF_HOME="+filepath.Join(dir, "hf"), voiceTokenEnv+"=segredo-do-teste") // nada de internet
	stdin, _ := cmd.StdinPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer cmd.Process.Kill()

	v := NewVoice(dir)
	v.Endpoint = "http://127.0.0.1:" + port
	v.token = "segredo-do-teste"
	var h VoiceHealth
	deadline := time.Now().Add(20 * time.Second)
	for h = v.Health(); h.State != "erro"; h = v.Health() { // sem o mlx-audio, o modelo não carrega
		if time.Now().After(deadline) {
			t.Fatalf("o servidor deveria atender e dizer que o modelo não carregou: %+v", h)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if h.Error == "" {
		t.Fatalf("o erro da carga deveria vir no /health: %+v", h)
	}
	// Sem o modelo, a fala é recusada com o motivo (o erro da carga)
	err = v.Speak(context.Background(), "Hello there.", func([]int16) {})
	if err == nil || !strings.Contains(err.Error(), h.Error) {
		t.Fatalf("sem o modelo a fala deveria ser recusada com o erro da carga (%s): %v", h.Error, err)
	}

	stdin.Close() // o app terminou
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("com a entrada fechada o servidor deveria sair")
	}
}
