// Package backend é o aplicativo do Recorder: liga a gravação, a
// transcrição, a identificação de quem fala, a IA do assistente e o
// Intérprete, e atende a interface.
//
//   - run.go: os três modos de execução (a janela, pelo Wails, e os dois de
//     terminal) e a montagem das peças;
//   - app.go: os métodos que a interface chama (sessões, gravação, resumo,
//     chat, configurações);
//   - recorder.go: a gravação em blocos e a fila que os transcreve;
//   - workspaces.go, notes.go, export.go, llm.go, interpreter.go, status.go:
//     o resto do que a interface faz;
//   - model.go: o que espera o modelo do Whisper, baixado na primeira execução;
//   - fileserver.go e csp.go: o que a janela pode buscar e executar.
//
// A interface (HTML, CSS e JavaScript) fica em frontend/, na raiz do projeto,
// e é embutida no executável pelo main.go de lá.
package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"recorder/internal/assistant"
	"recorder/internal/audio"
	"recorder/internal/chat"
	"recorder/internal/datadir"
	"recorder/internal/diarization"
	"recorder/internal/mac"
	"recorder/internal/roster"
	"recorder/internal/speakers"
	"recorder/internal/storage"
	"recorder/internal/transcriber"
	"recorder/internal/transcript"
	stdruntime "runtime"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct
type App struct {
	ctxMu sync.RWMutex
	ctx   context.Context // janela do Wails; nil até o startup (use wailsCtx/emit)
	// Nos testes: recebe os eventos no lugar da janela. Definido antes de o App ser usado.
	events      func(event string, data ...interface{})
	store       *storage.Storage
	ollama      *chat.OllamaClient
	llm         *chat.Switch // IA escolhida nas configurações (llm.go)
	interpOnce  sync.Once    // Intérprete (interpreter_app.go)
	interpState *interp
	mu          sync.Mutex
	// Servidor local de arquivos (fileserver.go): o endereço, já com o segredo
	fileServerMu  sync.RWMutex
	fileServerURL string
	capture       *audio.Capture
	diarizer      *diarization.Diarizer

	assistant *assistant.Assistant // resumo, tarefas, nomes e chat (Ollama)

	recOnce sync.Once
	rec     *recording // o que a tela pediu à gravação (recorder.go); use recorder()
	pending int        // blocos gravados esperando transcrição

	jobs statusJobs // trabalhos em segundo plano que a tela mostra (status.go)

	// Endereço do whisper-server; vazio é o do app (transcriber.Endpoint)
	whisperEndpoint string
	// O arquivo do modelo do Whisper, baixado na primeira execução (model.go);
	// nil nos testes, que não sobem o whisper-server
	model *transcriber.ModelFile

	audioInfoMu    sync.Mutex
	audioInfoCache map[string]cachedAudioInfo // data e duração de cada áudio de sessão

	capturing atomic.Bool // uma captura de tela está em andamento (notes.go)
}

// whisper devolve o cliente do whisper-server do app.
func (a *App) whisper() *transcriber.WhisperClient {
	return transcriber.NewWhisperClient(a.whisperEndpoint)
}

// recorder devolve o estado da gravação.
func (a *App) recorder() *recording {
	a.recOnce.Do(func() {
		if a.rec == nil {
			a.rec = newRecording()
		}
	})
	return a.rec
}

// NewApp creates a new App application struct
func NewApp(store *storage.Storage, ollama *chat.OllamaClient) *App {
	a := &App{
		store:  store,
		ollama: ollama,
	}
	a.llm = chat.NewSwitch(a.configuredLLM())
	a.assistant = assistant.New(store, a.llm, ollama, a.assistantStatus)
	return a
}

// wailsCtx devolve o contexto da janela do Wails, ou nil antes do startup (e
// nos testes). As goroutines que rodam desde antes de a janela existir (a
// gravação, a transcrição) leem por aqui.
func (a *App) wailsCtx() context.Context {
	a.ctxMu.RLock()
	defer a.ctxMu.RUnlock()
	return a.ctx
}

// emit manda um evento para a tela; sem janela (antes do startup, nos
// testes), não faz nada. Com events definido, o evento vai para ele em vez da
// janela: é por onde os testes veem o que a tela receberia.
func (a *App) emit(event string, data ...interface{}) {
	if a.events != nil {
		a.events(event, data...)
		return
	}
	if ctx := a.wailsCtx(); ctx != nil {
		runtime.EventsEmit(ctx, event, data...)
	}
}

// startup is called when the app starts.
func (a *App) startup(ctx context.Context) {
	a.ctxMu.Lock()
	a.ctx = ctx
	a.ctxMu.Unlock()

	// O áudio das sessões, as capturas de tela e as opções da calibração
	// chegam à janela por um servidor local (fileserver.go)
	a.startFileServer()
}

func (a *App) BroadcastTranscript(t *storage.Transcript) {
	a.emit("transcript", t)
}

func (a *App) BroadcastSummary(sessionID string, summary string) {
	a.emit("summary", map[string]string{"session_id": sessionID, "summary": summary})
}

// BroadcastSpeakersChanged avisa que falas já mostradas da sessão mudaram de
// pessoa (a identificação descobriu que duas vozes eram da mesma pessoa).
func (a *App) BroadcastSpeakersChanged(sessionID string) {
	a.emit("speakers-changed", map[string]string{"session_id": sessionID})
}

func (a *App) BroadcastSessionName(sessionID string, name string) {
	a.emit("session-renamed", map[string]string{"session_id": sessionID, "name": name})
}

