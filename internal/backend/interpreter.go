package backend

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"recorder/internal/chat"
	"recorder/internal/datadir"
	"recorder/internal/interpreter"
	"recorder/internal/mac"
	"recorder/internal/transcriber"
)

// O Intérprete (internal/interpreter): segurando ⌃⌥Espaço, a sua voz sai da
// reunião e o que você diz em português entra nela em inglês, com a sua voz.
// O Meet usa o BlackHole como microfone, e o Recorder decide o que vai para ele.

// audioRouter é o roteador ligado (interpreter.Router; nos testes, um falso).
type audioRouter interface {
	interpreter.Output
	Close()
	Names() (mic, meeting string)
}

// interp guarda as peças do Intérprete ligado.
type interp struct {
	mu          sync.Mutex
	voice       *interpreter.Voice
	running     *interpreter.Interpreter
	router      audioRouter
	whisper     *exec.Cmd
	correctors  []interpreter.Corrector // corrigem a tradução do Whisper (o claude fica aberto)
	installing  string                  // o que a instalação da voz está fazendo ("" se parada)
	installErr  string
	recorder    *interpreter.Recorder // gravação da calibração em andamento
	session     string                // reunião aberta na tela: sem ela, o Intérprete não traduz
	calib       CalibrationState
	calibrating bool // as opções de voz estão sendo geradas: uma calibração por vez

	// O aperto do atalho lê estes sem esperar o lock (preso enquanto o
	// Intérprete liga) nem uma resposta HTTP: apertar e soltar chegam ao
	// Intérprete na ordem em que aconteceram
	talk       atomic.Pointer[interpreter.Interpreter]
	hasSession atomic.Bool
	voiceWatch atomic.Int64 // muda a cada watchVoiceUntilReady: só o último continua
	lastKick   atomic.Int64 // quando um aperto recusado pôs a voz para ser conferida (kickVoice)

	// Quanto a calibração espera a voz: de quanto em quanto tempo consulta e
	// quanto tolera sem novidade (o download e a carga do modelo andam)
	voicePoll, voiceStall time.Duration

	// As peças do sistema, trocadas por falsas nos testes
	startRouter  func(onTalk func([]int16)) (audioRouter, error)
	startWhisper func() (*exec.Cmd, error)
	whisperURL   string        // onde o Whisper do Intérprete atende
	virtualMic   func() string // "" se o BlackHole não estiver instalado
	pushToTalk   func(on bool, onChange func(pressed bool))
}

func (a *App) interp() *interp {
	a.interpOnce.Do(func() {
		it := &interp{
			voice:     interpreter.NewVoice(interpreter.DefaultVoiceDir()),
			voicePoll: time.Second, voiceStall: 10 * time.Minute,
			startRouter: func(onTalk func([]int16)) (audioRouter, error) {
				router, err := interpreter.StartRouter(onTalk, true)
				if err != nil {
					return nil, err
				}
				return router, nil
			},
			// Whisper só do Intérprete: o do app pode estar ocupado com a gravação
			startWhisper: func() (*exec.Cmd, error) {
				return transcriber.StartServer("ggml-medium-q5_0.bin", interpreter.WhisperPort)
			},
			whisperURL: "http://127.0.0.1:" + interpreter.WhisperPort,
			virtualMic: func() string {
				name, _ := interpreter.FindVirtualMic()
				return name
			},
			pushToTalk: mac.EnablePushToTalk,
		}
		it.voice.OnExit = a.voiceExited
		a.interpState = it
	})
	return a.interpState
}

