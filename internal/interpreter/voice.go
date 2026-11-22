package interpreter

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"recorder/internal/datadir"
)

// A voz em inglês do usuário vem de um servidor local em Python (Qwen3-TTS
// via MLX), instalado pelo app em ~/.recorder/voz com o uv: venv/ (o
// ambiente), hf/ (o modelo, ~4 GB), amostra.wav e amostra.txt (a amostra da
// voz e o texto dela), voice_server.py (escrito pelo app a cada início) e
// servidor.log (o que o servidor imprime).

//go:embed voice_server.py
var voiceServerScript []byte

const (
	// VoicePort é a porta do servidor de voz, só em 127.0.0.1
	VoicePort = "47824"
	// VoiceModel é o modelo de voz: o 1.7B foi o que mais pareceu com o
	// usuário na prova de conceito (o 0.6B é mais rápido, mas menos parecido)
	VoiceModel = "mlx-community/Qwen3-TTS-12Hz-1.7B-Base-bf16"
	// VoiceModelRevision é a revisão do repositório do modelo que é baixada
	// (a de 25/01/2026). Presa, para o conteúdo ser sempre o que foi testado:
	// sem ela, quem pudesse escrever no repositório mudaria o que roda aqui
	VoiceModelRevision = "a6eb4f68e4b056f1215157bb696209bc82a6db48"
	// mlx-audio testado na prova de conceito
	mlxAudioVersion = "0.6.4"
	// SpeechRate é a taxa do áudio devolvido pelo servidor
	SpeechRate = 24000

	// installTimeout é o prazo de cada passo do uv (o maior baixa uns 500 MB)
	installTimeout = 30 * time.Minute
	// stopKillAfter é quanto o Stop espera o servidor sair sozinho antes de matá-lo
	stopKillAfter = 3 * time.Second
	// maxVoiceLog é o tamanho a partir do qual o servidor.log vira servidor.log.1
	maxVoiceLog = 1 << 20
)

// Supported diz se a voz roda neste computador: o MLX só existe para Mac
// com Apple Silicon.
func Supported() bool {
	return runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
}

// Voice gerencia o servidor de voz.
type Voice struct {
	Dir      string // ~/.recorder/voz
	Endpoint string // http://127.0.0.1:47824
	// OnExit, se definido (antes do Start), é chamado quando o servidor
	// termina sem o Stop ter pedido: ele caiu
	OnExit func(err error)
	// InstallTimeout é o prazo de cada passo da instalação (zero: installTimeout)
	InstallTimeout time.Duration

	starting sync.Mutex // um Start de cada vez

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	done    chan struct{} // fecha quando o servidor (o atual ou o último) termina
	crashed string        // por que o servidor caiu, até o próximo Start ou Stop
	last    VoiceHealth   // o último estado visto (LastHealth)
	epoch   int           // muda a cada Start, Stop e queda
	// token é o segredo que os pedidos levam ao servidor, sorteado a cada
	// Start: sem ele o servidor não fala (ver o começo do voice_server.py)
	token string
}

func NewVoice(dir string) *Voice {
	return &Voice{Dir: dir, Endpoint: "http://127.0.0.1:" + VoicePort, last: VoiceHealth{State: "parado"}}
}

// DefaultVoiceDir é ~/.recorder/voz.
func DefaultVoiceDir() string {
	return datadir.Voice()
}

func (v *Voice) python() string {
	return filepath.Join(v.Dir, "venv", "bin", "python")
}

func (v *Voice) installedMarker() string {
	return filepath.Join(v.Dir, "venv", ".recorder-instalado")
}

// Installed diz se o ambiente da voz já foi instalado.
func (v *Voice) Installed() bool {
	_, err := os.Stat(v.installedMarker())
	return err == nil
}