// localServices fala com os serviços deste computador (Ollama): eles
// respondem na hora ou não estão rodando, e sem prazo uma chamada presa
// deixava a tela de abertura esperando.
var localServices = &http.Client{Timeout: 3 * time.Second}

func (a *App) CheckServices() map[string]interface{} {
	ollamaStatus := false
	whisperStatus := false

	// Check Ollama
	respOllama, err := localServices.Get(a.ollama.Endpoint + "/api/tags")
	if err == nil {
		respOllama.Body.Close()
		if respOllama.StatusCode == 200 {
			ollamaStatus = true
		}
	}

	// Check Whisper: precisa ser o whisper-server, e não outro programa que
	// responda na mesma porta
	whisperStatus = a.whisper().Running()

	// Com uma IA na nuvem (agy ou claude), o Ollama não é obrigatório (sem
	// ele, o chat não busca em várias reuniões)
	provider := a.llmProvider()
	agyFound := chat.FindAgy() != ""
	claudeFound := chat.FindClaude() != ""
	llmStatus := map[string]bool{chat.ProviderOllama: ollamaStatus, chat.ProviderAgy: agyFound, chat.ProviderClaude: claudeFound}[provider]

	// Com o modelo do Whisper ainda sendo baixado (a primeira execução), o
	// whisper-server não tem como estar de pé: isso não é uma falha, e a tela
	// abre assim mesmo, mostrando o andamento
	model := a.whisperModelStatus()
	whisperExpected := model.State == transcriber.ModelReady

	return map[string]interface{}{
		"provider":     provider,
		"ollama":       ollamaStatus,
		"agy":          agyFound,
		"claude":       claudeFound,
		"whisper":      whisperStatus,
		"model":        model,
		"ok":           llmStatus && (whisperStatus || !whisperExpected),
		"whisper_port": transcriber.Port,
	}
}

// --- Wails Bindings (Called from JS) ---

// SessionInfo é uma sessão com quando e quanto áudio foi gravado, para a lista.
type SessionInfo struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Name        string    `json:"name"`
	Summary     string    `json:"summary"`
	Notes       string    `json:"notes"` // anotações do usuário: a busca da lista também olha nelas
	CreatedAt   time.Time `json:"created_at"`
	RecordedAt  time.Time `json:"recorded_at"` // início da gravação; sem áudio, a criação da sessão
	Duration    float64   `json:"duration"`    // segundos de áudio gravado
}

// GetSessions devolve as sessões de todos os workspaces, da gravada mais
// recentemente para a mais antiga. A tela mostra as do workspace aberto.
func (a *App) GetSessions() ([]SessionInfo, error) {
	sessions, err := a.store.GetSessions()
	if err != nil {
		return nil, err
	}
	// A pasta de áudio é lida uma vez só; antes era varrida uma vez por sessão
	parts := a.audioPartsBySession()
	out := make([]SessionInfo, len(sessions))
	for i, sess := range sessions {
		start, duration := a.partsAudio(parts[sess.ID])
		if start.IsZero() {
			start = sess.CreatedAt
		}
		out[i] = SessionInfo{ID: sess.ID, WorkspaceID: sess.WorkspaceID, Name: sess.Name, Summary: sess.Summary, Notes: sess.Notes, CreatedAt: sess.CreatedAt, RecordedAt: start, Duration: duration.Seconds()}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].RecordedAt.After(out[j].RecordedAt) })
	return out, nil
}

func (a *App) OpenDataDirectory() error {
	appDataDir := datadir.Dir()

	var cmd *exec.Cmd
	switch stdruntime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", appDataDir)
	case "darwin":
		cmd = exec.Command("open", appDataDir)
	default:
		cmd = exec.Command("xdg-open", appDataDir)
	}
	return cmd.Start()
}

// CreateSession cria uma sessão no workspace.
func (a *App) CreateSession(workspaceID, name string) (*storage.Session, error) {
	return a.store.CreateSession(workspaceID, name)
}

// DeleteSession apaga a sessão com tudo dela: falas, resumo, chat, notas,
// capturas de tela e áudio. Se ela estiver sendo gravada, a gravação para.
func (a *App) DeleteSession(id string) error {
	// Só uma sessão que existe: o id entra no nome dos arquivos apagados logo
	// abaixo (o áudio bruto e os blocos esperando transcrição), e um id
	// inventado ("../x", "*") alcançaria arquivos de fora da sessão
	if !a.store.SessionExists(id) {
		return fmt.Errorf("sessão não encontrada")
	}
	rec := a.recorder()
	recording := rec.Recording(id)
	// Fica no log o que foi apagado: sem isso não dava para saber, depois, se
	// uma reunião tinha sumido por um clique ou por um defeito
	if sess, err := a.store.GetSession(id); err == nil {
		log.Printf("Apagando a sessão %s (%q), a pedido do usuário", id, sess.Name)
	}

	// Primeiro o banco: a gravação e a transcrição, ao verem que a sessão não
	// existe mais, descartam o que ainda tinham dela em vez de salvar. Antes, a
	// gravação só parava depois de os arquivos serem apagados, e o último bloco
	// recriava o áudio e as falas de uma sessão que já não existia.
	if err := a.store.DeleteSession(id); err != nil {
		return err
	}
	if recording {
		rec.Stop()
		mac.SetBadge("")
	}
	// Espera a gravação largar o trecho da sessão (ela pode estar terminando de
	// salvar o áudio de uma gravação parada há instantes)
	if !rec.WaitSegmentClosed(id, 30*time.Second) {
		log.Printf("Aviso: a gravação não largou a sessão %s a tempo; pode sobrar áudio dela na pasta", id)
	}

	// Apagar os arquivos de áudio associados
	audioDir := datadir.SessionsAudio()
	for _, part := range a.GetSessionAudioParts(id) {
		os.Remove(filepath.Join(audioDir, part))
	}
	// O áudio bruto da gravação e os blocos dela que esperavam transcrição
	tmpDir := datadir.TmpAudio()
	os.Remove(audio.RawFile(tmpDir, id))
	for _, b := range audio.SessionBlocks(tmpDir, id) {
		os.Remove(b)
	}

	// E as capturas de tela da sessão
	if dir := screenshotsDir(id); dir != "" {
		os.RemoveAll(dir)
	}
	return nil
}