// InterpreterInfo é o que a barra lateral e as configurações mostram.
type InterpreterInfo struct {
	Supported        bool                    `json:"supported"`          // Mac com Apple Silicon
	VirtualMic       string                  `json:"virtual_mic"`        // "" se o BlackHole não estiver instalado; ligado, o que está em uso
	VirtualMicDriver bool                    `json:"virtual_mic_driver"` // instalado, mas talvez ainda não carregado
	UVFound          bool                    `json:"uv_found"`           // para instalar a voz
	VoiceInstalled   bool                    `json:"voice_installed"`    // o ambiente Python da voz
	ModelDownloaded  bool                    `json:"model_downloaded"`   // os ~4 GB do modelo
	Installing       string                  `json:"installing"`         // o que a instalação está fazendo
	InstallError     string                  `json:"install_error"`
	SampleRecorded   bool                    `json:"sample_recorded"`
	SampleText       string                  `json:"sample_text"` // texto da amostra gravada
	ReadingText      string                  `json:"reading_text"`
	Enabled          bool                    `json:"enabled"`
	Mic              string                  `json:"mic"` // o seu microfone, com o Intérprete ligado
	Voice            interpreter.VoiceHealth `json:"voice"`
	Status           interpreter.Status      `json:"status"`
	Shortcut         string                  `json:"shortcut"`
	Correction       string                  `json:"correction"`   // interpreter.Correction*
	ClaudeFound      bool                    `json:"claude_found"` // para corrigir com o Haiku
}

func (a *App) GetInterpreterInfo() InterpreterInfo {
	it := a.interp()
	info := a.interpreterSetup()
	info.Voice = it.voice.Health() // HTTP: fora do lock
	a.interpreterState(&info)
	if info.Enabled && info.Voice.State == "erro" && !it.voice.Running() {
		// A voz caiu com o Intérprete ligado: o próximo aperto a sobe de novo (voiceNotReady)
		info.Voice.Error += ". Aperte " + mac.PushToTalkShortcut + " para subir a voz de novo, ou desligue e ligue o Intérprete"
	}
	return info
}

// interpreterSetup é a parte do InterpreterInfo que não muda de um instante
// para o outro e custa para montar: os dispositivos de áudio, os executáveis
// e os arquivos da voz.
func (a *App) interpreterSetup() InterpreterInfo {
	it := a.interp()
	info := InterpreterInfo{
		Supported:        interpreter.Supported(),
		VirtualMicDriver: interpreter.VirtualMicDriverInstalled(),
		UVFound:          interpreter.FindUV() != "",
		VoiceInstalled:   it.voice.Installed(),
		ModelDownloaded:  it.voice.ModelDownloaded(),
		SampleRecorded:   it.voice.HasSample(),
		SampleText:       it.voice.SampleText(),
		ReadingText:      interpreter.CalibrationText,
		Shortcut:         mac.PushToTalkShortcut,
		Correction:       a.interpreterCorrection(),
		ClaudeFound:      chat.FindClaude() != "",
	}
	// Ligado, o microfone virtual é o do roteador (interpreterState): não é
	// preciso listar os dispositivos de novo
	if it.talk.Load() == nil {
		info.VirtualMic = it.virtualMic()
	}
	return info
}

// interpreterState preenche a parte do InterpreterInfo que muda com o
// Intérprete ligado e com a instalação da voz.
func (a *App) interpreterState(info *InterpreterInfo) {
	it := a.interp()
	it.mu.Lock()
	defer it.mu.Unlock()
	info.Installing, info.InstallError = it.installing, it.installErr
	info.Enabled = it.running != nil
	info.Status, info.Mic = interpreter.Status{}, ""
	if it.running != nil {
		info.Status = it.running.Status()
		info.Mic, info.VirtualMic = it.router.Names()
	}
}

func (a *App) emitInterpreterInfo() {
	if a.wailsCtx() == nil {
		return // sem janela (antes do startup, nos testes), nem monta o estado
	}
	a.emit("interpreter-info", a.GetInterpreterInfo())
}

