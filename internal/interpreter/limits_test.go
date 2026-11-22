package interpreter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedWhisper responde como o whisper-server, com uma transcrição (e a
// tradução literal dela) para cada frase, em ordem.
type scriptedWhisper struct {
	mu   sync.Mutex
	pt   []string
	en   []string
	n    int  // frases transcritas até agora
	hang bool // não responde: só volta quando quem pediu desiste
}

func (s *scriptedWhisper) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.hang {
			s.mu.Unlock()
			<-r.Context().Done()
			s.mu.Lock()
			return
		}
		switch {
		case fields["detect_language"] == "true":
			json.NewEncoder(w).Encode(map[string]string{"text": "", "language": "portuguese"})
		case fields["translate"] == "true":
			json.NewEncoder(w).Encode(map[string]string{"text": s.en[min(max(s.n-1, 0), len(s.en)-1)]})
		default:
			json.NewEncoder(w).Encode(map[string]string{"text": s.pt[min(s.n, len(s.pt)-1)]})
			s.n++
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// press aperta o atalho, fala audio e solta; espera a frase ser resolvida
// (falada, descartada ou abandonada).
func press(t *testing.T, in *Interpreter, audio []int16) Status {
	t.Helper()
	in.Talk(true)
	in.Feed(audio)
	in.Talk(false)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := in.Status(); s.State != "ouvindo" && s.State != "traduzindo" {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("a frase não foi resolvida em 5 s: %+v", in.Status())
	return Status{}
}

// stuckVoice manda chunks pedaços de áudio e depois não manda mais nada: só
// volta quando quem pediu desiste.
type stuckVoice struct{ chunks int }

func (v stuckVoice) Speak(ctx context.Context, _ string, onChunk func([]int16)) error {
	for range v.chunks {
		onChunk(make([]int16, 2400))
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestPhraseIsAbandonedWhenWhisperDoesNotAnswer(t *testing.T) {
	whisper := &scriptedWhisper{hang: true}
	out, voice := &fakeOutput{}, &fakeVoice{}
	in := New(Config{WhisperEndpoint: whisper.server(t).URL, Voice: voice,
		Limits: Limits{Understand: 150 * time.Millisecond}}, out)
	defer in.Close()

	started := time.Now()
	status := press(t, in, tone(1.2, true))
	if took := time.Since(started); took > 2*time.Second {
		t.Fatalf("a frase deveria ser abandonada no prazo, levou %v", took)
	}
	if !strings.Contains(status.Message, "Whisper não respondeu") {
		t.Fatalf("a tela deveria dizer que o Whisper não respondeu: %+v", status)
	}
	if out.isBusy() {
		t.Fatal("abandonada a frase, a sua voz volta para a reunião")
	}
	if len(voice.said) != 0 {
		t.Fatalf("nada deveria ser falado: %q", voice.said)
	}
}

func TestSlowCorrectorGoesLiteralAndStaysOutForAWhile(t *testing.T) {
	whisper := &scriptedWhisper{pt: []string{heardPT}, en: []string{whisperEN}}
	haiku := &fakeCorrector{name: "Claude Haiku", hang: true}
	out, voice := &fakeOutput{}, &fakeVoice{}
	in := New(Config{WhisperEndpoint: whisper.server(t).URL, Voice: voice, Correctors: []Corrector{haiku},
		Limits: Limits{Correct: 100 * time.Millisecond, Cooldown: time.Hour}}, out)
	defer in.Close()

	// 1ª frase: espera o prazo do corretor e vai a literal, com aviso
	status := press(t, in, tone(1.2, true))
	if len(voice.said) != 1 || voice.said[0] != whisperEN || !strings.Contains(status.Message, "literal") {
		t.Fatalf("sem resposta do corretor deveria falar a literal: %q, %+v", voice.said, status)
	}
	if haiku.calls() != 1 {
		t.Fatalf("o corretor deveria ter sido tentado uma vez, foi %d", haiku.calls())
	}

	// 2ª frase: o corretor está de fora, nem é chamado (a frase não espera)
	started := time.Now()
	status = press(t, in, tone(1.2, true))
	if haiku.calls() != 1 {
		t.Fatalf("o corretor que falhou deveria ficar de fora, foi chamado %d vezes", haiku.calls())
	}
	if len(voice.said) != 2 || !strings.Contains(status.Message, "Claude Haiku indisponível") {
		t.Fatalf("a 2ª frase vai literal, avisando: %q, %+v", voice.said, status)
	}
	if took := time.Since(started); took > time.Second {
		t.Fatalf("a 2ª frase não deveria esperar o corretor, levou %v", took)
	}

	// Passado o tempo de fora, ele é tentado de novo (e desta vez responde)
	in.mu.Lock()
	in.down[haiku] = time.Now().Add(-time.Second)
	in.mu.Unlock()
	haiku.mu.Lock()
	haiku.hang, haiku.fixed = false, correctedEN
	haiku.mu.Unlock()
	press(t, in, tone(1.2, true))
	if haiku.calls() != 2 || len(voice.said) != 3 || voice.said[2] != correctedEN {
		t.Fatalf("depois do tempo de fora o corretor volta: %d chamadas, falou %q", haiku.calls(), voice.said)
	}
}

// O usuário troca a correção com uma frase sendo corrigida: o corretor antigo
// falha (foi fechado), mas quem fica de fora é ele, e não o novo.
func TestReplacedCorrectorDoesNotTakeTheNewOneOut(t *testing.T) {
	whisper := &scriptedWhisper{pt: []string{heardPT}, en: []string{whisperEN}}
	old := &fakeCorrector{name: "Claude Haiku", hang: true}
	out, voice := &fakeOutput{}, &fakeVoice{}
	in := New(Config{WhisperEndpoint: whisper.server(t).URL, Voice: voice, Correctors: []Corrector{old},
		Limits: Limits{Correct: 300 * time.Millisecond, Cooldown: time.Hour}}, out)
	defer in.Close()

	in.Talk(true)
	in.Feed(tone(1.2, true))
	in.Talk(false)
	deadline := time.Now().Add(5 * time.Second)
	for old.calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	replacement := &fakeCorrector{name: "Claude Haiku", fixed: correctedEN}
	in.SetCorrectors([]Corrector{replacement})
	for in.Status().State == "traduzindo" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(voice.said) != 1 || voice.said[0] != whisperEN {
		t.Fatalf("a frase que estava com o corretor antigo vai literal: %q", voice.said)
	}

	press(t, in, tone(1.2, true))
	if replacement.calls() != 1 || len(voice.said) != 2 || voice.said[1] != correctedEN {
		t.Fatalf("a frase seguinte deveria ser corrigida pelo novo: %d chamadas, falou %q", replacement.calls(), voice.said)
	}
}

func TestDiscardedAnswerDoesNotTakeTheCorrectorOut(t *testing.T) {
	whisper := &scriptedWhisper{pt: []string{heardPT}, en: []string{whisperEN}}
	haiku := &fakeCorrector{name: "Claude Haiku", err: fmt.Errorf("%w: resposta longa demais", errImplausible)}
	out, voice := &fakeOutput{}, &fakeVoice{}
	in := New(Config{WhisperEndpoint: whisper.server(t).URL, Voice: voice, Correctors: []Corrector{haiku}}, out)
	defer in.Close()

	status := press(t, in, tone(1.2, true))
	if !strings.Contains(status.Message, "descartada") || !strings.Contains(status.Message, "literal") {
		t.Fatalf("a resposta descartada deveria ser avisada: %+v", status)
	}
	press(t, in, tone(1.2, true))
	if haiku.calls() != 2 {
		t.Fatalf("uma resposta ruim vale só para a frase: o corretor deveria ser chamado de novo, foi %d vezes", haiku.calls())
	}
}

func TestVoiceThatStopsAnsweringReleasesTheMeeting(t *testing.T) {
	cases := []struct {
		name   string
		chunks int
		want   string
	}{
		{"sem áudio nenhum", 0, "a voz falhou: ficou"},
		{"parou no meio", 2, "no meio da frase"},
	}
	for _, c := range cases {
		whisper := &scriptedWhisper{pt: []string{heardPT}, en: []string{whisperEN}}
		out := &fakeOutput{}
		in := New(Config{WhisperEndpoint: whisper.server(t).URL, Voice: stuckVoice{c.chunks},
			Limits: Limits{VoiceStall: 150 * time.Millisecond}}, out)
		started := time.Now()
		status := press(t, in, tone(1.2, true))
		in.Close()
		if !strings.Contains(status.Message, c.want) {
			t.Fatalf("%s: a tela deveria dizer %q: %+v", c.name, c.want, status)
		}
		if out.isBusy() || time.Since(started) > 2*time.Second {
			t.Fatalf("%s: a sua voz deveria voltar para a reunião logo (levou %v)", c.name, time.Since(started))
		}
		if len(out.speech) != c.chunks*2400 {
			t.Fatalf("%s: foram %d amostras para a reunião", c.name, len(out.speech))
		}
	}
}

// O português de reunião repete os termos em inglês da tradução anterior: só
// é retorno a frase gravada enquanto a tradução tocava (ou logo depois).
func TestEchoFilterOnlyAppliesWhileTheTranslationPlays(t *testing.T) {
	pt := []string{"O deploy do backend da API tá pronto", "A API do backend teve deploy ontem"}
	en := []string{"The deploy of the backend API is ready.", "The backend API was deployed yesterday."}
	if !repeats(pt[1], en[:1]) {
		t.Fatal("o teste precisa de uma frase que pareça repetir a tradução anterior")
	}

	// A tradução já acabou de tocar: a frase nova é traduzida
	whisper := &scriptedWhisper{pt: pt, en: en}
	out, voice := &fakeOutput{}, &fakeVoice{}
	in := New(Config{WhisperEndpoint: whisper.server(t).URL, Voice: voice,
		Limits: Limits{EchoTail: 20 * time.Millisecond}}, out)
	press(t, in, tone(1.2, true))
	time.Sleep(100 * time.Millisecond)
	status := press(t, in, tone(1.2, true))
	in.Close()
	if len(voice.said) != 2 || voice.said[1] != en[1] {
		t.Fatalf("a frase nova deveria ser traduzida: %q, %+v", voice.said, status)
	}

	// A tradução ainda está tocando (5 s na fila) quando a frase é gravada: é o retorno
	whisper = &scriptedWhisper{pt: pt, en: en}
	out, voice = &fakeOutput{pending: 5 * time.Second}, &fakeVoice{}
	in = New(Config{WhisperEndpoint: whisper.server(t).URL, Voice: voice}, out)
	press(t, in, tone(1.2, true))
	status = press(t, in, tone(1.2, true))
	in.Close()
	if len(voice.said) != 1 || !strings.Contains(status.Message, "repetia a última tradução") {
		t.Fatalf("o retorno da tradução não deveria ser traduzido de novo: %q, %+v", voice.said, status)
	}
}

func TestCloseAbandonsThePhraseInProgress(t *testing.T) {
	whisper := &scriptedWhisper{pt: []string{heardPT}, en: []string{whisperEN}}
	haiku := &fakeCorrector{name: "Claude Haiku", hang: true}
	out, voice := &fakeOutput{}, &fakeVoice{}
	in := New(Config{WhisperEndpoint: whisper.server(t).URL, Voice: voice, Correctors: []Corrector{haiku},
		Limits: Limits{Correct: time.Minute, Understand: time.Minute}}, out)
	in.Talk(true)
	in.Feed(tone(1.2, true))
	in.Talk(false)
	deadline := time.Now().Add(5 * time.Second)
	for haiku.calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if haiku.calls() != 1 {
		t.Fatal("a frase deveria estar na correção")
	}

	started := time.Now()
	in.Close() // espera o worker: depois dele ninguém usa mais os corretores
	if took := time.Since(started); took > time.Second {
		t.Fatalf("o Close deveria interromper a frase, levou %v", took)
	}
	in.Close() // duas vezes não quebra
	time.Sleep(50 * time.Millisecond)
	if len(voice.said) != 0 || haiku.calls() != 1 {
		t.Fatalf("depois do Close nada mais é falado nem corrigido: %q, %d", voice.said, haiku.calls())
	}
}
