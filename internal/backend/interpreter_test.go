package backend

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"recorder/internal/datadir"
	"recorder/internal/interpreter"
)

func TestInterpreterNeedsAnOpenMeeting(t *testing.T) {
	a, _ := testApp(t)
	if err := a.SetInterpreterEnabled(true); err == nil || !strings.Contains(err.Error(), "reunião") {
		t.Fatalf("sem reunião aberta o Intérprete não deveria ligar, veio %v", err)
	}
	// Sem o Intérprete ligado, o atalho não faz nada (nem quebra)
	a.InterpreterTalk(true)
	a.InterpreterTalk(false)

	a.SetInterpreterSession("sessao-1")
	if err := a.SetInterpreterEnabled(true); err != nil && strings.Contains(err.Error(), "reunião") {
		t.Fatalf("com reunião aberta, o motivo do erro não é a reunião: %v", err)
	}
	a.stopInterpreter()
}

// fakeRouter é o roteador sem os dispositivos de áudio.
type fakeRouter struct {
	onTalk func([]int16) // o que o microfone entregaria

	mu      sync.Mutex
	talking bool
	busy    bool
	closed  bool
}

func (r *fakeRouter) SetTalking(on bool)     { r.mu.Lock(); r.talking = on; r.mu.Unlock() }
func (r *fakeRouter) SetBusy(on bool)        { r.mu.Lock(); r.busy = on; r.mu.Unlock() }
func (r *fakeRouter) Enqueue([]int16)        {}
func (r *fakeRouter) Pending() time.Duration { return 0 }
func (r *fakeRouter) Close()                 { r.mu.Lock(); r.closed = true; r.mu.Unlock() }
func (r *fakeRouter) Names() (string, string) {
	return "Microfone de teste", "BlackHole 2ch"
}

func (r *fakeRouter) is(what *bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *what
}

// interpreterRig é o Intérprete do App sem tocar no sistema: roteador,
// Whisper e atalho falsos, e um script de shell no lugar do Python da voz.
type interpreterRig struct {
	a      *App
	it     *interp
	dir    string // a pasta da voz
	router *fakeRouter

	routerErr, whisperErr error // para o ligar falhar
	whisperStarts         atomic.Int32
	micLookups            atomic.Int32
	hotkey                atomic.Bool

	// O que o /health do "servidor de voz" responde; "" derruba a conexão,
	// como o servidor que não está de pé
	health atomic.Value
}

// newInterpreterRig prepara o App para ligar o Intérprete. python é o script
// (de shell) que faz as vezes do servidor de voz: recebe os argumentos dele
// (-I voice_server.py PORTA MODELO PASTA) e normalmente fica de pé até a
// entrada fechar (exec cat > /dev/null).
func newInterpreterRig(t *testing.T, python string) *interpreterRig {
	t.Helper()
	if !interpreter.Supported() {
		t.Skip("o Intérprete só existe em Mac com Apple Silicon")
	}
	a, store := testApp(t)
	// Sem correção: a padrão subiria o claude de verdade
	if err := store.SetSetting("interpreter_correction", interpreter.CorrectionNone); err != nil {
		t.Fatal(err)
	}
	rig := &interpreterRig{a: a, dir: datadir.Voice()}
	rig.health.Store("carregando")
	bin := filepath.Join(rig.dir, "venv", "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(bin, "python"):                          "#!/bin/sh\n" + python + "\n",
		filepath.Join(rig.dir, "venv", ".recorder-instalado"): "",
		filepath.Join(rig.dir, "amostra.wav"):                 "RIFF",
		filepath.Join(rig.dir, "amostra.txt"):                 "Hi everyone.\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}

	voice := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case "/health":
			state := rig.health.Load().(string)
			if state == "" {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
			json.NewEncoder(w).Encode(interpreter.VoiceHealth{State: state})
		case "/speak": // 0,1 s de áudio e o quadro de fim
			frame := func(kind byte, payload []byte) {
				header := []byte{kind, 0, 0, 0, 0}
				binary.LittleEndian.PutUint32(header[1:], uint32(len(payload)))
				w.Write(append(header, payload...))
			}
			frame('A', make([]byte, 4800))
			frame('F', nil)
		case "/warmup":
			w.Write([]byte(`{"ok": true}`))
		}
	}))
	t.Cleanup(voice.Close)

	rig.it = a.interp()
	it := rig.it
	it.voice.Endpoint = voice.URL
	it.voicePoll = 10 * time.Millisecond
	it.startRouter = func(onTalk func([]int16)) (audioRouter, error) {
		if rig.routerErr != nil {
			return nil, rig.routerErr
		}
		rig.router = &fakeRouter{onTalk: onTalk}
		return rig.router, nil
	}
	it.startWhisper = func() (*exec.Cmd, error) {
		rig.whisperStarts.Add(1)
		return nil, rig.whisperErr
	}
	it.whisperURL = "http://127.0.0.1:1" // ninguém atende
	it.virtualMic = func() string {
		rig.micLookups.Add(1)
		return "BlackHole 2ch"
	}
	it.pushToTalk = func(on bool, _ func(bool)) { rig.hotkey.Store(on) }
	a.SetInterpreterSession("sessao-1")
	t.Cleanup(func() {
		a.stopInterpreter()
		it.voice.Stop()
	})
	return rig
}