// InstallInterpreterVoice instala o ambiente Python da voz em segundo plano;
// o andamento chega pelo evento "interpreter-info".
func (a *App) InstallInterpreterVoice() error {
	it := a.interp()
	it.mu.Lock()
	if it.installing != "" {
		it.mu.Unlock()
		return nil
	}
	it.installing, it.installErr = "Preparando a instalação...", ""
	it.mu.Unlock()
	a.emitInterpreterInfo()
	go func() {
		err := it.voice.Install(func(msg string) {
			it.mu.Lock()
			it.installing = msg
			it.mu.Unlock()
			a.emitInterpreterInfo()
		})
		it.mu.Lock()
		it.installing = ""
		if err != nil {
			it.installErr = err.Error()
			log.Printf("Intérprete: erro ao instalar a voz: %v", err)
		}
		it.mu.Unlock()
		a.emitInterpreterInfo()
	}()
	return nil
}

// SetInterpreterEnabled liga ou desliga o Intérprete. Ligado, o seu microfone
// é repassado para o BlackHole (o microfone do Meet) e ⌃⌥Espaço traduz.
func (a *App) SetInterpreterEnabled(on bool) error {
	if !on {
		a.stopInterpreter()
		a.emitInterpreterInfo()
		return nil
	}
	it := a.interp()
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.running != nil {
		return nil
	}
	switch {
	case it.session == "":
		return fmt.Errorf("abra ou crie uma reunião para usar o Intérprete")
	case !interpreter.Supported():
		return fmt.Errorf("o Intérprete só funciona em Mac com Apple Silicon")
	case !it.voice.Installed():
		return fmt.Errorf("instale a voz em Configurações > Intérprete")
	case !it.voice.HasSample():
		return fmt.Errorf("calibre a sua voz em Configurações > Intérprete")
	}

	// Primeiro os dispositivos: sem o BlackHole (ou com ele como entrada
	// padrão) não adianta subir o Whisper e a voz. O seu microfone já começa
	// a ser repassado para a reunião
	var current atomic.Pointer[interpreter.Interpreter]
	router, err := it.startRouter(func(samples []int16) {
		if in := current.Load(); in != nil {
			in.Feed(samples)
		}
	})
	if err != nil {
		return err
	}
	cmd, err := it.startWhisper()
	if err != nil {
		router.Close()
		return fmt.Errorf("não foi possível subir o Whisper do Intérprete: %w", err)
	}
	if err := it.voice.Start(); err != nil {
		router.Close()
		killProcess(cmd)
		return err
	}

	correctors := a.interpreterCorrectors(a.interpreterCorrection())
	warmUp(correctors)
	running := interpreter.New(interpreter.Config{
		WhisperEndpoint: it.whisperURL,
		Correctors:      correctors,
		Voice:           it.voice,
		OnStatus:        func(s interpreter.Status) { a.emit("interpreter-status", s) },
		KeepDir:         datadir.Path("interprete"),
	}, router)
	current.Store(running)
	it.running, it.router, it.whisper, it.correctors = running, router, cmd, correctors
	it.talk.Store(running)
	it.pushToTalk(true, a.InterpreterTalk)
	mic, meeting := router.Names()
	log.Printf("Intérprete ligado: %s -> %s", mic, meeting)

	// A voz leva uns segundos para carregar (ou baixar o modelo, na 1ª vez):
	// a tela acompanha pelo evento
	go a.watchVoiceUntilReady()
	return nil
}

func voiceLoading(h interpreter.VoiceHealth) bool {
	return h.State == "carregando" || h.State == "baixando"
}

// watchVoiceUntilReady acompanha a voz enquanto ela baixa o modelo e carrega,
// avisando a tela a cada mudança. Depois de pronta não é preciso consultar:
// se o servidor cair, a Voice avisa (voiceExited).
func (a *App) watchVoiceUntilReady() {
	it := a.interp()
	watch := it.voiceWatch.Add(1)
	// A parte cara (dispositivos de áudio, executáveis, arquivos) é montada
	// uma vez só: no laço muda só o estado da voz
	info := a.GetInterpreterInfo()
	a.emit("interpreter-info", info)
	for voiceLoading(info.Voice) && it.voice.Running() {
		time.Sleep(500 * time.Millisecond)
		if it.voiceWatch.Load() != watch {
			return // a voz subiu de novo: outro acompanha
		}
		h := it.voice.Health()
		if h == info.Voice {
			continue
		}
		info.Voice = h
		a.interpreterState(&info)
		a.emit("interpreter-info", info)
	}
	// O estado final, com o que mudou no disco (o modelo baixado)
	a.emitInterpreterInfo()
}

