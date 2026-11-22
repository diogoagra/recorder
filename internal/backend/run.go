package backend

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	stdruntime "runtime"
	"strings"
	"sync/atomic"
	"time"

	"recorder/internal/assistant"
	"recorder/internal/audio"
	"recorder/internal/chat"
	"recorder/internal/datadir"
	"recorder/internal/diarization"
	"recorder/internal/interpreter"
	"recorder/internal/speakers"
	"recorder/internal/storage"
	"recorder/internal/transcriber"
	"recorder/internal/transcript"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

// Créditos mostrados no "Sobre" nativo do macOS; a tela "Sobre" do
// frontend (index.html) repete o mesmo texto para o Windows.
const aboutMessage = "Versão " + Version + "\n\nAssistente local de transcrição e resumo de reuniões.\n\nDesenvolvido por Diogo Agra\ndiogoagra.dev · diogoagra@gmail.com\n\nSoftware livre, sob a licença MIT."

// appMenu cria o menu do app no macOS: o do app, onde fica o item "Sobre", e
// o "Editar", sem o qual o webview não recebe os atalhos de copiar, colar e
// selecionar tudo (Cmd+C, Cmd+V, Cmd+A). No Windows não há menu: esses
// atalhos funcionam direto e o "Sobre" fica só na tela do app.
func appMenu() *menu.Menu {
	if stdruntime.GOOS != "darwin" {
		return nil
	}
	return menu.NewMenuFromItems(menu.AppMenu(), menu.EditMenu())
}

// RunWindow abre o app: a janela, com a interface de assets (a pasta frontend
// embutida no executável) e o ícone do "Sobre", mais a gravação e a
// transcrição em segundo plano. Só retorna quando o app fecha.
func RunWindow(assets fs.FS, appIcon []byte) {
	if generatingBindings {
		// O Wails está executando o app só para ler os métodos do App: nada de
		// banco (que seria migrado), whisper-server, microfone ou log. Antes
		// tudo isso subia aqui; com o whisper-server em segundo plano, ele
		// ainda ficava vivo depois de o app sair, segurando a saída do
		// processo, e o `wails build` travava em "Generating bindings".
		app := &App{}
		if err := wails.Run(&options.App{OnStartup: app.startup, Bind: []interface{}{app}}); err != nil {
			log.Fatalf("Erro ao gerar os bindings: %v", err)
		}
		return
	}

	appDataDir := datadir.Dir()
	if err := datadir.Ensure(); err != nil {
		log.Printf("Aviso: não foi possível preparar a pasta de dados %s: %v", appDataDir, err)
	}
	logToFile(appDataDir)
	log.Println("Iniciando Wails App...")

	// O modelo do Whisper não vai no pacote: na primeira execução ele é
	// baixado para a pasta de dados, em segundo plano (model.go). O app ainda
	// não existe aqui; ele passa a receber o andamento logo abaixo.
	var onModel atomic.Pointer[func(transcriber.ModelStatus)]
	model := transcriber.FetchModel(transcriber.DefaultModel, datadir.Models(), func(status transcriber.ModelStatus) {
		if notify := onModel.Load(); notify != nil {
			(*notify)(status)
		}
	})
	// O whisper-server sobe em segundo plano, quando o modelo estiver no disco
	// (a janela não espera por ele; a tela acompanha por CheckServices), e
	// sobe de novo se cair
	whisperServer := transcriber.Supervise(model, transcriber.Port)
	// O whisper-server do Intérprete só sobe quando ele é ligado: um órfão de
	// uma execução anterior ficaria ocupando memória até lá
	go transcriber.KillOrphans(interpreter.WhisperPort)
	fatal := func(format string, args ...any) {
		model.Stop()
		whisperServer.Stop()
		log.Fatalf(format, args...)
	}

	store, err := storage.NewStorage(datadir.DB())
	if err != nil {
		fatal("Erro ao abrir banco: %v", err)
	}

	savedModel, _ := store.GetSetting("ollama_model")
	if savedModel == "" {
		savedModel = "llama3.1" // fallback fallback
	}
	ollama := chat.NewOllamaClient(chat.DefaultOllamaEndpoint, savedModel)
	app := NewApp(store, ollama)
	app.model = model
	notifyModel := app.broadcastWhisperModel
	onModel.Store(&notifyModel)
	app.diarizer = diarization.Load(datadir.Models())

	levelChan := make(chan float64, 5)
	go func() {
		for level := range levelChan {
			app.emit("audio-level", level)
		}
	}()
	onAudioLevel := func(level float64) {
		select {
		case levelChan <- level:
		default:
		}
	}
	speakers.OnMerge = app.BroadcastSpeakersChanged

	capt, err := audio.NewCapture(onAudioLevel)
	if err != nil {
		fatal("Erro ao iniciar áudio: %v", err)
	}
	capt.SetSystemAudio(systemAudioEnabled(store))
	app.capture = capt

	// A gravação e a transcrição, cada uma na sua goroutine
	p := newAppPipeline(store, app.recorder(), capt, app.diarizer)
	p.onTranscript = app.BroadcastTranscript
	p.onSessionEnd = app.GenerateSummaryAsync
	p.onQueue = app.setTranscriptionQueue
	p.yield = app.interpreterBusy
	go p.run()

	err = wails.Run(&options.App{
		Title:  "Recorder",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets:     assets,
			Middleware: withPageCSP, // a interface sai com a política de conteúdo (csp.go)
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		Menu:             appMenu(),
		Mac: &mac.Options{
			About: &mac.AboutInfo{
				Title:   "Recorder",
				Message: aboutMessage,
				Icon:    appIcon,
			},
		},
		OnStartup: app.startup,
		OnShutdown: func(ctx context.Context) {
			// O Meet usa o BlackHole como microfone: o Intérprete sai antes de tudo
			app.stopInterpreter()
			// Fechando o app no meio de uma gravação: interrompe o bloco atual
			// para o que já foi gravado ir para o disco. Juntar ao áudio da
			// sessão, transcrever o que faltou e fazer o resumo fica para a
			// próxima vez que o app abrir (audio.Recover), sem segurar o fechamento.
			if app.recorder().Close() {
				app.recorder().WaitParked(5 * time.Second)
			}
		},
		Bind: []interface{}{
			app,
		},
	})

	// Encerrar o whisper-server quando o Wails fechar. Um download do modelo
	// pela metade continua de onde parou na próxima vez.
	model.Stop()
	whisperServer.Stop()
	if err != nil {
		log.Fatalf("Erro fatal no wails.Run: %v", err)
	}
}