const standingPython = `echo $$ > "$5/pid"; echo sobe >> "$5/vidas"; exec cat > /dev/null`

// until espera ok ficar verdadeiro.
func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("esperava: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// speech16k é fala a 16 kHz (varia de volume como fala).
func speech16k(seconds float64) []int16 {
	out := make([]int16, int(seconds*16000))
	for i := range out {
		amp := 8000.0
		if (i/2400)%2 == 1 {
			amp /= 5
		}
		out[i] = int16(amp * math.Sin(float64(i)*2*math.Pi*220/16000))
	}
	return out
}

// Sem o BlackHole não adianta subir o Whisper e a voz: os dispositivos são
// conferidos antes.
func TestInterpreterChecksTheDevicesBeforeStartingAnything(t *testing.T) {
	rig := newInterpreterRig(t, standingPython)
	rig.routerErr = fmt.Errorf("o microfone virtual (BlackHole) não está instalado")
	if err := rig.a.SetInterpreterEnabled(true); err == nil || !strings.Contains(err.Error(), "BlackHole") {
		t.Fatalf("sem o BlackHole o Intérprete não liga: %v", err)
	}
	if rig.whisperStarts.Load() != 0 || rig.it.voice.Running() || rig.hotkey.Load() {
		t.Fatal("nada deveria ter subido antes de conferir os dispositivos")
	}
	if info := rig.a.GetInterpreterInfo(); info.Enabled || info.VirtualMic != "BlackHole 2ch" {
		t.Fatalf("o Intérprete deveria continuar desligado: %+v", info)
	}
}

func TestInterpreterUndoesWhatItStartedWhenWhisperFails(t *testing.T) {
	rig := newInterpreterRig(t, standingPython)
	rig.whisperErr = fmt.Errorf("a porta 47825 está ocupada por outro programa")
	if err := rig.a.SetInterpreterEnabled(true); err == nil || !strings.Contains(err.Error(), "47825") {
		t.Fatalf("sem o Whisper o Intérprete não liga: %v", err)
	}
	if !rig.router.is(&rig.router.closed) || rig.it.voice.Running() || rig.hotkey.Load() {
		t.Fatal("o que subiu antes do Whisper deveria ter sido desfeito")
	}
	if rig.a.GetInterpreterInfo().Enabled {
		t.Fatal("o Intérprete deveria continuar desligado")
	}
}

func TestInterpreterTurnsOnTalksAndTurnsOff(t *testing.T) {
	rig := newInterpreterRig(t, standingPython)
	a, it := rig.a, rig.it
	if a.interpreterBusy() {
		t.Fatal("desligado, o Intérprete não segura a transcrição da gravação")
	}
	if err := a.SetInterpreterEnabled(true); err != nil {
		t.Fatal(err)
	}
	lookups := rig.micLookups.Load()
	info := a.GetInterpreterInfo()
	if !info.Enabled || info.Mic != "Microfone de teste" || info.VirtualMic != "BlackHole 2ch" || info.Voice.State != "carregando" {
		t.Fatalf("ligado: %+v", info)
	}
	if rig.micLookups.Load() != lookups {
		t.Fatal("ligado, o microfone virtual é o do roteador: não se listam os dispositivos de novo")
	}
	if !rig.hotkey.Load() || !it.voice.Running() {
		t.Fatal("ligado: o atalho e a voz deveriam estar de pé")
	}

	// A voz ainda carregando: o aperto é recusado (a sua fala sumiria da reunião)
	a.InterpreterTalk(true)
	if rig.router.is(&rig.router.talking) {
		t.Fatal("sem a voz pronta, o atalho não corta a sua voz da reunião")
	}
	a.InterpreterTalk(false)

	// Pronta: quem acompanha a voz percebe, sem o aperto consultar nada
	rig.health.Store("pronto")
	until(t, "a voz ficar pronta", it.voice.Ready)
	a.InterpreterTalk(true)
	if !rig.router.is(&rig.router.talking) {
		t.Fatal("com a voz pronta, o atalho liga o push-to-talk")
	}
	// Com uma frase (o atalho apertado), a transcrição da gravação cede a GPU
	if !a.interpreterBusy() {
		t.Fatal("com o atalho apertado, o Intérprete tem a vez")
	}
	a.InterpreterTalk(false)
	if rig.router.is(&rig.router.talking) {
		t.Fatal("soltar o atalho desliga o push-to-talk")
	}
	until(t, "o Intérprete ficar livre", func() bool { return !a.interpreterBusy() })

	// Sem reunião aberta, o atalho não traduz
	a.SetInterpreterSession("")
	a.InterpreterTalk(true)
	if rig.router.is(&rig.router.talking) {
		t.Fatal("sem reunião aberta o atalho não liga")
	}
	a.SetInterpreterSession("sessao-1")

	rig.health.Store("") // o servidor de voz sai junto: ninguém mais atende
	if err := a.SetInterpreterEnabled(false); err != nil {
		t.Fatal(err)
	}
	if !rig.router.is(&rig.router.closed) || it.voice.Running() || rig.hotkey.Load() {
		t.Fatal("desligado: roteador fechado, voz parada e atalho solto")
	}
	if info := a.GetInterpreterInfo(); info.Enabled || info.Voice.State != "parado" {
		t.Fatalf("desligado: %+v", info)
	}
	a.InterpreterTalk(true) // desligado, o atalho não faz nada
	if a.interpreterBusy() {
		t.Fatal("desligado, o Intérprete não segura a transcrição da gravação")
	}
}

// Desligar com uma frase no meio da tradução não espera os prazos dela.
func TestInterpreterStopsWithAPhraseInProgress(t *testing.T) {
	rig := newInterpreterRig(t, standingPython)
	asked := make(chan struct{}, 8)
	whisper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		asked <- struct{}{}
		<-r.Context().Done() // o Whisper travado
	}))
	defer whisper.Close()
	rig.it.whisperURL = whisper.URL
	rig.health.Store("pronto")
	a := rig.a
	if err := a.SetInterpreterEnabled(true); err != nil {
		t.Fatal(err)
	}
	until(t, "a voz ficar pronta", rig.it.voice.Ready)

	a.InterpreterTalk(true)
	rig.router.onTalk(speech16k(1.2))
	a.InterpreterTalk(false)
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("a frase deveria ter ido para o Whisper")
	}
	if !rig.router.is(&rig.router.busy) {
		t.Fatal("com a frase na tradução, a sua voz está fora da reunião")
	}
	started := time.Now()
	a.stopInterpreter()
	if took := time.Since(started); took > 2*time.Second {
		t.Fatalf("desligar deveria abandonar a frase, levou %v", took)
	}
	if !rig.router.is(&rig.router.closed) || a.GetInterpreterInfo().Enabled {
		t.Fatal("o Intérprete deveria estar desligado")
	}
}