// voiceExited é chamado quando o servidor de voz termina sozinho (o macOS o
// matou por memória, o MLX caiu). A tela passa a mostrar o erro
// (voice.state "erro"), em vez de continuar dizendo "Pronto".
func (a *App) voiceExited(err error) {
	log.Printf("Intérprete: o servidor de voz parou sozinho (%v)", err)
	a.emitInterpreterInfo()
}

// reviveVoice sobe de novo o servidor de voz que caiu, se o Intérprete ainda
// está ligado. Só a pedido (um aperto do atalho): um servidor que cai ao
// carregar não fica subindo e caindo sozinho.
func (a *App) reviveVoice() {
	it := a.interp()
	it.mu.Lock()
	on := it.running != nil
	var err error
	if on && !it.voice.Running() {
		err = it.voice.Start()
	}
	it.mu.Unlock()
	if !on {
		return
	}
	if err != nil {
		log.Printf("Intérprete: não foi possível subir a voz de novo: %v", err)
	}
	a.watchVoiceUntilReady()
}

// killProcess mata o processo e recolhe o resultado: sem o Wait, cada
// whisper-server morto ficava como zumbi até o app fechar.
func killProcess(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
		go cmd.Wait()
	}
}

// stopInterpreter desliga o Intérprete (também ao fechar o app).
func (a *App) stopInterpreter() {
	it := a.interp()
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.running == nil {
		return
	}
	it.pushToTalk(false, nil)
	it.talk.Store(nil)
	// Espera a frase em andamento ser abandonada: depois disso ninguém mais
	// usa o roteador nem os corretores, que podem ser fechados
	it.running.Close()
	it.router.Close()
	killProcess(it.whisper)
	closeCorrectors(it.correctors)
	it.running, it.router, it.whisper, it.correctors = nil, nil, nil, nil
	it.stopVoiceIfUnused()
	log.Println("Intérprete desligado")
}

// stopVoiceIfUnused desliga o servidor de voz quando ninguém mais precisa
// dele: o Intérprete está desligado e não há calibração gerando opções.
// Chamado com it.mu.
func (it *interp) stopVoiceIfUnused() {
	if it.running == nil && !it.calibrating {
		it.voice.Stop()
	}
}

// A tradução que vai para a reunião é a do Whisper (fiel ao que você disse),
// corrigida por uma IA só onde está errada (internal/interpreter/corrector.go).

// interpreterCorrection é a IA que corrige a tradução: a escolhida nas
// configurações; sem escolha, o Claude Haiku se o claude estiver instalado
// (o Ollama ocupa vários GB de memória).
func (a *App) interpreterCorrection() string {
	switch c, _ := a.store.GetSetting("interpreter_correction"); c {
	case interpreter.CorrectionClaude, interpreter.CorrectionOllama, interpreter.CorrectionNone:
		return c
	}
	if chat.FindClaude() != "" {
		return interpreter.CorrectionClaude
	}
	return interpreter.CorrectionNone
}

// interpreterCorrectors monta os corretores, em ordem. Com o Ollama, o
// Haiku fica de reserva (o Ollama pode estar desligado para poupar memória);
// com o Haiku, o Ollama não entra, para não ocupar a memória sem ser pedido.
// Se nenhum responder, vai a tradução literal do Whisper.
func (a *App) interpreterCorrectors(choice string) []interpreter.Corrector {
	var haiku []interpreter.Corrector
	if path := chat.FindClaude(); path != "" {
		haiku = append(haiku, interpreter.NewClaudeCorrector(path))
	}
	switch choice {
	case interpreter.CorrectionOllama:
		ollama := &interpreter.OllamaCorrector{Endpoint: a.ollama.Endpoint, Model: a.ollama.CurrentModel}
		return append([]interpreter.Corrector{ollama}, haiku...)
	case interpreter.CorrectionClaude:
		return haiku
	}
	return nil
}

