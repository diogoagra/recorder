package backend

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"recorder/internal/audio"
	"recorder/internal/datadir"
	"recorder/internal/storage"
	"recorder/internal/transcriber"
	"recorder/internal/transcript"
)

// fakeMic grava blocos de meio segundo de "áudio" sem microfone: cada bloco
// leva blockTime para ficar pronto, ou termina antes se for cancelado, com o
// que já tinha gravado (como o audio.Capture).
type fakeMic struct {
	blockTime time.Duration

	mu        sync.Mutex
	blocks    int       // blocos pedidos
	firstCall time.Time // quando o primeiro bloco foi pedido
	discards  int
}

func (m *fakeMic) RecordBlock(ctx context.Context, target time.Duration, outputPath string) ([]int16, error) {
	m.mu.Lock()
	m.blocks++
	if m.firstCall.IsZero() {
		m.firstCall = time.Now()
	}
	m.mu.Unlock()

	samples := make([]int16, 8000)
	for i := range samples {
		samples[i] = int16(i % 500)
	}
	select {
	case <-ctx.Done():
		return samples[:4000], ctx.Err()
	case <-time.After(m.blockTime):
		return samples, audio.SaveWav(outputPath, samples, 16000)
	}
}

func (m *fakeMic) DiscardPending() {
	m.mu.Lock()
	m.discards++
	m.mu.Unlock()
}

func (m *fakeMic) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.blocks
}

// testPipeline monta a gravação e a transcrição de um App de teste, com o
// microfone simulado e uma transcrição que devolve uma fala por bloco.
func testPipeline(t *testing.T, a *App, mic *fakeMic) *pipeline {
	t.Helper()
	if _, err := exec.LookPath("afconvert"); err != nil && runtime.GOOS != "windows" {
		t.Skip("afconvert indisponível")
	}
	oldAttempts, oldWait, oldPoll := transcribeAttempts, whisperWait, whisperPoll
	whisperWait, whisperPoll = 200*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { transcribeAttempts, whisperWait, whisperPoll = oldAttempts, oldWait, oldPoll })

	p := newPipeline(a.store, a.recorder(), mic, datadir.TmpAudio(), datadir.SessionsAudio())
	os.MkdirAll(p.tmpDir, 0755)
	os.MkdirAll(p.sessionsDir, 0755)
	p.transcribe = func(sessionID, wavPath string, samples, previous []int16) ([]transcript.Utterance, error) {
		return []transcript.Utterance{{Speaker: "Pessoa 1", Text: "fala do bloco"}}, nil
	}
	return p
}

// startPipeline liga a fila de transcrição (se transcribing) e o laço de
// gravação, e para os dois no fim do teste, antes de a pasta temporária sumir.
func startPipeline(t *testing.T, p *pipeline, transcribing bool) {
	t.Helper()
	if transcribing {
		go p.worker()
	}
	go p.recordLoop()
	t.Cleanup(func() {
		p.rec.Close()
		p.rec.WaitParked(5 * time.Second)
		close(p.queue)
	})
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("não aconteceu a tempo: %s", what)
}

func TestRecordingRefusesBlockAfterStop(t *testing.T) {
	r := newRecording()
	r.Start("s1", 600)
	if rec, id, _ := r.Status(); !rec || id != "s1" {
		t.Fatalf("Status = %v, %q", rec, id)
	}
	if off := r.Offset("s1"); off < 600 || off > 601 {
		t.Fatalf("Offset = %v, esperava ~600", off)
	}
	if off := r.Offset("outra"); off >= 0 {
		t.Fatalf("Offset de outra sessão = %v", off)
	}

	// O "parar" chega entre o laço consultar o estado e começar o bloco: o
	// bloco não pode começar (antes, gravava mais 30s sem ninguém para cancelar)
	wanted, sessionID, _, _ := r.state()
	r.Stop()
	canceled := false
	if wanted && r.beginBlock(sessionID, func() { canceled = true }) {
		t.Fatal("um bloco começou depois do pedido de parar")
	}
	if canceled {
		t.Fatal("o cancelamento de um bloco recusado não deveria ser chamado")
	}

	// Parar no meio de um bloco cancela o bloco
	r.Start("s1", 0)
	if !r.beginBlock("s1", func() { canceled = true }) {
		t.Fatal("o bloco deveria começar")
	}
	r.Stop()
	if !canceled {
		t.Fatal("parar deveria cancelar o bloco em andamento")
	}
	if rec, id, _ := r.Status(); rec || id != "" {
		t.Fatalf("depois de parar: Status = %v, %q", rec, id)
	}

	// Trocar de sessão sem parar também cancela o bloco da anterior
	r.Start("s1", 0)
	canceled = false
	r.beginBlock("s1", func() { canceled = true })
	r.Start("s2", 0)
	if !canceled {
		t.Fatal("trocar de sessão deveria cancelar o bloco da sessão anterior")
	}

	// Fechando o app: nada começa
	if !r.Close() {
		t.Fatal("Close deveria dizer que estava gravando")
	}
	if r.beginBlock("s2", func() {}) || r.Start("s3", 0) {
		t.Fatal("nada deveria começar com o app fechando")
	}
}

