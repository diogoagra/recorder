package interpreter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// timedWhisper responde como o whisper-server e anota quando cada pedido
// começou e terminou. Como o de verdade, atende um pedido de cada vez.
type timedWhisper struct {
	serial sync.Mutex // um pedido de cada vez

	mu    sync.Mutex
	pt    []string // transcrição de cada pedido de português, em ordem
	lang  []string // idioma de cada pedido de detecção, em ordem (o último vale para os seguintes)
	delay map[string]time.Duration
	calls []whisperCall
}

type whisperCall struct {
	kind       string // "pt", "traduz" ou "idioma"
	start, end time.Time
}

func (w *timedWhisper) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mr, _ := r.MultipartReader()
		fields := map[string]string{}
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() != "file" {
				b, _ := io.ReadAll(p)
				fields[p.FormName()] = string(b)
			} else {
				io.Copy(io.Discard, p)
			}
		}
		kind := "pt"
		switch {
		case fields["detect_language"] == "true":
			kind = "idioma"
		case fields["translate"] == "true":
			kind = "traduz"
		}
		w.serial.Lock()
		defer w.serial.Unlock()
		w.mu.Lock()
		n := 0
		for _, c := range w.calls {
			if c.kind == kind {
				n++
			}
		}
		call := len(w.calls)
		w.calls = append(w.calls, whisperCall{kind: kind, start: time.Now()})
		delay := w.delay[kind]
		w.mu.Unlock()
		time.Sleep(delay)
		w.mu.Lock()
		w.calls[call].end = time.Now()
		pt := heardPT
		if len(w.pt) > 0 {
			pt = w.pt[min(n, len(w.pt)-1)]
		}
		lang := "portuguese"
		if len(w.lang) > 0 {
			lang = w.lang[min(n, len(w.lang)-1)]
		}
		w.mu.Unlock()
		switch kind {
		case "idioma":
			if fields["response_format"] == "verbose_json" {
				json.NewEncoder(rw).Encode(map[string]any{"language": lang, "language_probabilities": map[string]float64{"en": 0.93}})
				return
			}
			json.NewEncoder(rw).Encode(map[string]string{"language": lang})
		case "traduz":
			json.NewEncoder(rw).Encode(map[string]string{"text": "EN de: " + pt})
		default:
			json.NewEncoder(rw).Encode(map[string]string{"text": pt})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (w *timedWhisper) count(kind string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, c := range w.calls {
		if c.kind == kind {
			n++
		}
	}
	return n
}

func (w *timedWhisper) call(kind string, n int) whisperCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range w.calls {
		if c.kind == kind {
			if n == 0 {
				return c
			}
			n--
		}
	}
	return whisperCall{}
}

// timedCorrector devolve a tradução literal como veio, depois de delay, e
// anota quando foi chamado.
type timedCorrector struct {
	delay time.Duration
	mu    sync.Mutex
	at    []time.Time
	got   []string
}

func (c *timedCorrector) Name() string  { return "Claude Haiku" }
func (c *timedCorrector) Warmup() error { return nil }
func (c *timedCorrector) Close()        {}
func (c *timedCorrector) Correct(ctx context.Context, pt, en string) (string, error) {
	c.mu.Lock()
	c.at = append(c.at, time.Now())
	c.got = append(c.got, pt)
	c.mu.Unlock()
	select {
	case <-time.After(c.delay):
		return en, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (c *timedCorrector) calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.got)
}

// slowVoice leva delay para falar cada frase e anota quando começou e acabou.
type slowVoice struct {
	delay time.Duration
	mu    sync.Mutex
	said  []string
	start []time.Time
	end   []time.Time
}

func (v *slowVoice) Speak(ctx context.Context, text string, onChunk func([]int16)) error {
	v.mu.Lock()
	v.said = append(v.said, text)
	v.start = append(v.start, time.Now())
	v.mu.Unlock()
	onChunk(make([]int16, 2400))
	time.Sleep(v.delay)
	v.mu.Lock()
	v.end = append(v.end, time.Now())
	v.mu.Unlock()
	return nil
}

func (v *slowVoice) spoken() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.said)
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("não aconteceu a tempo: %s", what)
}

func idle(in *Interpreter) bool {
	s := in.Status()
	return s.State != "ouvindo" && s.State != "traduzindo"
}