// O servidor de voz morre com o Intérprete ligado: a tela passa a mostrar o
// erro (antes seguia "Pronto") e o próximo aperto o sobe de novo.
func TestInterpreterShowsTheVoiceCrashAndRevivesItOnPress(t *testing.T) {
	rig := newInterpreterRig(t, standingPython)
	a, it := rig.a, rig.it
	rig.health.Store("pronto")
	if err := a.SetInterpreterEnabled(true); err != nil {
		t.Fatal(err)
	}
	until(t, "a voz ficar pronta", it.voice.Ready)
	vidas := filepath.Join(rig.dir, "vidas")
	lives := func() int {
		b, _ := os.ReadFile(vidas)
		return strings.Count(string(b), "sobe")
	}
	until(t, "o servidor de voz subir", func() bool { return lives() == 1 })

	// O macOS mata o servidor (memória): ninguém mais atende na porta
	pid, _ := os.ReadFile(filepath.Join(rig.dir, "pid"))
	n, _ := strconv.Atoi(strings.TrimSpace(string(pid)))
	rig.health.Store("")
	if process, err := os.FindProcess(n); err == nil {
		process.Kill()
	}
	until(t, "a queda aparecer", func() bool { return !it.voice.Running() })
	info := a.GetInterpreterInfo()
	if !info.Enabled || info.Voice.State != "erro" || !strings.Contains(info.Voice.Error, "parou de funcionar") {
		t.Fatalf("a tela deveria mostrar que a voz caiu: %+v", info.Voice)
	}

	// O aperto é recusado (sem voz, a fala sumiria) e põe a voz para subir
	a.InterpreterTalk(true)
	if rig.router.is(&rig.router.talking) {
		t.Fatal("com a voz caída o atalho não liga")
	}
	a.InterpreterTalk(false)
	until(t, "o servidor de voz subir de novo", func() bool { return lives() == 2 && it.voice.Running() })
	rig.health.Store("pronto")
	until(t, "a voz ficar pronta de novo", it.voice.Ready)
	a.InterpreterTalk(true)
	if !rig.router.is(&rig.router.talking) {
		t.Fatal("com a voz de volta, o atalho funciona")
	}
	a.InterpreterTalk(false)
}

