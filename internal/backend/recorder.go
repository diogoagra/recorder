package backend

import (
	"context"
	"errors"
	"log"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"recorder/internal/audio"
	"recorder/internal/speakers"
	"recorder/internal/storage"
	"recorder/internal/transcriber"
	"recorder/internal/transcript"
)

// A gravação tem três partes: o pedido da tela (recording: gravar em qual
// sessão, parar), o laço que grava em blocos de 30 segundos (recordLoop) e a
// fila que transcreve os blocos (worker). O laço e a fila rodam cada um na
// sua goroutine desde que o app abre.

// recording é o que a tela pediu à gravação e o que o laço de gravação está
// fazendo. Tudo passa pela mesma trava: antes, o pedido ficava no App e o
// cancelamento do bloco numa variável solta, lidos sem trava por goroutines
// diferentes.
type recording struct {
	mu        sync.Mutex
	wanted    bool      // a tela pediu para gravar
	sessionID string    // em qual sessão
	started   time.Time // quando o pedido foi feito
	base      float64   // segundos de áudio que a sessão já tinha nessa hora
	closing   bool      // o app está fechando

	cancel  context.CancelFunc // do bloco sendo gravado agora; nil entre blocos
	segment string             // sessão com um trecho aberto no laço de gravação; "" se nenhum

	// Fecha a cada mudança, para acordar quem espera (o laço parado, quem
	// espera o trecho de uma sessão fechar)
	changed chan struct{}
	parked  chan struct{} // fecha quando o laço para de vez, no fechamento do app
}

func newRecording() *recording {
	return &recording{changed: make(chan struct{}), parked: make(chan struct{})}
}

func (r *recording) notifyLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// Start pede para gravar na sessão; base é quanto áudio ela já tem salvo.
// Devolve false se o app está fechando.
func (r *recording) Start(sessionID string, base float64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return false
	}
	if r.wanted && r.sessionID == sessionID {
		return true // já está gravando nela
	}
	// Passando direto de uma sessão para outra: o bloco em andamento é da anterior
	if r.cancel != nil {
		r.cancel()
	}
	r.wanted, r.sessionID, r.started, r.base = true, sessionID, time.Now(), base
	r.notifyLocked()
	return true
}

// Stop para a gravação: o bloco em andamento é interrompido e o que já foi
// gravado dele é salvo.
func (r *recording) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.wanted {
		return
	}
	// Primeiro o pedido, depois o cancelamento: o laço, ao voltar do bloco
	// interrompido, já encontra o pedido de parar e não começa outro
	r.wanted, r.sessionID = false, ""
	if r.cancel != nil {
		r.cancel()
	}
	r.notifyLocked()
}

// Close avisa que o app está fechando: interrompe o bloco atual, para o que
// já foi gravado ir para o disco, e não deixa começar outro. Devolve se
// estava gravando.
func (r *recording) Close() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closing {
		r.closing = true
		if r.cancel != nil {
			r.cancel()
		}
		r.notifyLocked()
	}
	return r.wanted
}

// Status diz se a tela pediu para gravar, em qual sessão e desde quando.
func (r *recording) Status() (recording bool, sessionID string, started time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wanted, r.sessionID, r.started
}

// Recording diz se está gravando na sessão.
func (r *recording) Recording(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wanted && r.sessionID == sessionID
}

// Offset devolve em que momento do áudio da sessão a gravação está agora, em
// segundos; negativo se a sessão não estiver sendo gravada.
func (r *recording) Offset(sessionID string) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.wanted || r.sessionID != sessionID {
		return -1
	}
	return r.base + time.Since(r.started).Seconds()
}

// state é o que o laço de gravação consulta a cada volta. changed fecha na
// próxima mudança: vem junto com o estado para uma mudança entre a consulta
// e a espera não se perder.
func (r *recording) state() (wanted bool, sessionID string, closing bool, changed <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wanted, r.sessionID, r.closing, r.changed
}

// beginBlock registra como cancelar o bloco que o laço vai gravar para a
// sessão. Devolve false, sem registrar, se nesse meio tempo a tela pediu
// para parar, trocou de sessão ou o app começou a fechar: sem isso, um
// "parar" entre a consulta do estado e o começo do bloco não interrompia
// nada, e a gravação seguia por mais um bloco inteiro.
func (r *recording) beginBlock(sessionID string, cancel context.CancelFunc) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing || !r.wanted || r.sessionID != sessionID {
		return false
	}
	r.cancel = cancel
	return true
}

func (r *recording) endBlock() {
	r.mu.Lock()
	r.cancel = nil
	r.mu.Unlock()
}