// newAppPipeline monta a gravação e a transcrição com as pastas do app, o
// Whisper e a identificação de locutores de verdade.
func newAppPipeline(store *storage.Storage, rec *recording, capt blockRecorder, diarizer *diarization.Diarizer) *pipeline {
	tmpAudioDir := datadir.TmpAudio()
	os.MkdirAll(tmpAudioDir, 0755)
	os.MkdirAll(datadir.SessionsAudio(), 0755)

	whisper := transcriber.NewWhisperClient(transcriber.Endpoint)
	p := newPipeline(store, rec, capt, tmpAudioDir, datadir.SessionsAudio())
	p.whisperReady = whisper.Ready
	p.transcribe = func(sessionID, wavPath string, samples, previous []int16) ([]transcript.Utterance, error) {
		return transcript.Transcribe(whisper, diarizer, store, sessionID, wavPath, samples, previous)
	}
	return p
}

// run recupera as gravações interrompidas, liga a fila de transcrição e
// grava enquanto a tela pedir. Só retorna quando o app fecha.
func (p *pipeline) run() {
	go p.worker()
	p.recoverInterrupted()
	log.Println("Gravando em blocos de 30 segundos...")
	p.recordLoop()
}

// RunDaemon é o modo terminal ("recorder daemon"): grava e transcreve sem
// janela, numa sessão criada para esta execução, até o processo ser encerrado.
func RunDaemon() {
	log.Println("Iniciando Transcritor Contínuo (Modo Terminal)...")
	if err := datadir.Ensure(); err != nil {
		log.Printf("Aviso: não foi possível preparar a pasta de dados %s: %v", datadir.Dir(), err)
	}

	// Sem o modelo neste computador, ele é baixado em segundo plano (o
	// andamento vai para o log) e a gravação começa assim mesmo: o áudio fica
	// guardado, e o que a fila da transcrição não conseguir esperar é
	// transcrito na próxima vez que o app abrir
	model := transcriber.FetchModel(transcriber.DefaultModel, datadir.Models(), nil)
	defer model.Stop()
	whisperServer := transcriber.Supervise(model, transcriber.Port)
	defer whisperServer.Stop()

	store, err := storage.NewStorage(datadir.DB())
	if err != nil {
		whisperServer.Stop()
		log.Fatalf("Erro ao abrir banco: %v", err)
	}
	defer store.Close()

	capt, err := audio.NewCapture(nil)
	if err != nil {
		whisperServer.Stop()
		log.Fatalf("Erro ao iniciar áudio: %v", err)
	}
	defer capt.Close()
	capt.SetSystemAudio(systemAudioEnabled(store))

	// As falas e o áudio precisam de uma sessão que exista, para aparecerem
	// no app depois
	session, err := store.CreateSession("", time.Now().Format("02/01/2006")+" - Gravação pelo terminal")
	if err != nil {
		whisperServer.Stop()
		log.Fatalf("Erro ao criar a sessão: %v", err)
	}
	rec := newRecording()
	rec.Start(session.ID, 0)

	p := newAppPipeline(store, rec, capt, diarization.Load(datadir.Models()))
	p.run()
}

// systemAudioEnabled lê a preferência de capturar o áudio do sistema junto com o
// microfone. Fica ligada por padrão.
func systemAudioEnabled(store *storage.Storage) bool {
	val, _ := store.GetSetting("system_audio")
	return val != "false"
}

// RunQuery é o outro modo terminal ("recorder query"): responde à pergunta
// com as falas mais recentes, pelo Ollama.
func RunQuery(question string) {
	store, err := storage.NewStorage(datadir.DB())
	if err != nil {
		log.Fatalf("Erro ao abrir banco: %v", err)
	}
	defer store.Close()

	sessID := store.GetDefaultSessionID()
	recent, err := store.GetRecentTranscripts(sessID, 50)
	if err != nil {
		log.Fatalf("Erro ao ler banco: %v", err)
	}

	// O modelo escolhido nas configurações do app; sem escolha, o padrão do cliente
	model, _ := store.GetSetting("ollama_model")
	ollama := chat.NewOllamaClient(chat.DefaultOllamaEndpoint, model)

	fmt.Println("🤖 Pensando...")
	var contextText strings.Builder
	assistant.WriteTranscripts(&contextText, "TRANSCRIÇÕES RECENTES", recent)
	resposta, err := ollama.Answer(question, contextText.String(), nil, nil)
	if err != nil {
		log.Fatalf("Erro ao consultar Ollama: %v", err)
	}
	fmt.Println("\n=========================")
	fmt.Println(resposta)
	fmt.Println("=========================")
}