// recording40s é uma gravação de calibração de 40 s.
func recording40s() interpreter.Recording {
	rec := interpreter.Recording{At24k: make([]int16, 40*24000), At16k: make([]int16, 40*16000)}
	for i := range rec.At24k {
		rec.At24k[i] = int16(9000 * math.Sin(float64(i)*2*math.Pi*220/24000))
	}
	for i := range rec.At16k {
		rec.At16k[i] = int16(9000 * math.Sin(float64(i)*2*math.Pi*220/16000))
	}
	return rec
}

// calibrateNow gera as opções como o FinishVoiceCalibration, mas esperando o fim.
func calibrateNow(t *testing.T, rig *interpreterRig) CalibrationState {
	t.Helper()
	if err := rig.a.beginCalibration(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		rig.a.calibrate(recording40s())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a calibração não terminou (ficou esperando a voz para sempre?)")
	}
	return rig.a.GetVoiceCalibration()
}

func TestCalibrationGeneratesTheOptionsAndStopsTheVoice(t *testing.T) {
	rig := newInterpreterRig(t, standingPython)
	rig.health.Store("pronto")
	whisper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		var segs []map[string]any
		for i := range 8 {
			segs = append(segs, map[string]any{"start": float64(i) * 5, "end": float64(i)*5 + 4.8, "text": fmt.Sprintf(" Hi everyone, thanks for joining %d.", i+1)})
		}
		json.NewEncoder(w).Encode(map[string]any{"text": "...", "segments": segs})
	}))
	defer whisper.Close()
	rig.a.whisperEndpoint = whisper.URL

	state := calibrateNow(t, rig)
	if state.Stage != "pronto" || len(state.Options) < 2 || state.Options[0].ID != "atual" {
		t.Fatalf("calibração: %+v", state)
	}
	if rig.it.voice.Running() {
		t.Fatal("com o Intérprete desligado, a voz que subiu para a calibração é desligada no fim")
	}
	// Escolher uma opção e voltar para a voz de antes
	if err := rig.a.ChooseVoiceOption(state.Options[1].ID); err != nil || rig.it.voice.SampleText() != state.Options[1].Text {
		t.Fatalf("escolher a opção 1: %v, %q", err, rig.it.voice.SampleText())
	}
	if err := rig.a.ChooseVoiceOption("atual"); err != nil || rig.it.voice.SampleText() != "Hi everyone." {
		t.Fatalf("voltar para a voz atual: %v, %q", err, rig.it.voice.SampleText())
	}
	if again := calibrateNow(t, rig); again.Stage != "pronto" {
		t.Fatalf("terminada uma calibração, outra pode começar: %+v", again)
	}
}