// setSegment diz de qual sessão é o trecho que o laço está gravando ("" ao
// fechá-lo, depois de salvar o áudio).
func (r *recording) setSegment(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.segment != sessionID {
		r.segment = sessionID
		r.notifyLocked()
	}
}

// WaitSegmentClosed espera o laço de gravação fechar o trecho da sessão (o
// áudio dela já salvo ou descartado), até timeout. Devolve se fechou.
func (r *recording) WaitSegmentClosed(sessionID string, timeout time.Duration) bool {
	if sessionID == "" {
		return true // "" é "nenhum trecho aberto", não uma sessão
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		r.mu.Lock()
		open, changed := r.segment == sessionID, r.changed
		r.mu.Unlock()
		if !open {
			return true
		}
		select {
		case <-changed:
		case <-deadline.C:
			return false
		}
	}
}

func (r *recording) park() {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.parked:
	default:
		close(r.parked)
	}
}

// WaitParked espera o laço de gravação parar de vez depois de um Close.
func (r *recording) WaitParked(timeout time.Duration) {
	select {
	case <-r.parked:
	case <-time.After(timeout):
	}
}

// blockRecorder grava do microfone (e do áudio do computador) um bloco de
// cada vez; é o audio.Capture, ou um substituto nos testes.
type blockRecorder interface {
	RecordBlock(ctx context.Context, target time.Duration, outputPath string) ([]int16, error)
	DiscardPending()
}

// block é um trecho gravado esperando transcrição.
type block struct {
	sessionID string
	wav       string
	samples   []int16 // nil nos blocos recuperados, lidos do wav
	previous  []int16 // bloco anterior, contexto para identificar quem fala
	offset    float64 // onde o bloco começa no áudio da sessão, em segundos
	ended     bool    // marca o fim da gravação da sessão
}

// Quantas vezes a transcrição de um bloco é tentada e quanto se espera o
// whisper-server voltar entre as tentativas (ele sobe de novo sozinho se
// cair: transcriber.Supervise). Variáveis para os testes não esperarem.
var (
	transcribeAttempts = 3
	whisperWait        = 2 * time.Minute
	whisperPoll        = time.Second
)

// pipeline leva o áudio do microfone até as falas no banco.
type pipeline struct {
	store       *storage.Storage
	rec         *recording
	capture     blockRecorder
	tmpDir      string // áudio bruto da gravação em andamento e blocos na fila
	sessionsDir string // áudio das sessões

	// transcribe transcreve um bloco e identifica quem fala (transcript.Transcribe)
	transcribe func(sessionID, wavPath string, samples, previous []int16) ([]transcript.Utterance, error)
	// whisperReady diz se o whisper-server aceita transcrições agora
	whisperReady func() bool

	// yield, se definido, diz se alguém mais urgente precisa da GPU agora (o
	// Intérprete com uma frase): a transcrição do próximo bloco espera
	yield func() bool

	onTranscript func(*storage.Transcript) // uma fala nova foi gravada no banco
	onSessionEnd func(sessionID string)    // a gravação da sessão terminou e tudo dela foi transcrito
	onQueue      func(pending int)         // quantos blocos esperam transcrição

	blockDuration time.Duration
	queue         chan block
	pending       atomic.Int32
	// O Whisper não voltou a tempo para um bloco: os seguintes não esperam de
	// novo, só conferem se ele já está de volta. Só a fila mexe aqui.
	whisperDown bool
}

func newPipeline(store *storage.Storage, rec *recording, capture blockRecorder, tmpDir, sessionsDir string) *pipeline {
	return &pipeline{
		store:         store,
		rec:           rec,
		capture:       capture,
		tmpDir:        tmpDir,
		sessionsDir:   sessionsDir,
		whisperReady:  func() bool { return true },
		blockDuration: 30 * time.Second,
		// A transcrição roda numa fila separada para a gravação não parar entre
		// blocos. Antes, o áudio que chegava enquanto um bloco era transcrito se
		// perdia (2 a 4 segundos a cada 30).
		queue: make(chan block, 120),
	}
}