// isSessionAudio diz se name é um arquivo de áudio da sessão: o principal
// (<id>.m4a, ou .wav no Windows) ou uma parte à parte (<id>_part_<n>).
func isSessionAudio(name, sessionID string) bool {
	if !strings.HasSuffix(name, ".m4a") && !strings.HasSuffix(name, ".wav") {
		return false
	}
	base := name[:len(name)-4]
	return base == sessionID || strings.HasPrefix(base, sessionID+"_part_")
}

// GetSessionAudioParts lista os arquivos de áudio da sessão, em ordem: o
// principal e, se houver, as partes à parte (sessões antigas, ou um trecho que
// não pôde ser juntado ao áudio já salvo).
func (a *App) GetSessionAudioParts(sessionID string) []string {
	parts := []string{}
	if sessionID == "" {
		return parts
	}
	entries, _ := os.ReadDir(datadir.SessionsAudio())
	for _, entry := range entries {
		if !entry.IsDir() && isSessionAudio(entry.Name(), sessionID) {
			parts = append(parts, entry.Name())
		}
	}
	sort.Strings(parts)
	return parts
}

// audioPartsBySession lê a pasta de áudio uma vez e devolve os arquivos de
// cada sessão, em ordem.
func (a *App) audioPartsBySession() map[string][]string {
	bySession := map[string][]string{}
	entries, _ := os.ReadDir(datadir.SessionsAudio())
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) < 5 {
			continue
		}
		id, _, _ := strings.Cut(name[:len(name)-4], "_part_")
		if isSessionAudio(name, id) {
			bySession[id] = append(bySession[id], name)
		}
	}
	for _, parts := range bySession {
		sort.Strings(parts)
	}
	return bySession
}

// ReprocessSession transcreve de novo o áudio salvo da sessão, apagando a
// transcrição atual, e refaz o resumo.
func (a *App) ReprocessSession(sessionID string) error {
	log.Printf("Iniciando reprocessamento da sessão %s", sessionID)
	if len(a.GetSessionAudioParts(sessionID)) == 0 {
		return fmt.Errorf("nenhum áudio encontrado para reprocessar")
	}
	if a.recorder().Recording(sessionID) {
		return fmt.Errorf("pare a gravação antes de reprocessar a reunião")
	}
	if err := a.requireWhisperModel(); err != nil {
		return fmt.Errorf("%w; a transcrição atual foi mantida", err)
	}
	// A transcrição atual é apagada antes de a nova começar: sem o Whisper
	// pronto, a sessão ficaria sem nenhuma, e sem os nomes dados às vozes
	if !a.whisper().Ready() {
		return fmt.Errorf("o Whisper não está pronto (pode estar carregando o modelo); a transcrição atual foi mantida, tente de novo em instantes")
	}
	if err := a.transcribeSessionAudio(sessionID, "Audio Reprocessado", "Reprocessando áudio"); err != nil {
		return err
	}
	if a.store.SessionExists(sessionID) {
		go a.generateSummary(sessionID)
	}
	return nil
}

// Formatos oferecidos ao importar um áudio (ver audio.Decode)
const importPattern = "*.m4a;*.mp3;*.wav;*.aac;*.ogg;*.opus;*.oga;*.flac;*.amr;*.3gp;*.caf;*.aif;*.aiff;*.mp4;*.mov;*.webm"

// ImportAudio pede um áudio gravado fora do app (ex: no celular), cria uma
// sessão com ele no workspace e o transcreve em segundo plano, como uma
// gravação feita aqui: identifica quem fala e faz o resumo no fim. Devolve a
// sessão criada, ou nil se o usuário desistiu.
func (a *App) ImportAudio(workspaceID string) (*storage.Session, error) {
	// Sem o modelo, o áudio importado ficaria numa reunião sem transcrição
	if err := a.requireWhisperModel(); err != nil {
		return nil, err
	}
	path, err := runtime.OpenFileDialog(a.wailsCtx(), runtime.OpenDialogOptions{
		Title:   "Importar áudio",
		Filters: []runtime.FileFilter{{DisplayName: "Áudio e vídeo", Pattern: importPattern}},
	})
	if err != nil || path == "" {
		return nil, err
	}

	samples, err := audio.Decode(path)
	if err != nil {
		return nil, err
	}
	if len(samples) < 16000 {
		return nil, fmt.Errorf("o arquivo %s não tem áudio", filepath.Base(path))
	}
	duration := time.Duration(len(samples)) * time.Second / 16000
	end := importedEnd(path, duration)
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	session, err := a.store.CreateSession(workspaceID, end.Add(-duration).Format("02/01/2006")+" - "+name)
	if err != nil {
		return nil, err
	}

	saved, err := audio.AppendSession(datadir.SessionsAudio(), session.ID, samples)
	if err != nil {
		a.store.DeleteSession(session.ID)
		return nil, fmt.Errorf("erro ao salvar o áudio importado: %w", err)
	}
	// A lista de sessões mostra quando o áudio foi gravado, e não quando foi importado
	if err := audio.SetRecordedAt(saved, end); err != nil {
		log.Printf("Aviso: não foi possível gravar a data do áudio importado: %v", err)
	}
	log.Printf("Áudio importado de %s na sessão %s (%s)", path, session.ID, duration.Round(time.Second))

	go func() {
		if err := a.transcribeSessionAudio(session.ID, "Audio Importado", "Transcrevendo o áudio importado"); err != nil {
			// Ninguém espera esta chamada: a tela avisa
			a.emit("notice", map[string]string{
				"session_id": session.ID,
				"message":    "A transcrição do áudio importado parou no meio: o Whisper parou de responder e não voltou. O áudio está salvo na reunião; use Reprocessar para transcrever de novo.",
			})
			return
		}
		if a.store.SessionExists(session.ID) {
			a.generateSummary(session.ID)
		}
	}()
	return session, nil
}