func TestWaitSegmentClosed(t *testing.T) {
	r := newRecording()
	if !r.WaitSegmentClosed("s1", time.Second) {
		t.Fatal("sem trecho aberto, não há o que esperar")
	}
	r.setSegment("s1")
	if r.WaitSegmentClosed("s1", 20*time.Millisecond) {
		t.Fatal("o trecho continua aberto: deveria esgotar o tempo")
	}
	if !r.WaitSegmentClosed("outra", 20*time.Millisecond) {
		t.Fatal("o trecho aberto é de outra sessão")
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		r.setSegment("")
	}()
	if !r.WaitSegmentClosed("s1", 5*time.Second) {
		t.Fatal("deveria voltar quando o trecho fecha")
	}
}

func TestRecordingSavesAudioAndTranscribesEveryBlock(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")
	mic := &fakeMic{blockTime: 30 * time.Millisecond}
	p := testPipeline(t, a, mic)
	var ended atomic.Int32
	p.onSessionEnd = func(id string) {
		if id == sess.ID {
			ended.Add(1)
		}
	}
	startPipeline(t, p, true)

	// O laço acorda com o pedido, sem esperar a próxima conferência
	time.Sleep(50 * time.Millisecond)
	asked := time.Now()
	if _, err := a.StartRecording(sess.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "o primeiro bloco começar", func() bool { return mic.count() > 0 })
	mic.mu.Lock()
	delay := mic.firstCall.Sub(asked)
	mic.mu.Unlock()
	if delay > 300*time.Millisecond {
		t.Fatalf("a gravação começou %v depois do pedido (antes, até 1s)", delay)
	}

	eventually(t, "três blocos gravados", func() bool { return mic.count() >= 4 })
	a.StopRecording()
	eventually(t, "o fim da sessão depois de tudo transcrito", func() bool { return ended.Load() == 1 })

	transcripts, _ := store.GetAllTranscripts(sess.ID)
	if len(transcripts) < 4 {
		t.Fatalf("%d falas, esperava uma por bloco gravado (pelo menos 4)", len(transcripts))
	}
	// Cada bloco começa onde o anterior terminou: meio segundo cada, o último
	// (interrompido) com um quarto
	for i, tr := range transcripts {
		if want := 0.5 * float64(i); tr.Offset != want {
			t.Fatalf("fala %d em %.2fs, esperava %.2fs", i, tr.Offset, want)
		}
	}
	want := 0.5*float64(len(transcripts)-1) + 0.25
	_, duration, err := audio.Info(audio.SessionFile(p.sessionsDir, sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	if got := duration.Seconds(); got < want || got > want+0.3 {
		t.Fatalf("áudio da sessão com %.2fs, esperava ~%.2fs", got, want)
	}
	if left, _ := filepath.Glob(filepath.Join(p.tmpDir, "*")); len(left) > 1 { // só a trava da pasta
		t.Fatalf("sobrou na pasta temporária: %v", left)
	}
	if rec, _, _ := a.recorder().Status(); rec {
		t.Fatal("deveria ter parado")
	}
}

// Apagar a reunião que está sendo gravada: a gravação para e nada dela volta
// (antes, o último bloco recriava o áudio e as falas da sessão apagada).
func TestDeleteSessionWhileRecordingLeavesNothingBehind(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")
	mic := &fakeMic{blockTime: 30 * time.Millisecond}
	p := testPipeline(t, a, mic)
	var ended atomic.Int32
	p.onSessionEnd = func(string) { ended.Add(1) }
	// A transcrição demora: a sessão é apagada com blocos na fila e um em andamento
	p.transcribe = func(sessionID, wavPath string, samples, previous []int16) ([]transcript.Utterance, error) {
		time.Sleep(40 * time.Millisecond)
		store.SaveSpeakerVoices(sessionID, []storage.SpeakerVoice{{Label: "Pessoa 1", Embedding: []float32{1}, Seconds: 1}})
		return []transcript.Utterance{{Speaker: "Pessoa 1", Text: "fala do bloco"}}, nil
	}
	startPipeline(t, p, true)

	a.StartRecording(sess.ID)
	eventually(t, "alguns blocos gravados", func() bool { return mic.count() >= 3 })
	if err := a.DeleteSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	if status := a.GetStatus(); status["recording"] != false {
		t.Fatalf("a gravação deveria ter parado: %+v", status)
	}
	eventually(t, "a fila de transcrição esvaziar", func() bool { return p.pending.Load() == 0 })
	time.Sleep(100 * time.Millisecond)

	if parts := a.GetSessionAudioParts(sess.ID); len(parts) != 0 {
		t.Fatalf("sobrou áudio da sessão apagada: %v", parts)
	}
	if all, _ := store.GetAllTranscripts(sess.ID); len(all) != 0 {
		t.Fatalf("sobraram %d falas da sessão apagada", len(all))
	}
	if voices, _ := store.GetSpeakerVoices(sess.ID); len(voices) != 0 {
		t.Fatalf("sobraram vozes da sessão apagada: %+v", voices)
	}
	if left := append(audio.SessionBlocks(p.tmpDir, sess.ID), existing(audio.RawFile(p.tmpDir, sess.ID))...); len(left) != 0 {
		t.Fatalf("sobrou na pasta temporária: %v", left)
	}
	if ended.Load() != 0 {
		t.Fatal("o resumo de uma sessão apagada não deveria ser pedido")
	}
}

func existing(path string) []string {
	if _, err := os.Stat(path); err == nil {
		return []string{path}
	}
	return nil
}

// O Whisper cai no meio da reunião e volta (o app o sobe de novo): o bloco
// espera e é transcrito, em vez de ser descartado.
func TestBlockWaitsForWhisperToComeBack(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")
	p := testPipeline(t, a, &fakeMic{})
	var calls atomic.Int32
	var ready atomic.Bool
	p.whisperReady = ready.Load
	p.transcribe = func(sessionID, wavPath string, samples, previous []int16) ([]transcript.Utterance, error) {
		if calls.Add(1) == 1 {
			// Volta daqui a pouco
			time.AfterFunc(30*time.Millisecond, func() { ready.Store(true) })
			return nil, errors.New("falha ao falar com o whisper-server: connection refused")
		}
		return []transcript.Utterance{{Speaker: "Pessoa 1", Text: "agora foi"}}, nil
	}
	wav := audio.BlockFile(p.tmpDir, sess.ID, 30)
	audio.SaveWav(wav, make([]int16, 8000), 16000)

	b := block{sessionID: sess.ID, wav: wav, samples: make([]int16, 8000), offset: 30}
	if !p.transcribeBlock(&b, block{}) {
		t.Fatal("o bloco deveria ter sido transcrito na segunda tentativa")
	}
	all, _ := store.GetAllTranscripts(sess.ID)
	if len(all) != 1 || all[0].Text != "agora foi" || all[0].Offset != 30 {
		t.Fatalf("falas: %+v", all)
	}
	if exists(wav) {
		t.Fatal("o WAV do bloco transcrito deveria ter sido apagado")
	}
}

// O Whisper não volta: o WAV do bloco fica para a próxima vez que o app
// abrir, que retoma a transcrição (antes, era apagado e o trecho se perdia).
func TestBlockIsKeptWhenWhisperStaysDown(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")
	p := testPipeline(t, a, &fakeMic{})
	whisperWait = 20 * time.Millisecond
	var calls atomic.Int32
	p.whisperReady = func() bool { return false }
	p.transcribe = func(sessionID, wavPath string, samples, previous []int16) ([]transcript.Utterance, error) {
		calls.Add(1)
		return nil, errors.New("falha ao falar com o whisper-server: connection refused")
	}
	wav := audio.BlockFile(p.tmpDir, sess.ID, 60)
	audio.SaveWav(wav, make([]int16, 8000), 16000)

	b := block{sessionID: sess.ID, wav: wav, samples: make([]int16, 8000), offset: 60}
	if p.transcribeBlock(&b, block{}) {
		t.Fatal("a transcrição não deveria ter dado certo")
	}
	if int(calls.Load()) != transcribeAttempts {
		t.Fatalf("%d tentativas, esperava %d", calls.Load(), transcribeAttempts)
	}
	if !exists(wav) {
		t.Fatal("o WAV do bloco deveria ficar para ser transcrito na próxima abertura do app")
	}
	// É o que a recuperação encontra ao abrir o app
	found := audio.Recover(p.tmpDir, p.sessionsDir, store.SessionExists, nil)
	if len(found) != 1 || found[0].SessionID != sess.ID || len(found[0].Blocks) != 1 || found[0].Blocks[0].Offset != 60 {
		t.Fatalf("a recuperação deveria achar o bloco em 60s: %+v", found)
	}
}

// O Whisper está de pé e recusa aquele áudio: esperar não adianta, e o bloco
// não pode ficar sendo tentado a cada abertura do app.
func TestBlockRefusedByWhisperIsDropped(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")
	p := testPipeline(t, a, &fakeMic{})
	p.whisperReady = func() bool {
		t.Error("não deveria esperar o Whisper, que respondeu")
		return true
	}
	p.transcribe = func(sessionID, wavPath string, samples, previous []int16) ([]transcript.Utterance, error) {
		return nil, &transcriber.StatusError{Code: 400, Body: "invalid audio"}
	}
	wav := audio.BlockFile(p.tmpDir, sess.ID, 0)
	audio.SaveWav(wav, make([]int16, 8000), 16000)
	b := block{sessionID: sess.ID, wav: wav, samples: make([]int16, 8000)}
	if p.transcribeBlock(&b, block{}) || exists(wav) {
		t.Fatal("o bloco recusado deveria ser descartado")
	}
}

// Fechar o app gravando: o bloco atual vai para o disco e o laço para sem
// começar outro; o áudio fica para ser juntado na próxima abertura.
func TestClosingTheAppKeepsTheRecordingForRecovery(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")
	mic := &fakeMic{blockTime: time.Hour} // um bloco que só termina cancelado
	p := testPipeline(t, a, mic)
	// A fila fica parada, como num fechamento no meio da transcrição
	startPipeline(t, p, false)

	a.StartRecording(sess.ID)
	eventually(t, "o bloco começar", func() bool { return mic.count() == 1 })
	if !a.recorder().Close() {
		t.Fatal("Close deveria dizer que estava gravando")
	}
	a.recorder().WaitParked(5 * time.Second)
	select {
	case <-a.recorder().parked:
	default:
		t.Fatal("o laço de gravação não parou")
	}
	if mic.count() != 1 {
		t.Fatalf("um bloco novo começou com o app fechando (%d)", mic.count())
	}
	if !exists(audio.RawFile(p.tmpDir, sess.ID)) || len(audio.SessionBlocks(p.tmpDir, sess.ID)) != 1 {
		t.Fatal("o áudio bruto e o bloco interrompido deveriam ficar para a próxima abertura")
	}

	// Na próxima abertura: o áudio volta para a sessão e o bloco para a fila
	found := audio.Recover(p.tmpDir, p.sessionsDir, store.SessionExists, nil)
	if len(found) != 1 || len(found[0].Blocks) != 1 {
		t.Fatalf("recuperação: %+v", found)
	}
	if d := audio.SessionDuration(p.sessionsDir, sess.ID); d < 0.2 || d > 0.6 {
		t.Fatalf("áudio recuperado com %.2fs, esperava ~0,25s", d)
	}
}

func TestStartRecordingNeedsAnExistingSession(t *testing.T) {
	a, _ := testApp(t)
	if _, err := a.StartRecording("nao-existe"); err == nil {
		t.Fatal("gravar numa reunião que não existe deveria dar erro")
	}
	if rec, _, _ := a.recorder().Status(); rec {
		t.Fatal("nada deveria estar gravando")
	}
}

// Reprocessar apaga a transcrição atual antes de fazer a nova: sem o Whisper
// pronto, ela (e os nomes dados às vozes) tem que continuar lá.
func TestReprocessKeepsTranscriptWhenWhisperIsNotReady(t *testing.T) {
	loading := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "whisper.cpp")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer loading.Close()

	for name, endpoint := range map[string]string{"carregando o modelo": loading.URL, "parado": "http://127.0.0.1:1"} {
		a, store := testApp(t)
		a.whisperEndpoint = endpoint
		sess, _ := store.CreateSession("", "Reunião")
		store.InsertTranscript(sess.ID, "Audio Geral", "Ana", "o que já estava transcrito", 0)
		store.SaveSpeakerVoices(sess.ID, []storage.SpeakerVoice{{Label: "Ana", Embedding: []float32{1}, Seconds: 30}})
		os.MkdirAll(datadir.SessionsAudio(), 0755)
		os.WriteFile(audio.SessionFile(datadir.SessionsAudio(), sess.ID), []byte("áudio"), 0644)

		if err := a.ReprocessSession(sess.ID); err == nil {
			t.Fatalf("Whisper %s: reprocessar deveria ser recusado", name)
		}
		if all, _ := store.GetAllTranscripts(sess.ID); len(all) != 1 || all[0].Speaker != "Ana" {
			t.Fatalf("Whisper %s: a transcrição atual foi apagada: %+v", name, all)
		}
		if voices, _ := store.GetSpeakerVoices(sess.ID); len(voices) != 1 {
			t.Fatalf("Whisper %s: as vozes da sessão foram apagadas", name)
		}
	}
}

func TestReprocessRefusedWhileRecordingTheSession(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")
	os.MkdirAll(datadir.SessionsAudio(), 0755)
	os.WriteFile(audio.SessionFile(datadir.SessionsAudio(), sess.ID), []byte("áudio"), 0644)
	a.recorder().Start(sess.ID, 0)
	if err := a.ReprocessSession(sess.ID); err == nil {
		t.Fatal("reprocessar a reunião que está sendo gravada deveria ser recusado")
	}
}

// Depois que o Whisper não voltou para um bloco, os seguintes não esperam de
// novo: ficam guardados na hora. Sem isso a fila atrasava minutos por bloco
// enquanto a gravação seguia enchendo ela.
func TestBlocksDoNotWaitAgainWhileWhisperIsDown(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")
	p := testPipeline(t, a, &fakeMic{})
	whisperWait = 20 * time.Millisecond
	var calls atomic.Int32
	var ready atomic.Bool
	p.whisperReady = ready.Load
	p.transcribe = func(sessionID, wavPath string, samples, previous []int16) ([]transcript.Utterance, error) {
		calls.Add(1)
		if !ready.Load() {
			return nil, errors.New("connection refused")
		}
		return []transcript.Utterance{{Text: "voltou"}}, nil
	}
	newBlock := func(offset float64) block {
		wav := audio.BlockFile(p.tmpDir, sess.ID, offset)
		audio.SaveWav(wav, make([]int16, 8000), 16000)
		return block{sessionID: sess.ID, wav: wav, samples: make([]int16, 8000), offset: offset}
	}

	first := newBlock(0)
	p.transcribeBlock(&first, block{})
	if int(calls.Load()) != transcribeAttempts {
		t.Fatalf("primeiro bloco: %d tentativas, esperava %d", calls.Load(), transcribeAttempts)
	}
	second := newBlock(30)
	began := time.Now()
	if p.transcribeBlock(&second, block{}) || int(calls.Load()) != transcribeAttempts {
		t.Fatal("com o Whisper ainda fora, o segundo bloco não deveria nem ser tentado")
	}
	if waited := time.Since(began); waited > 15*time.Millisecond {
		t.Fatalf("o segundo bloco esperou %v pelo Whisper de novo", waited)
	}
	if !exists(first.wav) || !exists(second.wav) {
		t.Fatal("os dois blocos deveriam ficar guardados")
	}

	ready.Store(true)
	third := newBlock(60)
	if !p.transcribeBlock(&third, block{}) {
		t.Fatal("com o Whisper de volta, o bloco seguinte deveria ser transcrito")
	}
	if all, _ := store.GetAllTranscripts(sess.ID); len(all) != 1 || all[0].Offset != 60 {
		t.Fatalf("falas: %+v", all)
	}
}

// Com a fila de transcrição cheia, a gravação não espera: o bloco fica no disco.
func TestFullQueueNeverBlocksTheRecording(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")
	p := testPipeline(t, a, &fakeMic{})
	// Ninguém transcreve: a fila enche
	for i := range cap(p.queue) {
		p.enqueue(block{sessionID: sess.ID, offset: float64(i)})
	}
	if int(p.pending.Load()) != cap(p.queue) {
		t.Fatalf("pendentes: %d", p.pending.Load())
	}
	done := make(chan struct{})
	go func() {
		p.enqueue(block{sessionID: sess.ID, offset: 9999})
		p.enqueue(block{sessionID: sess.ID, ended: true})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a gravação ficou esperando um lugar na fila de transcrição")
	}
	if int(p.pending.Load()) != cap(p.queue) {
		t.Fatalf("o bloco que não coube não conta como pendente: %d", p.pending.Load())
	}
	// A marca de fim entra assim que houver lugar
	for range cap(p.queue) {
		<-p.queue
	}
	select {
	case b := <-p.queue:
		if !b.ended {
			t.Fatalf("esperava a marca de fim da sessão, veio %+v", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a marca de fim da sessão se perdeu")
	}
}

// speechLike gera seconds de "fala": tons com uma pausa a cada 10s, onde os
// blocos podem ser cortados.
func speechLike(seconds int) []int16 {
	samples := make([]int16, seconds*16000)
	for i := range samples {
		if i%(10*16000) > 9*16000 {
			continue // pausa
		}
		samples[i] = int16(3000 * float64(i%100-50) / 50)
	}
	return samples
}

// O Whisper sai do ar no meio de um reprocessamento e não volta: a sessão
// fica com a transcrição e as vozes que tinha antes, em vez de pela metade.
func TestReprocessRestoresTranscriptWhenWhisperGoesAway(t *testing.T) {
	if _, err := exec.LookPath("afconvert"); err != nil && runtime.GOOS != "windows" {
		t.Skip("afconvert indisponível")
	}
	oldWait, oldPoll := whisperWait, whisperPoll
	whisperWait, whisperPoll = 20*time.Millisecond, 5*time.Millisecond
	defer func() { whisperWait, whisperPoll = oldWait, oldPoll }()

	// Pronto ao começar; depois do primeiro bloco, derruba a conexão e para de
	// dizer que está pronto
	var inferences atomic.Int32
	whisper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "whisper.cpp")
		down := inferences.Load() >= 1
		switch {
		case r.URL.Path == "/health" && down:
			w.WriteHeader(http.StatusServiceUnavailable)
		case r.URL.Path == "/health":
			w.WriteHeader(http.StatusOK)
		case down:
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		default:
			inferences.Add(1)
			w.Write([]byte(`{"text":"transcrição nova, primeiro bloco","segments":[{"start":0,"end":2,"text":"transcrição nova, primeiro bloco."}]}`))
		}
	}))
	defer whisper.Close()

	a, store := testApp(t)
	a.whisperEndpoint = whisper.URL
	// O que a tela mostra enquanto espera
	var shown []string
	a.events = func(event string, data ...interface{}) {
		if event == "ollama-status" {
			shown = append(shown, data[0].(map[string]interface{})["message"].(string))
		}
	}
	sess, _ := store.CreateSession("", "Reunião")
	if _, err := audio.AppendSession(datadir.SessionsAudio(), sess.ID, speechLike(80)); err != nil {
		t.Fatal(err)
	}
	store.InsertTranscript(sess.ID, "Audio Geral", "Ana", "primeira fala de antes", 3)
	store.InsertTranscript(sess.ID, "Audio Geral", "Bruno", "segunda fala de antes", 42)
	store.SaveSpeakerVoices(sess.ID, []storage.SpeakerVoice{{Label: "Ana", Embedding: []float32{1, 2}, Seconds: 30}})
	store.ReplaceSpeakerSuggestions(sess.ID, []storage.SpeakerSuggestion{{Speaker: "Pessoa 3", Name: "Carla", Evidence: "oi, Carla"}})
	before, _ := store.GetAllTranscripts(sess.ID)

	err := a.ReprocessSession(sess.ID)
	if !errors.Is(err, errWhisperGone) {
		t.Fatalf("ReprocessSession: %v, esperava o aviso de que o Whisper parou no meio", err)
	}
	if inferences.Load() != 1 {
		t.Fatalf("o teste esperava um bloco transcrito antes da queda, foram %d", inferences.Load())
	}
	after, _ := store.GetAllTranscripts(sess.ID)
	if len(after) != len(before) {
		t.Fatalf("a sessão ficou com %d falas, esperava as %d de antes: %+v", len(after), len(before), after)
	}
	for i := range before {
		if after[i].Text != before[i].Text || after[i].Speaker != before[i].Speaker || after[i].Offset != before[i].Offset ||
			after[i].Source != before[i].Source || !after[i].Timestamp.Equal(before[i].Timestamp) {
			t.Fatalf("fala %d: %+v, esperava %+v", i, after[i], before[i])
		}
	}
	if voices, _ := store.GetSpeakerVoices(sess.ID); len(voices) != 1 || voices[0].Label != "Ana" || len(voices[0].Embedding) != 2 {
		t.Fatalf("as vozes de antes não voltaram: %+v", voices)
	}
	if suggestions, _ := store.GetSpeakerSuggestions(sess.ID); len(suggestions) != 1 || suggestions[0].Name != "Carla" {
		t.Fatalf("as sugestões de nomes de antes não voltaram: %+v", suggestions)
	}
	if working, _, _ := a.currentStatus(); working {
		t.Fatal("o aviso de trabalho deveria ter sumido")
	}
	waiting := false
	for _, message := range shown {
		waiting = waiting || strings.Contains(message, "esperando o Whisper voltar")
	}
	if !waiting {
		t.Fatalf("a tela deveria dizer que está esperando o Whisper voltar; mostrou %q", shown)
	}
}