// enqueue põe o bloco na fila de transcrição sem nunca fazer a gravação
// esperar: com a fila cheia (a transcrição mais de uma hora atrasada), o bloco
// fica só no disco, e a transcrição dele é retomada na próxima vez que o app
// abrir. Esperar um lugar na fila pararia o laço de gravação, e o áudio que
// chegasse nesse meio tempo se perderia.
func (p *pipeline) enqueue(b block) {
	if b.ended {
		select {
		case p.queue <- b:
		default:
			// A marca de fim não pode se perder: entra quando houver lugar
			go func() { p.queue <- b }()
		}
		return
	}
	n := p.pending.Add(1)
	select {
	case p.queue <- b:
		if p.onQueue != nil {
			p.onQueue(int(n))
		}
	default:
		p.pending.Add(-1)
		log.Printf("Aviso: fila de transcrição cheia; o bloco em %.0fs da sessão %s fica guardado e será transcrito na próxima vez que o app abrir", b.offset, b.sessionID)
	}
}

// worker transcreve os blocos da fila, em ordem, até a fila ser fechada.
func (p *pipeline) worker() {
	// Último bloco transcrito, contexto para os blocos recuperados
	var last block
	for b := range p.queue {
		if b.ended {
			// Só depois de transcrever todos os blocos da sessão. Se ela foi
			// apagada nesse meio tempo, não há o que consolidar nem resumir
			if p.store.SessionExists(b.sessionID) {
				speakers.Consolidate(p.store, b.sessionID)
				if p.onSessionEnd != nil {
					p.onSessionEnd(b.sessionID)
				}
			}
			continue
		}
		p.waitTurn()
		if p.transcribeBlock(&b, last) {
			last = b
		}
		n := p.pending.Add(-1)
		if p.onQueue != nil {
			p.onQueue(int(n))
		}
	}
}

// Quanto a transcrição de um bloco espera a vez (yield): variáveis para os
// testes não esperarem.
var (
	yieldPoll = 100 * time.Millisecond
	yieldMax  = 15 * time.Second
)

// waitTurn segura a transcrição do próximo bloco enquanto o Intérprete está
// com uma frase: os dois usam a GPU, e um bloco de 30 s da gravação no meio
// atrasava a frase em meio segundo. A transcrição da gravação pode chegar
// alguns segundos depois; o inglês na reunião, não. Espera no máximo
// yieldMax, para a gravação não ficar para trás numa conversa longa.
func (p *pipeline) waitTurn() {
	if p.yield == nil {
		return
	}
	for deadline := time.Now().Add(yieldMax); p.yield() && time.Now().Before(deadline); {
		time.Sleep(yieldPoll)
	}
}

// transcribeBlock transcreve o bloco e grava as falas dele. Devolve se deu certo.
//
// Se o Whisper não responde (caiu, travou, ainda está carregando o modelo),
// espera ele voltar e tenta de novo. Se nem assim, o WAV do bloco fica na
// pasta temporária e a transcrição é retomada na próxima vez que o app abrir
// (audio.Recover). Antes, um bloco gravado ao vivo cuja transcrição falhava
// era apagado, e aquele trecho da reunião ficava sem transcrição.
func (p *pipeline) transcribeBlock(b *block, last block) bool {
	if !p.store.SessionExists(b.sessionID) {
		// A sessão foi apagada com blocos dela ainda na fila
		os.Remove(b.wav)
		return false
	}
	if b.samples == nil { // bloco recuperado de uma gravação interrompida
		samples, err := audio.LoadWav(b.wav)
		if err != nil {
			log.Printf("Erro ao ler o bloco recuperado %s: %v", b.wav, err)
			os.Remove(b.wav)
			return false
		}
		b.samples = samples
		if last.sessionID == b.sessionID && math.Abs(last.offset+float64(len(last.samples))/16000-b.offset) < 0.1 {
			b.previous = last.samples
		}
		// Se o app fechou no meio da transcrição deste bloco, parte das
		// falas dele pode já ter sido salva
		p.store.DeleteTranscriptsBetween(b.sessionID, b.offset, b.offset+float64(len(samples))/16000)
	}

	if p.whisperDown {
		// O Whisper já não voltou a tempo para um bloco anterior: sem ele de
		// volta, este fica guardado na hora, para a fila não atrasar minutos a
		// cada bloco enquanto a gravação segue enchendo ela
		if !p.whisperReady() {
			return false
		}
		p.whisperDown = false
		log.Println("-> O Whisper voltou; a transcrição dos blocos seguintes continua. Os que ficaram guardados serão transcritos na próxima vez que o app abrir.")
	}

	for attempt := 1; ; attempt++ {
		err := p.transcribeOnce(*b)
		if err == nil {
			os.Remove(b.wav)
			return true
		}
		if errors.Is(err, storage.ErrNoSession) {
			// Apagada durante a transcrição: tira o que a transcrição gravou dela
			// (as vozes) e segue
			p.store.DeleteSession(b.sessionID)
			os.Remove(b.wav)
			return false
		}
		// O Whisper respondeu e recusou este áudio: esperar não adianta
		var refused *transcriber.StatusError
		isRefusal := errors.As(err, &refused) && !refused.Loading()
		log.Printf("Erro na transcrição do bloco em %.0fs da sessão %s (tentativa %d de %d): %v", b.offset, b.sessionID, attempt, transcribeAttempts, err)
		if attempt >= transcribeAttempts {
			if isRefusal {
				log.Printf("O Whisper recusou o bloco em %.0fs da sessão %s; ele fica sem transcrição (o áudio continua na sessão)", b.offset, b.sessionID)
				os.Remove(b.wav)
			} else {
				p.whisperDown = true
				log.Printf("Whisper indisponível: a transcrição do bloco em %.0fs da sessão %s (e dos seguintes, enquanto ele não voltar) fica para a próxima vez que o app abrir", b.offset, b.sessionID)
			}
			return false
		}
		if !isRefusal {
			p.waitWhisper()
		}
		if !p.store.SessionExists(b.sessionID) {
			os.Remove(b.wav)
			return false
		}
	}
}