// importedEnd estima quando terminou a gravação importada: pelos metadados do
// arquivo (m4a, mp4, mov, wav) ou, nos outros formatos, pela data de
// modificação dele.
func importedEnd(path string, duration time.Duration) time.Time {
	if start, _, err := audio.Info(path); err == nil && start.Year() >= 2000 && start.Before(time.Now()) {
		return start.Add(duration)
	}
	if info, err := os.Stat(path); err == nil {
		return info.ModTime()
	}
	return time.Now()
}

// transcribeSessionAudio transcreve do zero o áudio salvo da sessão, apagando
// as falas e as vozes que ela tinha. source marca as falas (ex: "Audio
// Reprocessado") e status é o que aparece na tela, com o andamento. No fim,
// junta as vozes com pouco áudio, como ao terminar uma gravação.
//
// Se o Whisper sair do ar no meio e não voltar, devolve erro e a sessão fica
// com as falas e as vozes que tinha antes.
func (a *App) transcribeSessionAudio(sessionID, source, status string) error {
	job := a.beginJob(sessionID, status+" (0%)")
	defer job.end()
	if err := a.transcribeParts(sessionID, source, status, job); err != nil {
		return err
	}
	if a.store.SessionExists(sessionID) {
		speakers.Consolidate(a.store, sessionID)
	}
	return nil
}

// errWhisperGone: o Whisper parou de responder no meio de uma transcrição e
// não voltou a tempo.
var errWhisperGone = errors.New("o Whisper parou de responder no meio da transcrição e não voltou; a transcrição anterior foi mantida")

func (a *App) transcribeParts(sessionID, source, status string, job *statusJob) (err error) {
	unlock := speakers.Lock(sessionID)
	defer unlock()

	// O que a sessão tinha, para devolver se a transcrição nova não puder terminar
	oldTranscripts, _ := a.store.GetAllTranscripts(sessionID)
	oldVoices, _ := a.store.GetSpeakerVoices(sessionID)
	oldSuggestions, _ := a.store.GetSpeakerSuggestions(sessionID)
	defer func() {
		if !errors.Is(err, errWhisperGone) || !a.store.SessionExists(sessionID) {
			return
		}
		a.store.DeleteSessionTranscripts(sessionID)
		a.store.DeleteSessionSpeakers(sessionID)
		if restoreErr := a.store.RestoreTranscripts(sessionID, oldTranscripts); restoreErr != nil {
			log.Printf("Erro ao devolver as falas anteriores da sessão %s: %v", sessionID, restoreErr)
		}
		a.store.SaveSpeakerVoices(sessionID, oldVoices)
		a.store.ReplaceSpeakerSuggestions(sessionID, oldSuggestions)
		// A tela já mostrou falas da transcrição nova: recarrega as da sessão
		a.BroadcastSpeakersChanged(sessionID)
	}()

	a.store.DeleteSessionTranscripts(sessionID)
	a.store.DeleteSessionSpeakers(sessionID)
	a.store.DeleteSessionPassages(sessionID)
	a.store.ReplaceSpeakerSuggestions(sessionID, nil)

	whisper := a.whisper()
	audioDir := datadir.SessionsAudio()
	parts := a.GetSessionAudioParts(sessionID)

	// Um arquivo por chamada: um áudio importado pode estar sendo transcrito
	// enquanto outra sessão é reprocessada
	tmp, err := os.CreateTemp("", "recorder-block-*.wav")
	if err != nil {
		log.Printf("Erro ao criar o arquivo do bloco: %v", err)
		return nil
	}
	tmp.Close()
	blockWav := tmp.Name()
	defer os.Remove(blockWav)

	// Onde cada bloco começa no áudio da sessão (as partes antigas tocam em sequência)
	var offset float64
	for i, part := range parts {
		samples, err := audio.LoadFile(filepath.Join(audioDir, part))
		if err != nil {
			log.Printf("Erro ao ler %s: %v", part, err)
			continue
		}

		// Em blocos de ~30s, como na gravação ao vivo. Com o arquivo inteiro de
		// uma vez, o Whisper detecta o idioma só pelos primeiros 30s (numa reunião
		// que começa em francês, transcrevia tudo em francês) e, num áudio longo,
		// entra em loop repetindo a mesma frase. As vozes da sessão foram apagadas
		// acima: a identificação de quem fala recomeça do zero.
		blocks := audio.SplitBlocks(samples, 30*time.Second)
		var previous []int16
		for j, block := range blocks {
			blockStart := offset
			offset += float64(len(block)) / 16000
			progress := int((float64(i) + float64(j)/float64(len(blocks))) / float64(len(parts)) * 100)
			job.update(fmt.Sprintf("%s (%d%%)", status, progress))

			if err := audio.SaveWav(blockWav, block, 16000); err != nil {
				log.Printf("Erro ao salvar bloco de %s: %v", part, err)
				continue
			}
			working := fmt.Sprintf("%s (%d%%)", status, progress)
			// Uma frase do Intérprete tem a vez na GPU (ver pipeline.waitTurn)
			for deadline := time.Now().Add(yieldMax); a.interpreterBusy() && time.Now().Before(deadline); {
				time.Sleep(yieldPoll)
			}
			utterances, err := transcribeRetrying(whisper, func(waiting bool) {
				// A espera pode levar minutos: a tela diz o que está acontecendo
				if waiting {
					job.update(working + ": esperando o Whisper voltar...")
				} else {
					job.update(working)
				}
			}, func() ([]transcript.Utterance, error) {
				return transcript.Transcribe(whisper, a.diarizer, a.store, sessionID, blockWav, block, previous)
			})
			previous = block
			var refused *transcriber.StatusError
			if err != nil && !(errors.As(err, &refused) && !refused.Loading()) {
				// O Whisper não está respondendo: continuar seria esperar minutos
				// em cada bloco restante para acabar sem transcrição nenhuma
				log.Printf("Transcrição da sessão %s interrompida em %.0fs: %v", sessionID, blockStart, err)
				return errWhisperGone
			}
			if err != nil {
				log.Printf("Erro na transcrição de %s (bloco em %.0fs, que fica sem transcrição): %v", part, blockStart, err)
				continue
			}
			for _, u := range utterances {
				t, err := a.store.InsertTranscript(sessionID, source, u.Speaker, u.Text, blockStart+u.Start)
				if errors.Is(err, storage.ErrNoSession) {
					// A sessão foi apagada no meio: para, e tira o que a transcrição
					// gravou dela (as vozes)
					log.Printf("A sessão %s foi apagada durante a transcrição; parando", sessionID)
					a.store.DeleteSession(sessionID)
					return nil
				}
				if err != nil {
					log.Printf("Erro ao gravar uma fala da sessão %s: %v", sessionID, err)
					continue
				}
				a.BroadcastTranscript(t)
			}
		}
	}
	return nil
}