// interpreterBusy diz se o Intérprete está com uma frase (ouvindo ou
// traduzindo): a transcrição da gravação cede a GPU a ele (pipeline.waitTurn).
func (a *App) interpreterBusy() bool {
	in := a.interp().talk.Load()
	if in == nil {
		return false // desligado
	}
	state := in.Status().State
	return state == "ouvindo" || state == "traduzindo"
}

// warmUp prepara o primeiro corretor que responder, para a primeira frase não
// esperar o modelo carregar ou o claude abrir.
func warmUp(cs []interpreter.Corrector) {
	go func() {
		for _, c := range cs {
			if err := c.Warmup(); err != nil {
				log.Printf("Intérprete: %s indisponível: %v", c.Name(), err)
				continue
			}
			return
		}
	}()
}

func closeCorrectors(cs []interpreter.Corrector) {
	for _, c := range cs {
		c.Close()
	}
}

// SetInterpreterCorrection escolhe a IA que corrige a tradução: "claude"
// (Haiku), "ollama" ou "none" (só o Whisper). Com o Intérprete ligado, vale
// a partir da próxima frase.
func (a *App) SetInterpreterCorrection(choice string) error {
	switch choice {
	case interpreter.CorrectionClaude:
		if chat.FindClaude() == "" {
			return fmt.Errorf("o claude (Claude Code) não foi encontrado neste computador")
		}
	case interpreter.CorrectionOllama, interpreter.CorrectionNone:
	default:
		return fmt.Errorf("correção desconhecida: %s", choice)
	}
	if err := a.store.SetSetting("interpreter_correction", choice); err != nil {
		return err
	}
	it := a.interp()
	it.mu.Lock()
	if it.running != nil {
		old := it.correctors
		it.correctors = a.interpreterCorrectors(choice)
		it.running.SetCorrectors(it.correctors)
		warmUp(it.correctors)
		// Fechados, os antigos não abrem mais nada (nem o warmUp deles, se
		// ainda estiver rodando). Uma frase sendo corrigida neste instante
		// perde a correção e vai literal, com aviso
		closeCorrectors(old)
	}
	it.mu.Unlock()
	a.emitInterpreterInfo()
	return nil
}

// SetInterpreterSession diz qual reunião está aberta na tela ("" se nenhuma).
// O Intérprete só liga e só traduz com uma reunião aberta; sem ela, o seu
// microfone continua indo para o Meet normalmente.
func (a *App) SetInterpreterSession(sessionID string) {
	it := a.interp()
	it.mu.Lock()
	it.session = sessionID
	it.hasSession.Store(sessionID != "")
	it.mu.Unlock()
}

// InterpreterTalk liga (on) ou desliga o push-to-talk: o botão da tela e o
// atalho ⌃⌥Espaço. Não espera lock nem resposta HTTP: se apertar demorasse
// mais que soltar (como quando consultava a voz), um toque curto no botão
// chegava fora de ordem e o Intérprete ficava preso em "ouvindo".
func (a *App) InterpreterTalk(on bool) {
	it := a.interp()
	running := it.talk.Load()
	if running == nil {
		return
	}
	if !on {
		running.Talk(false)
		return
	}
	if !it.hasSession.Load() {
		a.emit("interpreter-status", interpreter.Status{State: "pronto", Message: "Abra uma reunião para usar o Intérprete."})
		return
	}
	if !it.voice.Ready() {
		// Sem a voz pronta, a sua fala sumiria da reunião sem virar inglês
		a.emit("interpreter-status", interpreter.Status{State: "pronto", Message: a.voiceNotReady()})
		return
	}
	running.Talk(true)
}