// A voz não fica pronta: a calibração acaba com erro, em vez de ficar em
// "Carregando a voz..." para sempre.
func TestCalibrationGivesUpWhenTheVoiceDoesNotGetReady(t *testing.T) {
	t.Run("o servidor de voz morre ao subir", func(t *testing.T) {
		rig := newInterpreterRig(t, "exit 3")
		rig.health.Store("")
		state := calibrateNow(t, rig)
		if state.Stage != "erro" || !strings.Contains(state.Message, "parou de funcionar") {
			t.Fatalf("calibração: %+v", state)
		}
	})
	t.Run("a voz é desligada no meio", func(t *testing.T) {
		rig := newInterpreterRig(t, standingPython)
		rig.health.Store("") // ainda subindo o Python: ninguém atende
		go func() {
			for !rig.it.voice.Running() {
				time.Sleep(5 * time.Millisecond)
			}
			rig.it.voice.Stop()
		}()
		state := calibrateNow(t, rig)
		if state.Stage != "erro" || !strings.Contains(state.Message, "desligada") {
			t.Fatalf("calibração: %+v", state)
		}
	})
	t.Run("a voz nunca termina de carregar", func(t *testing.T) {
		rig := newInterpreterRig(t, standingPython) // o /health responde "carregando", sem mudar
		rig.it.voiceStall = 100 * time.Millisecond
		state := calibrateNow(t, rig)
		if state.Stage != "erro" || !strings.Contains(state.Message, "sem novidade") {
			t.Fatalf("calibração: %+v", state)
		}
		if rig.it.voice.Running() {
			t.Fatal("desistindo, a voz que subiu para a calibração é desligada")
		}
	})
}

func TestOnlyOneCalibrationAtATime(t *testing.T) {
	rig := newInterpreterRig(t, standingPython)
	a, it := rig.a, rig.it
	if err := a.beginCalibration(); err != nil {
		t.Fatal(err)
	}
	if err := a.beginCalibration(); err == nil || !strings.Contains(err.Error(), "anterior") {
		t.Fatalf("com uma calibração gerando as opções, outra não começa: %v", err)
	}
	// Nem a gravação da próxima (o microfone nem é aberto)
	if err := a.StartVoiceCalibration(); err == nil || !strings.Contains(err.Error(), "anterior") {
		t.Fatalf("gravar durante a calibração anterior: %v", err)
	}

	// Desligar o Intérprete no meio não tira a voz da calibração
	rig.health.Store("pronto")
	if err := a.SetInterpreterEnabled(true); err != nil {
		t.Fatal(err)
	}
	a.stopInterpreter()
	if !it.voice.Running() {
		t.Fatal("a calibração em andamento ainda precisa da voz")
	}
	it.mu.Lock()
	it.calibrating = false
	it.stopVoiceIfUnused()
	it.mu.Unlock()
	if it.voice.Running() {
		t.Fatal("sem calibração e com o Intérprete desligado, a voz é desligada")
	}
}

// Sem o Wait depois do Kill, o whisper-server morto ficava na tabela de
// processos (zumbi) até o app fechar, um a cada liga/desliga.
func TestKillProcessDoesNotLeaveAZombie(t *testing.T) {
	killProcess(nil) // o Whisper que já estava de pé (StartServer devolve nil): nada a matar
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip("sem o sleep para servir de processo")
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	killProcess(cmd)
	until(t, "o processo morto sair da tabela de processos", func() bool {
		return exec.Command("ps", "-p", pid).Run() != nil
	})
}