// transcribeOnce transcreve o bloco e grava as falas, com a sessão travada
// até o fim (ver speakers.Lock).
func (p *pipeline) transcribeOnce(b block) error {
	unlock := speakers.Lock(b.sessionID)
	defer unlock()
	utterances, err := p.transcribe(b.sessionID, b.wav, b.samples, b.previous)
	if err != nil {
		return err
	}
	for _, u := range utterances {
		// O texto das falas não vai para o log: ele fica no banco, e some de lá
		// quando a sessão é apagada
		log.Printf("🗣 Transcrito [%s]: %d caracteres", u.Speaker, len([]rune(u.Text)))
		t, err := p.store.InsertTranscript(b.sessionID, "Audio Geral", u.Speaker, u.Text, b.offset+u.Start)
		if errors.Is(err, storage.ErrNoSession) {
			return err
		}
		if err != nil {
			log.Printf("Erro ao gravar uma fala da sessão %s: %v", b.sessionID, err)
			continue
		}
		if p.onTranscript != nil {
			p.onTranscript(t)
		}
	}
	return nil
}

// waitWhisper espera o whisper-server aceitar transcrições, até whisperWait.
func (p *pipeline) waitWhisper() {
	for deadline := time.Now().Add(whisperWait); !p.whisperReady() && time.Now().Before(deadline); {
		time.Sleep(whisperPoll)
	}
}

// recoverInterrupted aproveita as gravações que o app não terminou de salvar
// porque fechou ou travou no meio: o áudio volta para a sessão e os blocos
// sem transcrição entram na fila. Precisa rodar antes de o app voltar a
// gravar. Se outra instância do app estiver aberta, os arquivos podem ser da
// gravação dela.
func (p *pipeline) recoverInterrupted() {
	if !audio.LockDir(p.tmpDir) {
		log.Println("Aviso: outra instância do app está gravando; gravações interrompidas serão recuperadas na próxima vez")
		return
	}
	interrupted := audio.Recover(p.tmpDir, p.sessionsDir, p.store.SessionExists, func(start time.Time) (string, error) {
		// A sessão da gravação foi apagada: a recuperada vai para o workspace atual
		s, err := p.store.CreateSession("", start.Format("02/01/2006")+" - Gravação recuperada")
		if err != nil {
			return "", err
		}
		return s.ID, nil
	})
	if len(interrupted) == 0 {
		return
	}
	go func() {
		// O whisper-server pode ainda estar carregando o modelo
		p.waitWhisper()
		ready := p.whisperReady()
		for _, rec := range interrupted {
			if len(rec.Blocks) > 0 && !ready {
				log.Printf("Whisper indisponível: a transcrição da gravação interrompida da sessão %s fica para a próxima vez", rec.SessionID)
				continue
			}
			log.Printf("Retomando a gravação interrompida da sessão %s: %d blocos para transcrever", rec.SessionID, len(rec.Blocks))
			for _, b := range rec.Blocks {
				p.enqueue(block{sessionID: rec.SessionID, wav: b.Path, offset: b.Offset})
			}
			p.enqueue(block{sessionID: rec.SessionID, ended: true})
		}
	}()
}