// Com earlySilence de pausa o áudio da frase já é o que vai ser cortado: o
// que é adiantado tem que ser idêntico à frase que sai depois.
func TestSegmenterAnnouncesThePhraseEarly(t *testing.T) {
	// A pausa se confirma: a frase é a que foi adiantada
	s := NewSegmenter()
	var events []segEvent
	events = append(events, s.feed(tone(1.2, true))...)
	events = append(events, s.feed(tone(earlySilence+0.06, false))...)
	if len(events) != 1 || events[0].kind != segEarly {
		t.Fatalf("com %.1fs de pausa esperava a frase adiantada, veio %+v", earlySilence, kinds(events))
	}
	early := events[0].audio
	events = s.feed(tone(endSilence, false))
	if len(events) != 1 || events[0].kind != segPhrase || !slices.Equal(events[0].audio, early) {
		t.Fatalf("a frase cortada pela pausa deveria ser igual à adiantada: %v, %d e %d amostras", kinds(events), len(events[0].audio), len(early))
	}

	// O atalho é solto no meio da pausa (com um resto de áudio que não completa 30 ms)
	s = NewSegmenter()
	s.feed(tone(1.2, true))
	events = s.feed(tone(earlySilence+0.137, false))
	if len(events) != 1 || events[0].kind != segEarly {
		t.Fatalf("esperava a frase adiantada, veio %v", kinds(events))
	}
	if got := s.Flush(); !slices.Equal(got, events[0].audio) {
		t.Fatalf("a frase ao soltar na pausa deveria ser igual à adiantada: %d e %d amostras", len(got), len(events[0].audio))
	}

	// A fala volta antes de a pausa se confirmar: o adiantado não vale
	s = NewSegmenter()
	s.feed(tone(1.2, true))
	events = s.feed(tone(earlySilence+0.06, false))
	events = append(events, s.feed(tone(0.5, true))...)
	if got := kinds(events); len(got) != 2 || got[0] != segEarly || got[1] != segResumed {
		t.Fatalf("esperava adiantada e depois retomada, veio %v", got)
	}
	// E uma nova pausa adianta de novo, com a frase inteira
	events = s.feed(tone(earlySilence+0.06, false))
	if len(events) != 1 || events[0].kind != segEarly || len(events[0].audio) <= len(early) {
		t.Fatalf("a nova pausa deveria adiantar a frase inteira: %v", kinds(events))
	}

	// Pausa sem fala suficiente antes não adianta nada
	s = NewSegmenter()
	s.feed(tone(0.2, true))
	if events := s.feed(tone(earlySilence+0.06, false)); len(events) != 0 {
		t.Fatalf("um estalo não é frase para adiantar: %v", kinds(events))
	}
}

func kinds(events []segEvent) []segKind {
	out := make([]segKind, len(events))
	for i, e := range events {
		out[i] = e.kind
	}
	return out
}

// O Whisper começa aos 0,3 s de pausa, sem esperar ela se confirmar aos
// 0,6 s; confirmada, a frase aproveita o que já foi feito.
func TestWhisperStartsBeforeThePauseIsConfirmed(t *testing.T) {
	whisper := &timedWhisper{}
	haiku := &timedCorrector{}
	out, voice := &fakeOutput{}, &fakeVoice{}
	in := New(Config{WhisperEndpoint: whisper.server(t).URL, Voice: voice, Correctors: []Corrector{haiku}}, out)
	defer in.Close()

	in.Talk(true)
	in.Feed(tone(1.2, true))
	in.Feed(tone(earlySilence+0.06, false))
	waitUntil(t, "o Whisper começar na pausa", func() bool { return whisper.count("traduz") == 1 })
	time.Sleep(50 * time.Millisecond)
	if out.isBusy() || len(haiku.calls()) != 0 || len(voice.said) != 0 {
		t.Fatalf("antes de a pausa se confirmar nada é corrigido nem falado: busy=%v, correções=%d, falas=%d", out.isBusy(), len(haiku.calls()), len(voice.said))
	}

	in.Feed(tone(endSilence, false)) // a pausa se confirma: é uma frase
	waitUntil(t, "a frase ser falada", func() bool { voice.mu.Lock(); defer voice.mu.Unlock(); return len(voice.said) == 1 })
	in.Talk(false)
	waitUntil(t, "o Intérprete ficar livre", func() bool { return idle(in) })
	if pt, tr := whisper.count("pt"), whisper.count("traduz"); pt != 1 || tr != 1 {
		t.Fatalf("a frase deveria aproveitar o Whisper adiantado: %d transcrições e %d traduções", pt, tr)
	}
	if got := haiku.calls(); len(got) != 1 || got[0] != heardPT {
		t.Fatalf("correções: %q", got)
	}
	if voice.said[0] != "EN de: "+heardPT {
		t.Fatalf("falou %q", voice.said)
	}
}