// Com o Intérprete no meio de uma frase, a transcrição do bloco da gravação
// espera a vez na GPU; mas não para sempre.
func TestBlockTranscriptionYieldsToTheInterpreter(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")
	p := testPipeline(t, a, &fakeMic{})
	oldPoll, oldMax := yieldPoll, yieldMax
	yieldPoll, yieldMax = 2*time.Millisecond, 150*time.Millisecond
	defer func() { yieldPoll, yieldMax = oldPoll, oldMax }()

	var busy atomic.Bool
	busy.Store(true)
	p.yield = busy.Load
	var transcribed atomic.Int32
	p.transcribe = func(sessionID, wavPath string, samples, previous []int16) ([]transcript.Utterance, error) {
		transcribed.Add(1)
		return []transcript.Utterance{{Text: "fala"}}, nil
	}
	go p.worker()
	defer close(p.queue)
	newBlock := func(offset float64) block {
		wav := audio.BlockFile(p.tmpDir, sess.ID, offset)
		audio.SaveWav(wav, make([]int16, 8000), 16000)
		return block{sessionID: sess.ID, wav: wav, samples: make([]int16, 8000), offset: offset}
	}

	// O Intérprete termina a frase: o bloco é transcrito logo depois
	p.enqueue(newBlock(0))
	time.Sleep(40 * time.Millisecond)
	if transcribed.Load() != 0 {
		t.Fatal("com o Intérprete no meio de uma frase, o bloco deveria esperar")
	}
	busy.Store(false)
	eventually(t, "o bloco ser transcrito quando o Intérprete libera", func() bool { return transcribed.Load() == 1 })

	// O Intérprete não larga: o bloco espera só até o limite
	busy.Store(true)
	began := time.Now()
	p.enqueue(newBlock(30))
	eventually(t, "o bloco ser transcrito depois do limite de espera", func() bool { return transcribed.Load() == 2 })
	if waited := time.Since(began); waited < 140*time.Millisecond || waited > 2*time.Second {
		t.Fatalf("o bloco esperou %v, o limite era %v", waited, yieldMax)
	}
}