// transcribeRetrying transcreve um bloco e, se o Whisper cair ou travar no
// meio (ele sobe de novo sozinho), espera ele voltar e tenta de novo, como na
// gravação ao vivo. Antes, cada bloco que falhava ficava sem transcrição.
// onWait avisa quando a espera pelo Whisper começa (true) e termina (false).
func transcribeRetrying(whisper *transcriber.WhisperClient, onWait func(waiting bool), transcribe func() ([]transcript.Utterance, error)) ([]transcript.Utterance, error) {
	for attempt := 1; ; attempt++ {
		utterances, err := transcribe()
		var refused *transcriber.StatusError
		if err == nil || attempt >= transcribeAttempts || (errors.As(err, &refused) && !refused.Loading()) {
			return utterances, err
		}
		log.Printf("Erro na transcrição (tentativa %d de %d), esperando o Whisper: %v", attempt, transcribeAttempts, err)
		onWait(true)
		for deadline := time.Now().Add(whisperWait); !whisper.Ready() && time.Now().Before(deadline); {
			time.Sleep(whisperPoll)
		}
		onWait(false)
	}
}

// GetStatus diz se está gravando, em qual sessão, desde quando (em
// milissegundos Unix, 0 se não estiver gravando) e quantos blocos gravados
// esperam transcrição.
func (a *App) GetStatus() map[string]interface{} {
	recording, sessionID, started := a.recorder().Status()
	var startedAt int64
	if recording {
		startedAt = started.UnixMilli()
	}
	a.mu.Lock()
	pending := a.pending
	a.mu.Unlock()
	return map[string]interface{}{
		"recording":  recording,
		"session_id": sessionID,
		"started_at": startedAt,
		"pending":    pending,
	}
}

// setTranscriptionQueue guarda quantos blocos esperam transcrição e avisa a tela.
func (a *App) setTranscriptionQueue(pending int) {
	a.mu.Lock()
	a.pending = pending
	a.mu.Unlock()
	a.emit("transcription-queue", map[string]int{"pending": pending})
}

// IsBackendReady diz se o whisper-server está atendendo na porta do Recorder.
func (a *App) IsBackendReady() bool {
	return a.whisper().Running()
}

// StartRecording começa a gravar na sessão (o laço de gravação acorda na
// hora; antes ele conferia o pedido uma vez por segundo e o começo da fala
// podia se perder).
func (a *App) StartRecording(sessionID string) (map[string]interface{}, error) {
	if !a.store.SessionExists(sessionID) {
		return a.GetStatus(), fmt.Errorf("reunião não encontrada")
	}
	if err := a.requireWhisperModel(); err != nil {
		return a.GetStatus(), err
	}
	// A gravação continua de onde o áudio da sessão parou: as capturas de
	// tela guardam o momento da reunião a partir dali
	base := audio.SessionDuration(datadir.SessionsAudio(), sessionID)
	if a.recorder().Start(sessionID, base) {
		mac.SetBadge("🔴")
	}
	return a.GetStatus(), nil
}

func (a *App) StopRecording() map[string]interface{} {
	a.recorder().Stop()
	mac.SetBadge("")
	return a.GetStatus()
}

func (a *App) GenerateSummaryAsync(sessionID string) {
	go a.generateSummary(sessionID)
}