// voiceNotReady explica por que o aperto do atalho foi recusado e põe a voz
// para subir de novo, se ela caiu.
func (a *App) voiceNotReady() string {
	it := a.interp()
	h := it.voice.LastHealth()
	switch {
	case !it.voice.Running():
		// Caiu (ou foi desligada por fora) com o Intérprete ligado
		a.kickVoice(a.reviveVoice)
		return "A voz parou de funcionar; subindo de novo, espere ficar pronta."
	case h.State == "erro":
		return "Erro na voz: " + h.Error + ". Desligue e ligue o Intérprete."
	}
	// O estado guardado pode estar velho: confere de novo, sem segurar o aperto
	a.kickVoice(a.watchVoiceUntilReady)
	if h.State == "baixando" {
		return fmt.Sprintf("Baixando o modelo de voz (%d%%); espere ficar pronta.", h.Progress)
	}
	return "A voz ainda está carregando; espere ficar pronta."
}

// kickVoice roda f em segundo plano, no máximo uma vez por segundo (o atalho
// repete enquanto está apertado).
func (a *App) kickVoice(f func()) {
	it := a.interp()
	now, last := time.Now().UnixNano(), it.lastKick.Load()
	if now-last < int64(time.Second) || !it.lastKick.CompareAndSwap(last, now) {
		return
	}
	go f()
}

// Calibração da voz (internal/interpreter/calibration.go): você grava uns
// minutos lendo em inglês; o app separa os melhores trechos, gera a mesma
// frase com cada um e você escolhe a voz que mais parece com a sua.

// CalibrationState é o andamento da calibração, para a tela.
type CalibrationState struct {
	Stage   string                    `json:"stage"` // "", "gravando", "processando", "pronto" ou "erro"
	Message string                    `json:"message"`
	Options []interpreter.VoiceOption `json:"options"`
	BaseURL string                    `json:"base_url"` // onde tocar os arquivos das opções
}

func (a *App) calibrationState(it *interp) CalibrationState {
	c := it.calib
	c.BaseURL = a.GetAudioServerURL() + "/voz/"
	if c.Stage == "" {
		c.Options = it.voice.CalibrationOptions() // a última calibração, para escolher de novo
	}
	return c
}

// GetVoiceCalibration devolve o andamento da calibração.
func (a *App) GetVoiceCalibration() CalibrationState {
	it := a.interp()
	it.mu.Lock()
	defer it.mu.Unlock()
	return a.calibrationState(it)
}

func (a *App) setCalibration(stage, message string, options []interpreter.VoiceOption) {
	it := a.interp()
	it.mu.Lock()
	it.calib = CalibrationState{Stage: stage, Message: message, Options: options}
	state := a.calibrationState(it)
	it.mu.Unlock()
	a.emit("voice-calibration", state)
}

// StartVoiceCalibration começa a gravar você lendo o texto da calibração.
func (a *App) StartVoiceCalibration() error {
	it := a.interp()
	it.mu.Lock()
	if it.calibrating {
		it.mu.Unlock()
		return fmt.Errorf("a calibração anterior ainda está gerando as opções; espere ela terminar")
	}
	if it.recorder != nil {
		it.recorder.Cancel()
	}
	rec, err := interpreter.StartRecording()
	if err == nil {
		it.recorder = rec
	}
	it.mu.Unlock()
	if err != nil {
		return err
	}
	a.setCalibration("gravando", "", nil)
	return nil
}

// CancelVoiceCalibration descarta a gravação em andamento.
func (a *App) CancelVoiceCalibration() {
	it := a.interp()
	it.mu.Lock()
	rec := it.recorder
	it.recorder = nil
	it.mu.Unlock()
	if rec != nil {
		rec.Cancel()
		a.setCalibration("", "", nil)
	}
}