// FindUV procura o uv, que instala o ambiente Python da voz; "" se não achar.
func FindUV() string {
	if p, err := exec.LookPath("uv"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{"/opt/homebrew/bin/uv", "/usr/local/bin/uv",
		filepath.Join(home, ".local", "bin", "uv"), filepath.Join(home, ".cargo", "bin", "uv")} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Install cria o ambiente Python da voz com o uv (Python 3.12 e mlx-audio).
// O modelo é baixado depois, na primeira vez que o servidor sobe. progress
// recebe o que está sendo feito.
func (v *Voice) Install(progress func(string)) error {
	if !Supported() {
		return fmt.Errorf("a voz só funciona em Mac com Apple Silicon")
	}
	uv := FindUV()
	if uv == "" {
		return fmt.Errorf("o uv não foi encontrado; instale com: brew install uv")
	}
	if err := os.MkdirAll(v.Dir, 0755); err != nil {
		return err
	}
	timeout := v.InstallTimeout
	if timeout == 0 {
		timeout = installTimeout
	}
	venv := filepath.Join(v.Dir, "venv")
	steps := []struct {
		msg  string
		args []string
	}{
		{"Criando o ambiente Python da voz...", []string{"venv", "--allow-existing", "--python", "3.12", venv}},
		{"Instalando o modelo de voz (mlx-audio)...", []string{"pip", "install", "--python", v.python(), "mlx-audio==" + mlxAudioVersion}},
	}
	for _, step := range steps {
		progress(step.msg)
		// Com prazo: um uv travado (rede parada) deixava a tela em
		// "instalando" para sempre, sem como tentar de novo
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		cmd := exec.CommandContext(ctx, uv, step.args...)
		cmd.Dir = v.Dir
		cmd.WaitDelay = 5 * time.Second // não espera os filhos do uv que ficarem com a saída aberta
		out, err := cmd.CombinedOutput()
		timedOut := ctx.Err() != nil
		cancel()
		what := strings.TrimSuffix(step.msg, "...")
		if timedOut {
			return fmt.Errorf("%s não terminou em %v (a internet está lenta ou parada?); tente de novo\n%s", what, timeout, tail(string(out), 1500))
		}
		if err != nil {
			return fmt.Errorf("%s falhou: %v\n%s", what, err, tail(string(out), 1500))
		}
	}
	return os.WriteFile(v.installedMarker(), []byte(mlxAudioVersion+"\n"), 0644)
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// openLog abre o servidor.log para acrescentar: o que o servidor imprimiu
// antes de cair continua lá depois de ele subir de novo. Passando de
// maxVoiceLog, o arquivo vira servidor.log.1 (um só).
func (v *Voice) openLog() *os.File {
	path := filepath.Join(v.Dir, "servidor.log")
	if info, err := os.Stat(path); err == nil && info.Size() > maxVoiceLog {
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil
	}
	fmt.Fprintf(f, "\n--- %s: servidor de voz iniciado ---\n", time.Now().Format("2006-01-02 15:04:05"))
	return f
}

// Start sobe o servidor de voz, se ainda não estiver rodando. Ele baixa o
// modelo na primeira vez e carrega em segundo plano: Health diz quando ficou
// pronto.
func (v *Voice) Start() error {
	v.starting.Lock()
	defer v.starting.Unlock()
	v.mu.Lock()
	running, previous := v.cmd != nil, v.done
	v.mu.Unlock()
	if running {
		return nil
	}
	if !v.Installed() {
		return fmt.Errorf("a voz ainda não foi instalada")
	}
	// O servidor anterior pode ainda estar saindo (o Stop não espera): a
	// porta é a mesma, e o novo não conseguiria abri-la
	if previous != nil {
		select {
		case <-previous:
		case <-time.After(stopKillAfter + 2*time.Second):
		}
	}
	script := filepath.Join(v.Dir, "voice_server.py")
	if err := os.WriteFile(script, voiceServerScript, 0644); err != nil {
		return err
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("não foi possível sortear o segredo do servidor de voz: %w", err)
	}
	token := hex.EncodeToString(secret)
	cmd := exec.Command(v.python(), "-I", script, VoicePort, VoiceModel+"@"+VoiceModelRevision, v.Dir)
	cmd.Dir = v.Dir
	// O segredo vai no ambiente: os argumentos aparecem para qualquer programa
	cmd.Env = append(os.Environ(), "HF_HOME="+filepath.Join(v.Dir, "hf"), "HF_HUB_DISABLE_TELEMETRY=1",
		"TOKENIZERS_PARALLELISM=false", voiceTokenEnv+"="+token)
	// Com o modelo no disco, nada de internet: o mlx-audio consulta o Hugging
	// Face ao carregar e, sem conexão, a voz não subiria
	if v.ModelDownloaded() {
		cmd.Env = append(cmd.Env, "HF_HUB_OFFLINE=1")
	}
	logFile := v.openLog()
	if logFile != nil {
		cmd.Stdout, cmd.Stderr = logFile, logFile
	}
	closeLog := func() {
		if logFile != nil {
			logFile.Close()
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		closeLog()
		return err
	}
	if err := cmd.Start(); err != nil {
		closeLog()
		return fmt.Errorf("não foi possível subir o servidor de voz: %w", err)
	}
	done := make(chan struct{})
	v.mu.Lock()
	v.cmd, v.stdin, v.done = cmd, stdin, done
	v.token = token
	v.crashed, v.last = "", VoiceHealth{State: "carregando"}
	v.epoch++
	v.mu.Unlock()
	go func() {
		err := cmd.Wait()
		closeLog()
		v.mu.Lock()
		crashed := v.cmd == cmd // ninguém pediu (Stop): o servidor caiu
		if crashed {
			v.cmd, v.stdin = nil, nil
			reason := "saiu sozinho"
			if err != nil {
				reason = err.Error()
			}
			v.crashed = fmt.Sprintf("o servidor de voz parou de funcionar (%s); o que ele imprimiu está em %s",
				reason, filepath.Join(v.Dir, "servidor.log"))
			v.last = VoiceHealth{State: "erro", Error: v.crashed}
			v.epoch++
		}
		onExit := v.OnExit
		v.mu.Unlock()
		close(done)
		if crashed && onExit != nil {
			onExit(err)
		}
	}()
	return nil
}

// Stop encerra o servidor de voz. Não espera ele sair: o próximo Start espera.
func (v *Voice) Stop() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.crashed, v.last = "", VoiceHealth{State: "parado"}
	v.epoch++
	if v.cmd == nil {
		return
	}
	v.stdin.Close() // o servidor sai quando a entrada fecha
	cmd, done := v.cmd, v.done
	go func() {
		select {
		case <-done:
		case <-time.After(stopKillAfter):
			cmd.Process.Kill()
		}
	}()
	v.cmd, v.stdin = nil, nil
}

// Running diz se o processo do servidor está de pé (pronto ou ainda carregando).
func (v *Voice) Running() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.cmd != nil
}

// VoiceHealth é o estado do servidor de voz.
type VoiceHealth struct {
	State    string `json:"state"` // "parado", "baixando", "carregando", "pronto" ou "erro"
	Progress int    `json:"progress"`
	Error    string `json:"error"`
}

// Health consulta o servidor de voz. Se ele caiu, o estado é "erro" (com o
// motivo) até o próximo Start ou Stop; "parado" é só o desligado de propósito.
func (v *Voice) Health() VoiceHealth {
	v.mu.Lock()
	epoch := v.epoch
	v.mu.Unlock()
	h, answered := v.ask()
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.epoch != epoch {
		return v.last // subiu, parou ou caiu durante a consulta: vale o que aconteceu
	}
	if answered && v.cmd == nil && v.exiting() {
		// Quem respondeu foi o servidor que o Stop mandou sair, nos instantes
		// que ele leva para terminar
		answered = false
	}
	if !answered {
		switch {
		case v.cmd != nil && v.last.State == "pronto":
			// O servidor só deixa de estar pronto morrendo: não respondeu a
			// tempo porque está ocupado gerando uma fala
			h = v.last
		case v.cmd != nil:
			h = VoiceHealth{State: "carregando"} // ainda subindo o Python
		case v.crashed != "":
			h = VoiceHealth{State: "erro", Error: v.crashed}
		default:
			h = VoiceHealth{State: "parado"}
		}
	}
	v.last = h
	return h
}

// exiting diz se o último servidor ainda não terminou. Chamado com v.mu.
func (v *Voice) exiting() bool {
	if v.done == nil {
		return false
	}
	select {
	case <-v.done:
		return false
	default:
		return true
	}
}

func (v *Voice) ask() (h VoiceHealth, answered bool) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(v.Endpoint + "/health")
	if err != nil {
		return VoiceHealth{}, false
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return VoiceHealth{State: "erro", Error: err.Error()}, true
	}
	return h, true
}

// LastHealth é o último estado visto, sem consultar o servidor: para o
// aperto do atalho, que não pode esperar uma resposta HTTP.
func (v *Voice) LastHealth() VoiceHealth {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.last
}

// Ready diz se a voz estava pronta da última vez que foi vista (LastHealth).
func (v *Voice) Ready() bool {
	return v.LastHealth().State == "pronto"
}

// WaitReady espera o servidor ficar pronto, consultando a cada every. onWait
// (pode ser nil) recebe o estado enquanto ele baixa ou carrega. Desiste com
// erro se o servidor der erro, cair ou for desligado, ou quando ctx terminar.
func (v *Voice) WaitReady(ctx context.Context, every time.Duration, onWait func(VoiceHealth)) error {
	for {
		h := v.Health()
		switch h.State {
		case "pronto":
			return nil
		case "erro":
			return fmt.Errorf("erro na voz: %s", h.Error)
		case "parado":
			return fmt.Errorf("a voz foi desligada antes de ficar pronta")
		}
		if onWait != nil {
			onWait(h)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("a voz não ficou pronta a tempo: %w", ctx.Err())
		case <-time.After(every):
		}
	}
}

// Warmup prepara o modelo para as frases reais (ex: depois de gravar uma
// amostra nova).
func (v *Voice) Warmup() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := v.post(ctx, "/warmup", []byte("{}"))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return serverError(resp)
	}
	return nil
}