func (a *App) generateSummary(sessionID string) {
	log.Printf("Gerando resumo para a sessão %s...", sessionID)
	transcripts, err := a.store.GetAllTranscripts(sessionID)
	if err != nil || len(transcripts) == 0 {
		return
	}

	job := a.beginJob(sessionID, "Procurando os nomes das pessoas...")
	defer job.end()
	// Os nomes primeiro, para o resumo e as tarefas já saírem com eles
	if a.nameSpeakers(sessionID, transcripts) {
		if named, err := a.store.GetAllTranscripts(sessionID); err == nil && len(named) > 0 {
			transcripts = named
		}
	}
	job.update("Gerando resumo da sessão...")
	answer, err := a.assistant.Summarize("", transcripts, a.participants(sessionID), a.summaryProgress(sessionID))
	if err != nil {
		log.Printf("Erro ao gerar resumo: %v", err)
		return
	}
	err = a.store.UpdateSessionSummary(sessionID, answer)
	if err != nil {
		log.Printf("Erro ao salvar resumo: %v", err)
		return
	}
	a.BroadcastSummary(sessionID, answer)

	// O título vem junto com as tarefas, numa resposta em formato fixo
	title := a.updateTasks(sessionID, answer)
	if name := a.suggestedSessionName(sessionID, title); name != "" {
		a.emit("suggest-session-name", map[string]interface{}{
			"session_id": sessionID,
			"name":       name,
		})
	}
}

// suggestedSessionName monta o nome no padrão "dd/mm/aaaa - Título", com a
// data em que o áudio da sessão foi gravado. Devolve "" se o título não servir.
func (a *App) suggestedSessionName(sessionID, title string) string {
	title = strings.Trim(strings.TrimSpace(title), "\"'*.")
	if title == "" || utf8.RuneCountInString(title) > 60 {
		return ""
	}
	return a.sessionDate(sessionID).Format("02/01/2006") + " - " + title
}

// sessionDate é quando o áudio da sessão começou a ser gravado, pelos
// metadados do arquivo; sem áudio, a data em que a sessão foi criada.
func (a *App) sessionDate(sessionID string) time.Time {
	if start, _ := a.sessionAudio(sessionID); !start.IsZero() {
		return start
	}
	if sess, err := a.store.GetSession(sessionID); err == nil {
		return sess.CreatedAt
	}
	return time.Now()
}

// sessionAudio devolve quando o áudio da sessão começou a ser gravado (zero se
// não houver áudio) e quanto ele dura, somando as partes antigas.
func (a *App) sessionAudio(sessionID string) (start time.Time, duration time.Duration) {
	return a.partsAudio(a.GetSessionAudioParts(sessionID))
}

// cachedAudioInfo é a data e a duração lidas de um arquivo de áudio, válidas
// enquanto ele não mudar.
type cachedAudioInfo struct {
	modTime  time.Time
	size     int64
	start    time.Time
	duration time.Duration
}

// partsAudio soma os arquivos de áudio de uma sessão: quando o primeiro
// começou a ser gravado e quanto eles duram juntos. A lista de sessões pede
// isso para todas a cada atualização, então o que foi lido de cada arquivo
// fica guardado até ele mudar.
func (a *App) partsAudio(parts []string) (start time.Time, duration time.Duration) {
	audioDir := datadir.SessionsAudio()
	for _, part := range parts {
		path := filepath.Join(audioDir, part)
		stat, err := os.Stat(path)
		if err != nil {
			continue
		}
		a.audioInfoMu.Lock()
		cached, ok := a.audioInfoCache[path]
		a.audioInfoMu.Unlock()
		if !ok || !cached.modTime.Equal(stat.ModTime()) || cached.size != stat.Size() {
			partStart, partDuration, err := audio.Info(path)
			if err != nil {
				continue
			}
			cached = cachedAudioInfo{modTime: stat.ModTime(), size: stat.Size(), start: partStart, duration: partDuration}
			a.audioInfoMu.Lock()
			if a.audioInfoCache == nil {
				a.audioInfoCache = map[string]cachedAudioInfo{}
			}
			a.audioInfoCache[path] = cached
			a.audioInfoMu.Unlock()
		}
		if start.IsZero() {
			start = cached.start
		}
		duration += cached.duration
	}
	return start, duration
}

// RenameSession called from frontend when user accepts or edits the suggested name
func (a *App) RenameSession(sessionID string, newName string) error {
	if newName == "" {
		return nil
	}
	if err := a.store.UpdateSessionName(sessionID, newName); err != nil {
		return err
	}
	a.BroadcastSessionName(sessionID, newName)
	return nil
}

// RegenerateSummary allows generating a new summary passing specific user instructions
func (a *App) RegenerateSummary(sessionID string, instructions string) error {
	log.Printf("Regerando resumo para a sessão %s com instruções extras...", sessionID)
	transcripts, err := a.store.GetAllTranscripts(sessionID)
	if err != nil || len(transcripts) == 0 {
		return fmt.Errorf("no transcripts found")
	}

	job := a.beginJob(sessionID, "Gerando novo resumo da sessão...")
	defer job.end()
	answer, err := a.assistant.Summarize(instructions, transcripts, a.participants(sessionID), a.summaryProgress(sessionID))
	if err != nil {
		log.Printf("Erro ao regerar resumo: %v", err)
		return err
	}

	err = a.store.UpdateSessionSummary(sessionID, answer)
	if err != nil {
		log.Printf("Erro ao salvar novo resumo: %v", err)
		return err
	}

	a.BroadcastSummary(sessionID, answer)
	a.updateTasks(sessionID, answer)
	return nil
}