// recordLoop grava enquanto a tela pedir, um bloco de cada vez, e põe cada
// bloco na fila de transcrição. Só retorna quando o app fecha.
func (p *pipeline) recordLoop() {
	// O trecho sendo gravado: de qual sessão, onde fica o áudio bruto dele,
	// onde ele começa dentro do áudio da sessão (o que já estava salvo de
	// gravações anteriores) e quanto já foi gravado, para cada fala guardar a
	// sua posição no áudio
	var segment struct {
		sessionID string
		rawFile   string
		start     float64
		samples   int
	}
	// Bloco anterior da mesma gravação, contexto para identificar quem fala
	var previousBlock []int16

	closeSegment := func() {
		if segment.sessionID == "" {
			return
		}
		id := segment.sessionID
		if p.saveSegment(id, segment.rawFile) {
			p.enqueue(block{sessionID: id, ended: true})
		}
		segment.sessionID, segment.rawFile = "", ""
		p.rec.setSegment("")
	}

	for {
		wanted, sessionID, closing, changed := p.rec.state()
		if closing {
			// O bloco anterior já foi salvo: não começa outro (nem religa o
			// microfone) enquanto o app fecha. Juntar ao áudio da sessão,
			// transcrever o que faltou e fazer o resumo fica para a próxima vez
			// que o app abrir (recoverInterrupted), sem segurar o fechamento
			p.rec.park()
			return
		}
		if !wanted {
			previousBlock = nil
			closeSegment()
			<-changed
			continue
		}

		if segment.sessionID != "" && segment.sessionID != sessionID {
			previousBlock = nil
			p.capture.DiscardPending()
			closeSegment()
		}
		if segment.sessionID == "" {
			segment.sessionID = sessionID
			segment.rawFile = audio.RawFile(p.tmpDir, sessionID)
			segment.start = audio.SessionDuration(p.sessionsDir, sessionID)
			segment.samples = 0
			// Áudio de uma gravação interrompida que não foi recuperado: ele entra
			// no áudio da sessão antes do que for gravado agora
			if info, err := os.Stat(segment.rawFile); err == nil {
				segment.samples = int(info.Size() / 2)
			}
			p.rec.setSegment(sessionID)
		}

		ctx, cancel := context.WithCancel(context.Background())
		if !p.rec.beginBlock(sessionID, cancel) {
			cancel()
			continue
		}

		// O nome guarda a sessão e a posição do bloco, para a transcrição ser
		// retomada se o app fechar antes (audio.Recover)
		offset := segment.start + float64(segment.samples)/16000
		tmpWav := audio.BlockFile(p.tmpDir, sessionID, offset)

		samples, err := p.capture.RecordBlock(ctx, p.blockDuration, tmpWav)
		p.rec.endBlock()
		cancel()
		canceled := errors.Is(err, context.Canceled)

		if len(samples) > 0 {
			if err := audio.AppendRaw(segment.rawFile, samples); err != nil {
				log.Printf("Erro ao salvar o áudio gravado: %v", err)
			}
			if canceled {
				// Interrompido antes do fim: o bloco ainda não foi gravado em WAV
				if err := audio.SaveWav(tmpWav, samples, 16000); err != nil {
					log.Printf("Erro ao salvar o bloco interrompido: %v", err)
				}
			}
			segment.samples += len(samples)
			p.enqueue(block{sessionID: sessionID, wav: tmpWav, samples: samples, previous: previousBlock, offset: offset})
			previousBlock = samples
		}

		switch {
		case canceled:
			log.Println("Gravação interrompida.")
		case err != nil:
			log.Printf("Erro na gravação: %v", err)
			// Não insiste sem parar num microfone com problema; um pedido novo
			// da tela (parar) interrompe a espera
			select {
			case <-changed:
			case <-time.After(time.Second):
			}
		}
	}
}

// saveSegment junta o áudio bruto do trecho gravado ao áudio da sessão.
// Devolve false se a sessão foi apagada (o áudio é descartado).
func (p *pipeline) saveSegment(sessionID, rawFile string) bool {
	if !p.store.SessionExists(sessionID) {
		os.Remove(rawFile)
		return false
	}
	info, err := os.Stat(rawFile)
	if err != nil || info.Size() == 0 {
		os.Remove(rawFile)
		return true
	}
	log.Printf("Salvando o áudio gravado da sessão %s (%.0fs)", sessionID, float64(info.Size())/2/16000)
	if _, err := audio.AppendSessionRaw(p.sessionsDir, sessionID, rawFile); err != nil {
		// O áudio bruto fica: entra na sessão na próxima vez que ela for
		// gravada ou que o app abrir. Antes ele era apagado mesmo com erro
		log.Printf("Erro ao salvar o áudio da sessão %s (o áudio bruto fica guardado em %s): %v", sessionID, filepath.Base(rawFile), err)
		return true
	}
	os.Remove(rawFile)
	return true
}