// voiceTokenEnv é a variável de ambiente que leva o segredo ao servidor, e
// voiceTokenHeader, o cabeçalho que o traz de volta em cada pedido.
const (
	voiceTokenEnv    = "RECORDER_VOICE_TOKEN"
	voiceTokenHeader = "X-Recorder-Token"
)

// post monta um pedido ao servidor de voz, em JSON e com o segredo dele.
func (v *Voice) post(ctx context.Context, path string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", v.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	token := v.token
	v.mu.Unlock()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(voiceTokenHeader, token)
	return req, nil
}

// A resposta do /speak vem em quadros: 1 byte com o tipo, 4 com o tamanho
// (little-endian) e o conteúdo. Sem o quadro de fim, a fala foi cortada (o
// servidor morreu ou falhou no meio); antes, a conexão fechando era o fim, e
// meia frase passava por frase inteira.
const (
	frameAudio = 'A' // PCM int16 mono, 24 kHz
	frameEnd   = 'F' // a fala acabou
	frameError = 'E' // a geração falhou: o conteúdo é o erro
	maxFrame   = 8 << 20
)

// Speak fala text com a voz do usuário. onChunk recebe o áudio (int16, 24
// kHz) enquanto ele é gerado, um pedaço por quadro do servidor (~0,3 s).
func (v *Voice) Speak(ctx context.Context, text string, onChunk func([]int16)) error {
	return v.SpeakWith(ctx, text, "", "", onChunk)
}