func (a *App) GetTranscripts(sessionID string) ([]storage.Transcript, error) {
	if sessionID == "" {
		sessionID = a.store.GetDefaultSessionID()
	}
	return a.store.GetAllTranscripts(sessionID)
}

type ChatResponse struct {
	Answer         string `json:"answer"`
	UserMsgID      int    `json:"user_msg_id"`
	AssistantMsgID int    `json:"assistant_msg_id"`
}

// SendChatMessage responde à pergunta com o resumo, os trechos da transcrição
// encontrados pela busca (desta sessão ou, com allSessions, de todas) e as
// falas mais recentes. A resposta vai aparecendo na tela pelo evento
// "chat-progress" enquanto é escrita.
func (a *App) SendChatMessage(sessionID string, question string, allSessions bool) (ChatResponse, error) {
	if sessionID == "" {
		sessionID = a.store.GetDefaultSessionID()
	}

	answer, err := a.assistant.Ask(sessionID, question, allSessions, a.chatProgress(sessionID))
	if err != nil {
		return ChatResponse{}, err
	}
	userMsgID, err := a.store.SaveChatMessage(sessionID, "user", question)
	if err != nil {
		log.Printf("Erro ao guardar a pergunta do chat: %v", err)
	}
	assistantMsgID, err := a.store.SaveChatMessage(sessionID, "assistant", answer)
	if err != nil {
		log.Printf("Erro ao guardar a resposta do chat: %v", err)
	}

	return ChatResponse{
		Answer:         answer,
		UserMsgID:      userMsgID,
		AssistantMsgID: assistantMsgID,
	}, nil
}

func (a *App) GetChatHistory(sessionID string) ([]storage.ChatMessage, error) {
	if sessionID == "" {
		sessionID = a.store.GetDefaultSessionID()
	}
	msgs, err := a.store.GetChatMessages(sessionID)
	if err != nil {
		return nil, err
	}
	if msgs == nil {
		msgs = []storage.ChatMessage{}
	}
	return msgs, nil
}

func (a *App) DeleteChatMessage(id int) error {
	return a.store.DeleteChatMessage(id)
}

func (a *App) GetOllamaModels() ([]string, error) {
	resp, err := localServices.Get(a.ollama.Endpoint + "/api/tags")
	if err != nil {
		return nil, fmt.Errorf("falha ao conectar no Ollama (está rodando?): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status inesperado do Ollama: %d", resp.StatusCode)
	}

	var data struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}

	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}

	var models []string
	for _, m := range data.Models {
		models = append(models, m.Name)
	}
	return models, nil
}

func (a *App) GetSystemAudioEnabled() bool {
	return systemAudioEnabled(a.store)
}

// SetSystemAudioEnabled liga/desliga a captura do áudio do computador junto com
// o microfone. Se estiver gravando, a mudança vale imediatamente.
func (a *App) SetSystemAudioEnabled(enabled bool) error {
	if err := a.store.SetSetting("system_audio", strconv.FormatBool(enabled)); err != nil {
		return err
	}
	if a.capture != nil {
		return a.capture.SetSystemAudio(enabled)
	}
	return nil
}

// RenameSpeaker dá um nome a uma voz da sessão (ex: "Pessoa 3" -> "Ana") e
// guarda a voz para reconhecer a pessoa nas próximas sessões do workspace.
func (a *App) RenameSpeaker(sessionID, from, to string) error {
	return speakers.Rename(a.store, sessionID, from, to)
}

// GetKnownPeople lista as pessoas com voz guardada no workspace.
func (a *App) GetKnownPeople(workspaceID string) ([]string, error) {
	people, err := a.store.GetPeopleVoices(workspaceID)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(people))
	for i, p := range people {
		names[i] = p.Label
	}
	return names, nil
}

// RenamePerson corrige o nome de uma pessoa com voz guardada no workspace (ex:
// "Diego" -> "Diogo"). As falas, as vozes e as tarefas das reuniões do
// workspace também mudam; se o nome novo já existir, as duas vozes viram uma.
func (a *App) RenamePerson(workspaceID, from, to string) error {
	changed, err := speakers.RenamePerson(a.store, workspaceID, from, to)
	for _, sessionID := range changed {
		a.BroadcastSpeakersChanged(sessionID)
		a.emit("tasks", map[string]string{"session_id": sessionID})
	}
	return err
}

// ForgetPerson apaga a voz guardada de uma pessoa no workspace. As
// transcrições já feitas continuam com o nome dela.
func (a *App) ForgetPerson(workspaceID, name string) error {
	return speakers.Forget(a.store, workspaceID, name)
}

func (a *App) chatProgress(sessionID string) func(string) {
	var last time.Time
	return func(text string) {
		if time.Since(last) < 100*time.Millisecond {
			return
		}
		last = time.Now()
		a.emit("chat-progress", map[string]string{"session_id": sessionID, "answer": text})
	}
}

// GetTasks lista as tarefas combinadas na sessão.
func (a *App) GetTasks(sessionID string) ([]storage.Task, error) {
	return a.store.GetTasks(sessionID)
}

func (a *App) SetTaskDone(taskID int, done bool) error {
	return a.store.SetTaskDone(taskID, done)
}

// GetSpeakerSuggestions lista os nomes prováveis das pessoas da sessão, tirados da conversa.
func (a *App) GetSpeakerSuggestions(sessionID string) ([]storage.SpeakerSuggestion, error) {
	return a.store.GetSpeakerSuggestions(sessionID)
}