// FinishVoiceCalibration termina a gravação e gera as opções de voz em
// segundo plano; o andamento e as opções chegam pelo evento "voice-calibration".
func (a *App) FinishVoiceCalibration() error {
	it := a.interp()
	it.mu.Lock()
	rec := it.recorder
	it.recorder = nil
	it.mu.Unlock()
	if rec == nil {
		return fmt.Errorf("a gravação não foi iniciada")
	}
	recording, err := rec.Stop()
	if err != nil {
		a.setCalibration("", "", nil)
		return err
	}
	if !it.voice.Installed() {
		a.setCalibration("", "", nil)
		return fmt.Errorf("instale a voz antes de calibrar")
	}
	if err := a.beginCalibration(); err != nil {
		return err
	}
	a.setCalibration("processando", fmt.Sprintf("Gravação de %.0f s. Preparando a voz...", recording.Seconds()), nil)
	go a.calibrate(recording)
	return nil
}

// beginCalibration marca que as opções de voz estão sendo geradas. Uma
// calibração por vez: duas ao mesmo tempo escreviam na mesma pasta, e as
// opções de uma ficavam com os arquivos da outra.
func (a *App) beginCalibration() error {
	it := a.interp()
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.calibrating {
		return fmt.Errorf("a calibração anterior ainda está gerando as opções; espere ela terminar")
	}
	it.calibrating = true
	return nil
}

// calibrate gera as opções de voz da gravação (depois de beginCalibration).
func (a *App) calibrate(recording interpreter.Recording) {
	it := a.interp()
	options, err := a.voiceOptions(recording)
	// Com o Intérprete desligado, a voz subiu só para a calibração
	it.mu.Lock()
	it.calibrating = false
	it.stopVoiceIfUnused()
	it.mu.Unlock()
	if err != nil {
		log.Printf("Intérprete: a calibração falhou: %v", err)
		a.setCalibration("erro", err.Error(), nil)
		return
	}
	a.setCalibration("pronto", "", options)
}

func (a *App) voiceOptions(recording interpreter.Recording) ([]interpreter.VoiceOption, error) {
	it := a.interp()
	if err := it.voice.Start(); err != nil {
		return nil, err
	}
	if err := a.waitVoiceForCalibration(); err != nil {
		return nil, err
	}
	return interpreter.Calibrate(context.Background(), recording, a.whisper().Endpoint, it.voice, func(msg string) {
		a.setCalibration("processando", msg, nil)
	})
}

// waitVoiceForCalibration espera a voz ficar pronta, mostrando o andamento.
// Acaba com erro se a voz der erro, cair ou for desligada (antes, ficava em
// "Carregando a voz..." para sempre) ou se ficar voiceStall sem novidade.
func (a *App) waitVoiceForCalibration() error {
	it := a.interp()
	ctx, giveUp := context.WithCancel(context.Background())
	defer giveUp()
	stall := time.AfterFunc(it.voiceStall, giveUp)
	defer stall.Stop()
	var last interpreter.VoiceHealth
	err := it.voice.WaitReady(ctx, it.voicePoll, func(h interpreter.VoiceHealth) {
		if h != last {
			last = h
			stall.Reset(it.voiceStall) // o download ou a carga andou
		}
		if h.State == "baixando" {
			a.setCalibration("processando", fmt.Sprintf("Baixando o modelo de voz (%d%%), só na primeira vez...", h.Progress), nil)
		} else {
			a.setCalibration("processando", "Carregando a voz...", nil)
		}
	})
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("a voz não ficou pronta: %v sem novidade; tente de novo", it.voiceStall)
	}
	return err
}

// ChooseVoiceOption passa a usar a voz escolhida na calibração.
func (a *App) ChooseVoiceOption(id string) error {
	it := a.interp()
	if err := it.voice.ChooseOption(id); err != nil {
		return err
	}
	// Com a voz carregada, ela se prepara para a amostra nova
	go func() {
		if it.voice.Health().State == "pronto" {
			it.voice.Warmup()
		}
	}()
	a.emitInterpreterInfo()
	return nil
}