// Era só uma pausa no meio da frase: o que foi adiantado é descartado, sem
// gastar o corretor nem falar meia frase.
func TestEarlyWorkIsDroppedWhenSpeechResumes(t *testing.T) {
	whisper := &timedWhisper{pt: []string{"meia frase", "a frase inteira"}}
	haiku := &timedCorrector{}
	out, voice := &fakeOutput{}, &fakeVoice{}
	in := New(Config{WhisperEndpoint: whisper.server(t).URL, Voice: voice, Correctors: []Corrector{haiku}}, out)
	defer in.Close()

	in.Talk(true)
	in.Feed(tone(1.2, true))
	in.Feed(tone(earlySilence+0.06, false))
	waitUntil(t, "o Whisper começar na pausa", func() bool { return whisper.count("pt") == 1 })
	in.Feed(tone(1.0, true)) // a fala voltou
	in.Talk(false)
	waitUntil(t, "a frase ser falada", func() bool { voice.mu.Lock(); defer voice.mu.Unlock(); return len(voice.said) == 1 })
	waitUntil(t, "o Intérprete ficar livre", func() bool { return idle(in) })
	time.Sleep(50 * time.Millisecond)

	if got := haiku.calls(); len(got) != 1 || got[0] != "a frase inteira" {
		t.Fatalf("só a frase inteira vai para o corretor: %q", got)
	}
	if len(voice.said) != 1 || voice.said[0] != "EN de: a frase inteira" {
		t.Fatalf("só a frase inteira é falada: %q", voice.said)
	}
	if out.isBusy() {
		t.Fatal("a sua voz deveria ter voltado para a reunião")
	}
}

// Soltar o atalho no meio da pausa: a frase é a mesma que foi adiantada.
func TestReleasingDuringThePauseReusesTheEarlyWork(t *testing.T) {
	whisper := &timedWhisper{}
	out, voice := &fakeOutput{}, &fakeVoice{}
	in := New(Config{WhisperEndpoint: whisper.server(t).URL, Voice: voice}, out)
	defer in.Close()

	in.Talk(true)
	in.Feed(tone(1.2, true))
	in.Feed(tone(earlySilence+0.137, false))
	waitUntil(t, "o Whisper começar na pausa", func() bool { return whisper.count("pt") == 1 })
	in.Talk(false)
	waitUntil(t, "a frase ser falada", func() bool { voice.mu.Lock(); defer voice.mu.Unlock(); return len(voice.said) == 1 })
	waitUntil(t, "o Intérprete ficar livre", func() bool { return idle(in) })
	if pt, tr := whisper.count("pt"), whisper.count("traduz"); pt != 1 || tr != 1 {
		t.Fatalf("soltar na pausa deveria aproveitar o Whisper adiantado: %d transcrições e %d traduções", pt, tr)
	}
}

// Conferir o idioma roda enquanto a correção está na rede, e não antes de
// tudo: a frase não espera as duas em série.
func TestLanguageCheckRunsWhileTheCorrectionIsOut(t *testing.T) {
	whisper := &timedWhisper{delay: map[string]time.Duration{"idioma": 150 * time.Millisecond}}
	haiku := &timedCorrector{delay: 150 * time.Millisecond}
	out, voice := &fakeOutput{}, &fakeVoice{}
	in := New(Config{WhisperEndpoint: whisper.server(t).URL, Voice: voice, Correctors: []Corrector{haiku}}, out)
	defer in.Close()

	press(t, in, tone(1.2, true))
	if len(voice.said) != 1 {
		t.Fatalf("falou %q", voice.said)
	}
	detect := whisper.call("idioma", 0)
	haiku.mu.Lock()
	corrected := haiku.at[0]
	haiku.mu.Unlock()
	if !corrected.Before(detect.end) {
		t.Fatalf("a correção deveria começar antes de a detecção de idioma terminar (correção %v depois do fim da detecção)", corrected.Sub(detect.end))
	}
	// O Whisper traduz antes de conferir o idioma
	if tr := whisper.call("traduz", 0); !tr.end.Before(detect.start.Add(time.Millisecond)) {
		t.Fatal("a tradução deveria vir antes da detecção de idioma")
	}
}