// ConfirmSpeakerSuggestion aplica a sugestão: a voz passa a ter o nome e fica
// guardada para as próximas sessões. Num nome que o app já tinha dado sozinho
// (speaker igual a name), só tira o aviso da tela.
func (a *App) ConfirmSpeakerSuggestion(sessionID, speaker, name string) error {
	if speaker == name {
		return a.store.DeleteSpeakerSuggestion(sessionID, speaker)
	}
	return speakers.Rename(a.store, sessionID, speaker, name)
}

// DismissSpeakerSuggestion recusa a sugestão. Num nome que o app já tinha
// dado sozinho, desfaz: a voz volta a ser uma "Pessoa N" e deixa de contar
// para a voz guardada da pessoa.
func (a *App) DismissSpeakerSuggestion(sessionID, speaker string) error {
	suggestions, err := a.store.GetSpeakerSuggestions(sessionID)
	if err != nil {
		return err
	}
	for _, sg := range suggestions {
		if sg.Speaker != speaker || sg.Name != speaker {
			continue
		}
		label, err := speakers.Unname(a.store, sessionID, speaker)
		if err != nil {
			return err
		}
		log.Printf("Nome %q desfeito na sessão %s: a voz voltou a ser %s", speaker, sessionID, label)
		a.BroadcastSpeakersChanged(sessionID)
		a.emit("tasks", map[string]string{"session_id": sessionID})
	}
	return a.store.DeleteSpeakerSuggestion(sessionID, speaker)
}

// GetParticipants devolve a lista das pessoas que costumam estar nas reuniões
// do workspace (uma por linha, com os apelidos entre parênteses). Ela serve
// para os nomes saírem escritos certo na transcrição, no resumo e nas tarefas.
func (a *App) GetParticipants(workspaceID string) (string, error) {
	return a.store.GetParticipants(workspaceID)
}

func (a *App) SetParticipants(workspaceID, participants string) error {
	return a.store.SetParticipants(workspaceID, strings.TrimSpace(participants))
}

// participants é a lista de participantes do workspace da sessão.
func (a *App) participants(sessionID string) []roster.Person {
	text, err := a.store.GetParticipants(a.store.SessionWorkspace(sessionID))
	if err != nil {
		log.Printf("Erro ao ler os participantes do workspace: %v", err)
	}
	return roster.Parse(text)
}

// GetGlossary devolve os termos que o Whisper deve reconhecer nas reuniões do
// workspace (um por linha).
func (a *App) GetGlossary(workspaceID string) (string, error) {
	return a.store.GetGlossary(workspaceID)
}

func (a *App) SetGlossary(workspaceID, glossary string) error {
	return a.store.SetGlossary(workspaceID, strings.TrimSpace(glossary))
}

// summaryProgress manda para a tela o resumo enquanto ele é escrito.
func (a *App) summaryProgress(sessionID string) func(string) {
	var last time.Time
	return func(text string) {
		if time.Since(last) < 150*time.Millisecond {
			return
		}
		last = time.Now()
		a.emit("summary-progress", map[string]string{"session_id": sessionID, "summary": text})
	}
}

// updateTasks extrai as tarefas do resumo e as mostra na tela. Devolve o título sugerido.
func (a *App) updateTasks(sessionID, summary string) string {
	a.statusMessage("Separando as tarefas...")
	title, tasks, err := a.assistant.ExtractInsights(summary)
	if err != nil {
		log.Printf("Erro ao extrair as tarefas do resumo: %v", err)
		return ""
	}
	if err := a.store.ReplaceTasks(sessionID, tasks); err != nil {
		log.Printf("Erro ao salvar as tarefas: %v", err)
	}
	a.emit("tasks", map[string]string{"session_id": sessionID})
	return title
}

// nameSpeakers procura na conversa o nome de cada "Pessoa N". O nome com
// pistas bastantes já é dado à voz (e fica na tela o trecho que o comprova,
// com como desfazer); os outros viram sugestões para o usuário confirmar.
// Devolve true se alguma voz mudou de nome.
func (a *App) nameSpeakers(sessionID string, transcripts []storage.Transcript) bool {
	guesses := a.assistant.IdentifySpeakers(transcripts, a.participants(sessionID))
	if guesses == nil {
		return false
	}
	voices, _ := a.store.GetSpeakerVoices(sessionID)
	has := map[string]bool{}
	for _, v := range voices {
		has[v.Label] = true
	}
	suggestions := []storage.SpeakerSuggestion{}
	renamed := false
	for _, g := range guesses {
		// Enquanto a IA respondia, o usuário pode ter dado nome à voz
		if !has[g.Speaker] {
			continue
		}
		if g.Sure {
			if err := speakers.Rename(a.store, sessionID, g.Speaker, g.Name); err != nil {
				log.Printf("Erro ao dar o nome %q a %s: %v", g.Name, g.Speaker, err)
				suggestions = append(suggestions, g.SpeakerSuggestion)
				continue
			}
			// Sem o trecho que comprova (ele fica no banco e aparece na tela):
			// o que foi dito nas reuniões não vai para o log
			log.Printf("%s é %s, pela conversa", g.Speaker, g.Name)
			renamed = true
			suggestions = append(suggestions, storage.SpeakerSuggestion{Speaker: g.Name, Name: g.Name, Evidence: g.Evidence})
			continue
		}
		suggestions = append(suggestions, g.SpeakerSuggestion)
	}
	if err := a.store.ReplaceSpeakerSuggestions(sessionID, suggestions); err != nil {
		log.Printf("Erro ao salvar as sugestões de nomes: %v", err)
	}
	if renamed {
		a.BroadcastSpeakersChanged(sessionID)
	}
	a.emit("speaker-suggestions", map[string]string{"session_id": sessionID})
	return renamed
}