// SpeakWith é como Speak, mas imitando o trecho refWav (com o texto refText),
// que precisa estar na pasta da voz, em vez da amostra escolhida.
func (v *Voice) SpeakWith(ctx context.Context, text, refWav, refText string, onChunk func([]int16)) error {
	req := map[string]string{"text": text}
	if refWav != "" {
		req["ref_audio"], req["ref_text"] = refWav, refText
	}
	body, _ := json.Marshal(req)
	httpReq, err := v.post(ctx, "/speak", body)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("o servidor de voz não respondeu: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return serverError(resp)
	}
	cut := func(err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("o servidor de voz parou no meio da fala")
		}
		return fmt.Errorf("o servidor de voz parou no meio da fala: %w", err)
	}
	r := bufio.NewReader(resp.Body)
	var header [5]byte
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return cut(err)
		}
		size := binary.LittleEndian.Uint32(header[1:])
		if size > maxFrame {
			return fmt.Errorf("servidor de voz: quadro de %d bytes (resposta fora do formato)", size)
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(r, payload); err != nil {
			return cut(err)
		}
		switch header[0] {
		case frameAudio:
			samples := make([]int16, len(payload)/2)
			for k := range samples {
				samples[k] = int16(binary.LittleEndian.Uint16(payload[2*k:]))
			}
			if len(samples) > 0 {
				onChunk(samples)
			}
		case frameEnd:
			return nil
		case frameError:
			return fmt.Errorf("servidor de voz: %s", payload)
		default:
			return fmt.Errorf("servidor de voz: quadro desconhecido %q (resposta fora do formato)", header[0])
		}
	}
}

func serverError(resp *http.Response) error {
	var e struct {
		Error string `json:"error"`
	}
	b, _ := io.ReadAll(resp.Body)
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return fmt.Errorf("servidor de voz: %s", e.Error)
	}
	return fmt.Errorf("servidor de voz: HTTP %d", resp.StatusCode)
}

// ModelDownloaded diz se o modelo de voz já está no disco.
func (v *Voice) ModelDownloaded() bool {
	repo := "models--" + strings.ReplaceAll(VoiceModel, "/", "--")
	weights := filepath.Join(v.Dir, "hf", "hub", repo, "snapshots", VoiceModelRevision, "model.safetensors")
	info, err := os.Stat(weights) // Stat segue o link
	return err == nil && info.Size() > 0
}

// HasSample diz se a amostra da voz já foi gravada.
func (v *Voice) HasSample() bool {
	_, errWav := os.Stat(filepath.Join(v.Dir, "amostra.wav"))
	_, errTxt := os.Stat(filepath.Join(v.Dir, "amostra.txt"))
	return errWav == nil && errTxt == nil
}

// SampleText devolve o texto da amostra gravada ("" se não houver).
func (v *Voice) SampleText() string {
	b, _ := os.ReadFile(filepath.Join(v.Dir, "amostra.txt"))
	return strings.TrimSpace(string(b))
}

// SaveSample guarda a amostra da voz (mono, 16 bits) e o texto exato dela.
func (v *Voice) SaveSample(samples []int16, rate int, text string) error {
	if err := os.MkdirAll(v.Dir, 0755); err != nil {
		return err
	}
	if err := writeWav(filepath.Join(v.Dir, "amostra.wav"), samples, rate); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(v.Dir, "amostra.txt"), []byte(strings.TrimSpace(text)+"\n"), 0644)
}