// O retorno em inglês continua descartado, e descartá-lo não tira o corretor
// de uso: a frase seguinte é corrigida normalmente.
func TestEnglishEchoIsStillDroppedAndTheCorrectorSurvives(t *testing.T) {
	whisper := &timedWhisper{lang: []string{"english", "english", "portuguese"}, pt: []string{"o retorno", heardPT}}
	haiku := &timedCorrector{delay: 100 * time.Millisecond}
	out, voice := &fakeOutput{}, &fakeVoice{}
	in := New(Config{WhisperEndpoint: whisper.server(t).URL, Voice: voice, Correctors: []Corrector{haiku}}, out)
	defer in.Close()

	status := press(t, in, tone(1.2, true))
	if len(voice.said) != 0 || len(out.speech) != 0 || !strings.Contains(status.Message, "Ouvi inglês") {
		t.Fatalf("o retorno em inglês não deveria ser falado: %q, %+v", voice.said, status)
	}
	if out.isBusy() {
		t.Fatal("a sua voz deveria voltar para a reunião")
	}

	status = press(t, in, tone(1.2, true))
	if len(voice.said) != 1 || voice.said[0] != "EN de: "+heardPT {
		t.Fatalf("a frase seguinte deveria ser falada: %q", voice.said)
	}
	if strings.Contains(status.Message, "indisponível") || strings.Contains(status.Message, "literal") {
		t.Fatalf("o corretor não deveria ter saído de uso por causa do retorno descartado: %+v", status)
	}
	if got := haiku.calls(); got[len(got)-1] != heardPT {
		t.Fatalf("a frase seguinte deveria passar pelo corretor: %q", got)
	}
}

// Duas frases seguidas: a segunda é entendida enquanto a primeira é falada
// (antes, só começava depois), e a fala segue a ordem.
func TestNextPhraseIsUnderstoodWhileThePreviousIsSpoken(t *testing.T) {
	whisper := &timedWhisper{pt: []string{"primeira frase", "segunda frase"}}
	voice := &slowVoice{delay: 400 * time.Millisecond}
	out := &fakeOutput{}
	in := New(Config{WhisperEndpoint: whisper.server(t).URL, Voice: voice}, out)
	defer in.Close()

	in.Talk(true)
	in.Feed(tone(1.2, true))
	in.Feed(tone(endSilence+0.06, false)) // fecha a 1ª
	waitUntil(t, "a 1ª frase começar a ser falada", func() bool { return len(voice.spoken()) == 1 })
	in.Feed(tone(1.2, true))
	in.Talk(false) // fecha a 2ª, com a 1ª ainda sendo falada
	waitUntil(t, "as duas frases serem faladas", func() bool { voice.mu.Lock(); defer voice.mu.Unlock(); return len(voice.end) == 2 })
	waitUntil(t, "o Intérprete ficar livre", func() bool { return idle(in) })

	if got := voice.spoken(); len(got) != 2 || got[0] != "EN de: primeira frase" || got[1] != "EN de: segunda frase" {
		t.Fatalf("as frases deveriam ser faladas na ordem: %q", got)
	}
	voice.mu.Lock()
	firstEnd, secondStart := voice.end[0], voice.start[1]
	voice.mu.Unlock()
	second := whisper.call("traduz", 1)
	if !second.end.Before(firstEnd) {
		t.Fatalf("a 2ª frase deveria ser entendida enquanto a 1ª era falada (terminou %v depois)", second.end.Sub(firstEnd))
	}
	if gap := secondStart.Sub(firstEnd); gap > 100*time.Millisecond {
		t.Fatalf("a 2ª frase deveria começar assim que a 1ª acaba; esperou %v", gap)
	}
	if out.isBusy() {
		t.Fatal("a sua voz deveria ter voltado para a reunião")
	}
}

// Os pedidos de uma frase ao Whisper vão juntos: os de uma frase nova não
// passam na frente do resto da anterior, que é a que vai ser falada primeiro.
func TestWhisperRequestsOfAPhraseStayTogether(t *testing.T) {
	whisper := &timedWhisper{pt: []string{"primeira frase", "segunda frase"},
		delay: map[string]time.Duration{"pt": 60 * time.Millisecond, "traduz": 60 * time.Millisecond, "idioma": 60 * time.Millisecond}}
	out, voice := &fakeOutput{}, &fakeVoice{}
	in := New(Config{WhisperEndpoint: whisper.server(t).URL, Voice: voice}, out)
	defer in.Close()

	in.Talk(true)
	in.Feed(tone(1.2, true))
	in.Feed(tone(endSilence+0.06, false))
	in.Feed(tone(1.2, true))
	in.Talk(false)
	waitUntil(t, "as duas frases serem faladas", func() bool { voice.mu.Lock(); defer voice.mu.Unlock(); return len(voice.said) == 2 })
	whisper.mu.Lock()
	var order []string
	for _, c := range whisper.calls {
		order = append(order, c.kind)
	}
	whisper.mu.Unlock()
	if got := strings.Join(order, " "); got != "pt traduz idioma pt traduz idioma" {
		t.Fatalf("ordem dos pedidos ao Whisper: %s", got)
	}
}
