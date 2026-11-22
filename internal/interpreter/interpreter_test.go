package interpreter

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// tone gera seconds de áudio a 16 kHz: voz ou silêncio com ruído baixo
// (~-65 dB). A voz varia de volume como fala (sílabas a cada 0,15 s, com
// ~14 dB de diferença), na amplitude amp.
func tone(seconds float64, voice bool) []int16 {
	return speechAt(seconds, voice, 8000)
}

func speechAt(seconds float64, voice bool, amp float64) []int16 {
	n := int(seconds * WhisperRate)
	out := make([]int16, n)
	for i := range out {
		if voice {
			a := amp
			if (i/(WhisperRate*15/100))%2 == 1 {
				a = amp / 5
			}
			out[i] = int16(a * math.Sin(float64(i)*2*math.Pi*220/WhisperRate))
		} else {
			out[i] = int16((i*7919)%61 - 30)
		}
	}
	return out
}

// feedAll manda o áudio em pedaços de tamanhos variados, como chegam do microfone.
func feedAll(s *Segmenter, samples []int16) (got [][]int16) {
	for len(samples) > 0 {
		n := min(len(samples), 1000)
		got = append(got, s.Feed(samples[:n])...)
		samples = samples[n:]
	}
	return got
}

func TestSegmenterSplitsOnPausesAndFlushes(t *testing.T) {
	s := NewSegmenter()
	var got [][]int16
	feed := func(samples []int16) { got = append(got, feedAll(s, samples)...) }
	feed(tone(0.5, false)) // apertou e demorou para falar
	feed(tone(1.5, true))
	feed(tone(0.8, false)) // pausa: fecha a 1ª frase
	feed(tone(0.2, true))  // um estalo curto não fecha frase
	feed(tone(0.8, false))
	feed(tone(1.0, true)) // soltou falando
	if len(got) != 1 {
		t.Fatalf("esperava 1 frase fechada pela pausa, veio %d", len(got))
	}
	if d := float64(len(got[0])) / WhisperRate; d < 1.9 || d > 2.6 {
		t.Fatalf("1ª frase (com o começo do aperto) com %.2fs", d)
	}
	// Ao soltar, vai tudo desde a 1ª frase: o estalo, a pausa e a fala
	last := s.Flush()
	if d := float64(len(last)) / WhisperRate; d < 1.9 || d > 2.4 {
		t.Fatalf("frase ao soltar com %.2fs", d)
	}
	if s.Flush() != nil {
		t.Fatal("depois de soltar não sobra frase")
	}

	// Frase sem pausa é cortada em maxUtterance
	s = NewSegmenter()
	if n := len(feedAll(s, tone(maxUtterance+1, true))); n != 1 {
		t.Fatalf("frase longa deveria ser cortada, veio %d", n)
	}
}

// O bug do "às vezes não traduz": o ruído era medido antes do aperto (com o
// Meet tocando no alto-falante ou você falando na reunião) e uma fala mais
// baixa que ele era descartada sem aviso. Agora só conta o próprio aperto.
func TestSegmenterKeepsQuietSpeech(t *testing.T) {
	s := NewSegmenter()
	feedAll(s, speechAt(2, true, 250)) // ~-45 dB
	if u := s.Flush(); float64(len(u))/WhisperRate < 1.9 {
		t.Fatalf("fala baixa deveria ir para o Whisper, veio %d amostras", len(u))
	}
}

func TestSegmenterDropsPressWithoutSpeech(t *testing.T) {
	s := NewSegmenter()
	feedAll(s, tone(2, false))
	if s.Flush() != nil {
		t.Fatal("só ruído não é frase")
	}
	if s.Heard() > minVoiceDB {
		t.Fatalf("volume ouvido %.0f dB", s.Heard())
	}
	// Ruído constante e alto (ventilador) também não
	s = NewSegmenter()
	hum := make([]int16, 2*WhisperRate)
	for i := range hum {
		hum[i] = int16(3000 * math.Sin(float64(i)*2*math.Pi*120/WhisperRate))
	}
	if feedAll(s, hum); s.Flush() != nil {
		t.Fatal("ruído constante não é frase")
	}
	// Segurando o atalho muito tempo sem falar: nada vai para o Whisper
	s = NewSegmenter()
	if got := feedAll(s, tone(maxUtterance+1, false)); len(got) != 0 {
		t.Fatalf("ruído longo não deveria virar frase: %d", len(got))
	}
	// Um toque rápido no atalho
	s = NewSegmenter()
	feedAll(s, tone(0.2, true))
	if s.Flush() != nil {
		t.Fatal("toque curto não é frase")
	}
}

func TestMixerRoutesVoiceAndEnglish(t *testing.T) {
	var m mixer
	frame := func(v float32) []float32 {
		f := make([]float32, 480) // 10 ms a 48 kHz
		for i := range f {
			f[i] = v
		}
		return f
	}
	out := make([]float32, 480)

	// Fora do atalho: a sua voz vai para a reunião
	if talk := m.micIn(frame(0.5)); talk != nil {
		t.Fatal("fora do atalho nada vai para a tradução")
	}
	m.meetingOut(out)
	if out[0] != 0.5 {
		t.Fatalf("a sua voz deveria ir para a reunião, veio %v", out[0])
	}

	// Atalho apertado: a voz vai para a tradução, a reunião fica em silêncio
	m.setTalking(true)
	talk := m.micIn(frame(0.5))
	if len(talk) != 160 || talk[0] == 0 {
		t.Fatalf("esperava 10 ms a 16 kHz para a tradução, veio %d amostras", len(talk))
	}
	m.meetingOut(out)
	if out[0] != 0 {
		t.Fatalf("com o atalho apertado a reunião não ouve você, veio %v", out[0])
	}

	// Soltou, mas a tradução ainda está em andamento: continua mudo
	m.setTalking(false)
	m.setBusy(true)
	m.micIn(frame(0.5))
	m.meetingOut(out)
	if out[0] != 0 {
		t.Fatal("enquanto traduz, a reunião não ouve você")
	}

	// O inglês chega: vai para a reunião e para o fone; você segue mudo até ele acabar
	speech := make([]int16, 240) // 10 ms a 24 kHz
	for i := range speech {
		speech[i] = 16384
	}
	m.enqueue(speech, true)
	m.setBusy(false)
	if p := m.pending(); p != 10*time.Millisecond {
		t.Fatalf("pendente %v", p)
	}
	m.micIn(frame(0.5))
	m.meetingOut(out)
	if math.Abs(float64(out[479])-0.5) > 0.01 {
		t.Fatalf("a reunião deveria ouvir o inglês (0.5), veio %v", out[479])
	}
	mon := make([]float32, 480)
	m.monitorOut(mon)
	if math.Abs(float64(mon[479])-0.5) > 0.01 {
		t.Fatalf("o fone deveria ouvir o inglês, veio %v", mon[479])
	}

	// Inglês acabou: a sua voz volta
	m.micIn(frame(0.25))
	m.meetingOut(out)
	if out[0] != 0.25 {
		t.Fatalf("depois do inglês, a sua voz volta, veio %v", out[0])
	}

	// O repasse nunca acumula mais de 100 ms
	for range 50 {
		m.micIn(frame(0.1))
	}
	if len(m.pass) > maxPassBacklog {
		t.Fatalf("repasse acumulou %d amostras", len(m.pass))
	}
}

// fakeOutput registra o que o intérprete manda para o roteador.
type fakeOutput struct {
	mu      sync.Mutex
	talking bool
	busy    bool
	speech  []int16
	log     []string
	pending time.Duration // quanto inglês "ainda falta tocar"
}

func (f *fakeOutput) SetTalking(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.talking = on
	f.log = append(f.log, map[bool]string{true: "mudo", false: "soltou"}[on])
}
func (f *fakeOutput) SetBusy(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.busy = on
}
func (f *fakeOutput) Enqueue(s []int16) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.speech = append(f.speech, s...)
}
func (f *fakeOutput) Pending() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pending
}

func (f *fakeOutput) isBusy() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.busy
}

type fakeVoice struct {
	mu   sync.Mutex
	said []string
}

func (v *fakeVoice) Speak(_ context.Context, text string, onChunk func([]int16)) error {
	v.mu.Lock()
	v.said = append(v.said, text)
	v.mu.Unlock()
	onChunk(make([]int16, 2400))
	onChunk(make([]int16, 2400))
	return nil
}

const (
	heardPT      = "Acho que quem tá acompanhando é o Mauro."
	whisperEN    = "I think that who is following is Mauro."
	correctedEN  = "I think it's Mauro who is following it."
	otherFixedEN = "I believe Mauro is the one following it."
)

// fakeServices responde como o whisper-server; lang é o idioma detectado, ou
// "sem-fala" para responder como ele responde a um áudio só com ruído.
func fakeServices(lang string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference" {
			http.NotFound(w, r)
			return
		}
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
			}
		}
		switch {
		case fields["detect_language"] == "true" && fields["response_format"] == "verbose_json":
			p := map[string]float64{"english": 0.93, "english-incerto": 0.42}[lang]
			json.NewEncoder(w).Encode(map[string]any{"text": "", "language": "english",
				"language_probabilities": map[string]float64{"en": p, "pt": 1 - p}})
		case fields["detect_language"] == "true":
			json.NewEncoder(w).Encode(map[string]string{"text": "", "language": strings.TrimSuffix(lang, "-incerto")})
		case lang == "sem-fala" && fields["translate"] == "true":
			// Do ruído do microfone, a tradução sai inventada
			json.NewEncoder(w).Encode(map[string]string{"text": " Thank you for watching!"})
		case lang == "sem-fala":
			json.NewEncoder(w).Encode(map[string]string{"text": " [silêncio]"})
		case fields["translate"] == "true":
			json.NewEncoder(w).Encode(map[string]string{"text": " " + whisperEN})
		default:
			json.NewEncoder(w).Encode(map[string]string{"text": " " + heardPT})
		}
	}))
}

// fakeCorrector corrige para fixed, ou falha com err. Com hang, não responde:
// só volta quando quem chamou desiste (o prazo, o Close).
type fakeCorrector struct {
	name, fixed string
	err         error
	hang        bool
	mu          sync.Mutex
	got         []string
}

func (c *fakeCorrector) Name() string  { return c.name }
func (c *fakeCorrector) Warmup() error { return nil }
func (c *fakeCorrector) Close()        {}
func (c *fakeCorrector) Correct(ctx context.Context, pt, en string) (string, error) {
	c.mu.Lock()
	c.got = append(c.got, pt+" | "+en)
	hang := c.hang
	c.mu.Unlock()
	if hang {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return c.fixed, c.err
}

func (c *fakeCorrector) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.got)
}

// writeFrame manda um quadro da resposta do /speak, como o servidor de voz.
func writeFrame(w http.ResponseWriter, kind byte, payload []byte) {
	header := []byte{kind, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(header[1:], uint32(len(payload)))
	w.Write(header)
	w.Write(payload)
	w.(http.Flusher).Flush()
}

// pcm são n amostras de valor v, em bytes.
func pcm(n int, v int16) []byte {
	out := make([]byte, 2*n)
	for i := range n {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out
}

// runInterpreter aperta o atalho, manda audio e solta; devolve o que foi
// para a reunião, o que a voz falou e os estados mostrados na tela.
func runInterpreter(t *testing.T, lang string, audio []int16, correctors ...Corrector) (*fakeOutput, *fakeVoice, []Status) {
	srv := fakeServices(lang)
	defer srv.Close()
	out := &fakeOutput{}
	voice := &fakeVoice{}
	var mu sync.Mutex
	var statuses []Status
	in := New(Config{
		WhisperEndpoint: srv.URL,
		Correctors:      correctors,
		Voice:           voice,
		OnStatus: func(s Status) {
			mu.Lock()
			statuses = append(statuses, s)
			mu.Unlock()
		},
	}, out)
	defer in.Close()

	in.Talk(true)
	in.Talk(true) // o atalho repete enquanto está apertado
	in.Feed(audio)
	time.Sleep(250 * time.Millisecond) // a tela vê "ouvindo"
	in.Talk(false)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := in.Status(); s.State == "pronto" && (s.EN != "" || s.Message != "") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	return out, voice, append([]Status(nil), statuses...)
}

func TestInterpreterCorrectsWhisperTranslation(t *testing.T) {
	haiku := &fakeCorrector{name: "Claude Haiku", fixed: correctedEN}
	out, voice, statuses := runInterpreter(t, "portuguese", tone(1.2, true), haiku)
	if len(voice.said) != 1 || voice.said[0] != correctedEN {
		t.Fatalf("falou %q", voice.said)
	}
	// O corretor recebe o português e a tradução literal do Whisper
	if len(haiku.got) != 1 || haiku.got[0] != heardPT+" | "+whisperEN {
		t.Fatalf("corretor recebeu %q", haiku.got)
	}
	if len(out.speech) != 4800 {
		t.Fatalf("esperava 0,2 s de inglês para a reunião, veio %d amostras", len(out.speech))
	}
	if strings.Join(out.log, ",") != "mudo,soltou" || out.busy {
		t.Fatalf("roteador: %v, busy=%v", out.log, out.busy)
	}
	var states []string
	for _, s := range statuses {
		if len(states) == 0 || states[len(states)-1] != s.State {
			states = append(states, s.State)
		}
	}
	if got := strings.Join(states, ","); !strings.HasPrefix(got, "ouvindo,") || !strings.HasSuffix(got, "pronto") {
		t.Fatalf("estados: %s", got)
	}
	last := statuses[len(statuses)-1]
	if last.PT != heardPT || last.EN != correctedEN || last.Literal != whisperEN || last.Delay <= 0 || last.Message != "" {
		t.Fatalf("última frase: %+v", last)
	}
}

func TestInterpreterSpeaksWhisperTranslationWithoutCorrector(t *testing.T) {
	_, voice, statuses := runInterpreter(t, "portuguese", tone(1.2, true))
	if len(voice.said) != 1 || voice.said[0] != whisperEN {
		t.Fatalf("sem corretor deveria falar a tradução do Whisper, falou %q", voice.said)
	}
	if last := statuses[len(statuses)-1]; last.Message != "" || last.Literal != "" {
		t.Fatalf("sem corretor não há aviso: %+v", last)
	}
}

func TestInterpreterFallsBackToNextCorrectorThenLiteral(t *testing.T) {
	ollama := &fakeCorrector{name: "Ollama", err: fmt.Errorf("o Ollama não respondeu")}
	haiku := &fakeCorrector{name: "Claude Haiku", fixed: otherFixedEN}
	_, voice, statuses := runInterpreter(t, "portuguese", tone(1.2, true), ollama, haiku)
	if len(voice.said) != 1 || voice.said[0] != otherFixedEN {
		t.Fatalf("com o Ollama parado deveria usar o Haiku, falou %q", voice.said)
	}
	if last := statuses[len(statuses)-1]; !strings.Contains(last.Message, "Ollama indisponível") || !strings.Contains(last.Message, "Claude Haiku") {
		t.Fatalf("deveria avisar a troca: %+v", last)
	}

	// Nenhum corretor: vai a literal, com aviso
	haiku = &fakeCorrector{name: "Claude Haiku", err: fmt.Errorf("sem rede")}
	_, voice, statuses = runInterpreter(t, "portuguese", tone(1.2, true), haiku)
	if len(voice.said) != 1 || voice.said[0] != whisperEN {
		t.Fatalf("sem corretor deveria falar a literal, falou %q", voice.said)
	}
	if last := statuses[len(statuses)-1]; !strings.Contains(last.Message, "literal") {
		t.Fatalf("deveria avisar que foi a literal: %+v", last)
	}
}

// Ruído no fim da fala às vezes sai "english" com pouca certeza: segue
// como português, em vez de perder a frase.
func TestInterpreterTranslatesUncertainEnglish(t *testing.T) {
	_, voice, _ := runInterpreter(t, "english-incerto", tone(1.2, true))
	if len(voice.said) != 1 || voice.said[0] != whisperEN {
		t.Fatalf("inglês incerto deveria seguir como português, falou %q", voice.said)
	}
}

func TestInterpreterExplainsEveryLostPress(t *testing.T) {
	cases := []struct {
		name, lang string
		audio      []int16
		want       string
	}{
		{"inglês (o retorno)", "english", tone(1.2, true), "Ouvi inglês"},
		{"só ruído", "portuguese", tone(1.2, false), "Não chegou som"},
		{"ruído constante", "portuguese", func() []int16 {
			hum := make([]int16, WhisperRate)
			for i := range hum {
				hum[i] = int16(3000 * math.Sin(float64(i)*2*math.Pi*120/WhisperRate))
			}
			return hum
		}(), "Não ouvi fala"},
	}
	for _, c := range cases {
		out, voice, statuses := runInterpreter(t, c.lang, c.audio)
		if len(voice.said) != 0 || len(out.speech) != 0 {
			t.Fatalf("%s: não deveria falar nada: %q", c.name, voice.said)
		}
		if out.busy {
			t.Fatalf("%s: a sua voz deveria voltar para a reunião", c.name)
		}
		if len(statuses) == 0 || !strings.Contains(statuses[len(statuses)-1].Message, c.want) {
			t.Fatalf("%s: a tela deveria explicar (%q): %+v", c.name, c.want, statuses)
		}
	}
}

func TestOllamaCorrector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model     string
			KeepAlive string `json:"keep_alive"`
			Messages  []struct{ Role, Content string }
		}
		json.NewDecoder(r.Body).Decode(&req)
		if r.URL.Path != "/api/chat" || req.KeepAlive == "" || req.Model != "gemma" {
			t.Errorf("pedido inesperado: %s %+v", r.URL.Path, req)
		}
		if len(req.Messages) == 0 { // Warmup: só carrega o modelo
			w.Write([]byte(`{}`))
			return
		}
		if !strings.Contains(req.Messages[1].Content, "PT: "+heardPT+"\nEN: "+whisperEN) {
			t.Errorf("mensagem: %q", req.Messages[1].Content)
		}
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": " " + correctedEN + "\n"}})
	}))
	defer srv.Close()
	o := &OllamaCorrector{Endpoint: srv.URL, Model: func() string { return "gemma" }}
	if err := o.Warmup(); err != nil {
		t.Fatal(err)
	}
	got, err := o.Correct(context.Background(), heardPT, whisperEN)
	if err != nil || got != correctedEN {
		t.Fatalf("correção %q, %v", got, err)
	}
	stopped := &OllamaCorrector{Endpoint: "http://127.0.0.1:1", Model: func() string { return "gemma" }}
	if _, err := stopped.Correct(context.Background(), heardPT, whisperEN); err == nil {
		t.Fatal("Ollama parado deveria dar erro")
	}
	// Quem chamou desistiu: o pedido não fica esperando o Ollama
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := o.Correct(ctx, heardPT, whisperEN); err == nil {
		t.Fatal("com o prazo esgotado deveria dar erro")
	}
}

func TestPlausibleRejectsAnswers(t *testing.T) {
	if plausible("Alguém tem dúvida?", "Someone has doubt?", "Does anyone have questions?") != nil {
		t.Fatal("tradução normal")
	}
	answer := "Sure! Here are some common questions people ask in meetings, along with suggestions for how to answer each of them."
	if plausible("Alguém tem dúvida?", "Someone has doubt?", answer) == nil {
		t.Fatal("uma resposta no lugar da correção deveria ser descartada")
	}
	// A frase que o Whisper cortou pela metade, recuperada pela correção
	pt := "Vamos pegar do real as ordens que foram feitas, o tempo e vamos ver o backtest, a hora que entrou e o preço que entrou. Aí você vai ver que as entradas vão estar parecidas, mas os valores são bem diferentes."
	en := "Let's see the backtest You will see that the entries are similar but the values are very different"
	fixed := "Let's get the real orders that were made, the time, and let's look at the backtest, the time it entered and the price it entered. Then you'll see that the entries are similar, but the values are very different."
	if plausible(pt, en, fixed) != nil {
		t.Fatal("recuperar o que o Whisper perdeu é uma correção válida")
	}
}

func TestRepeatsAndHallucinations(t *testing.T) {
	recent := []string{"I don't have any news about the test that was sent to the team."}
	if !repeats("No Suissa, I don't have any news about the test that was sent to the team", recent) {
		t.Fatal("o próprio inglês voltando deveria ser reconhecido")
	}
	if repeats("Não tenho notícia do teste que foi mandado para o time", recent) {
		t.Fatal("português novo não é repetição")
	}
	if !isHallucination(" Obrigado por assistir! ") || isHallucination("Obrigado pelo relatório") {
		t.Fatal("alucinações do Whisper")
	}
}

func TestVoiceSpeakStreamsAndSavesSample(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			json.NewEncoder(w).Encode(VoiceHealth{State: "baixando", Progress: 42})
		case "/speak":
			var req struct{ Text string }
			json.NewDecoder(r.Body).Decode(&req)
			if req.Text != "Hello there." {
				t.Errorf("texto %q", req.Text)
			}
			for chunk := range 3 { // 3 pedaços de 0,05 s
				writeFrame(w, 'A', pcm(1200, int16(chunk+1)))
			}
			writeFrame(w, 'F', nil)
		}
	}))
	defer srv.Close()
	v := NewVoice(t.TempDir())
	v.Endpoint = srv.URL
	if h := v.Health(); h.State != "baixando" || h.Progress != 42 {
		t.Fatalf("health %+v", h)
	}
	var got []int16
	if err := v.Speak(context.Background(), "Hello there.", func(c []int16) { got = append(got, c...) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3600 || got[0] != 1 || got[3599] != 3 {
		t.Fatalf("áudio recebido: %d amostras", len(got))
	}

	if v.HasSample() {
		t.Fatal("ainda sem amostra")
	}
	if err := v.SaveSample(make([]int16, 24000), 24000, "  Hi everyone.\n"); err != nil {
		t.Fatal(err)
	}
	if !v.HasSample() || v.SampleText() != "Hi everyone." {
		t.Fatalf("amostra: %v %q", v.HasSample(), v.SampleText())
	}
	if v.Installed() {
		t.Fatal("sem o ambiente instalado")
	}
}

func TestTrimSilence(t *testing.T) {
	at48 := func(samples []int16) []float32 {
		out := make([]float32, 0, 3*len(samples))
		for _, s := range samples {
			f := float32(s) / 32768
			out = append(out, f, f, f)
		}
		return out
	}
	// 1 s de silêncio, 3 s de fala, 2 s de silêncio
	audio := at48(append(append(tone(1, false), tone(3, true)...), tone(2, false)...))
	trimmed := trimSilence(audio)
	if d := float64(len(trimmed)) / RouterRate; d < 3.2 || d > 3.6 {
		t.Fatalf("depois de tirar o silêncio: %.2fs", d)
	}
	rec := newRecording(trimmed)
	if d := rec.Seconds(); d < 3.2 || d > 3.6 || len(rec.At24k) != len(trimmed)/2 {
		t.Fatalf("gravação: %.2fs, %d amostras a 24 kHz", d, len(rec.At24k))
	}
}

func TestPickReferencesUsesWholeSentences(t *testing.T) {
	// Frases de 4 s com 0,5 s entre elas; da 6ª em diante, pausas longas
	var segs []segment
	at := 0.0
	for i := range 12 {
		gap := 0.5
		if i >= 6 {
			gap = 3
		}
		segs = append(segs, segment{at, at + 4, fmt.Sprintf("Sentence %d.", i+1)})
		at += 4 + gap
	}
	allSpeech := func(start, end float64) float64 { return 1 }
	refs := pickReferences(segs, 4, allSpeech, nil)
	if len(refs) == 0 || len(refs) > 4 {
		t.Fatalf("trechos: %d", len(refs))
	}
	for k, r := range refs {
		dur := r.end - r.start
		if dur < minRefSeconds || dur > maxRefSeconds {
			t.Fatalf("trecho %d com %.1fs", k, dur)
		}
		if k > 0 && r.start < refs[k-1].end {
			t.Fatalf("trechos sobrepostos: %+v", refs)
		}
		// O texto é o das frases inteiras do trecho
		if !strings.HasPrefix(r.text, "Sentence ") || !strings.HasSuffix(r.text, ".") {
			t.Fatalf("texto %q", r.text)
		}
	}
	// O melhor trecho é contínuo (sem as pausas longas do fim)
	if refs[0].start >= 27 {
		t.Fatalf("o primeiro trecho deveria vir da parte sem pausas: %+v", refs[0])
	}
}

func TestPickReferencesPrefersWholeKnownSentences(t *testing.T) {
	known := words(CalibrationText)
	segs := []segment{
		{0, 5, "Hi everyone, thanks for joining."},
		{5.2, 10, "Let me give you a quick update on what I've been working on"},
		{10.1, 11.5, "this week."},
		{11.7, 16, "I have a couple of ideas to make it faster, and I'd like to"},
		{16.1, 19, "discuss them with you after the scowl."},
		{19.2, 23, "Do you and your owner have any questions so far?"},
	}
	refs := pickReferences(segs, 1, func(float64, float64) float64 { return 1 }, known)
	if len(refs) != 1 || refs[0].start != 0 || refs[0].end != 11.5 {
		t.Fatalf("esperava o trecho de frases inteiras e sem erros (0 a 11,5 s), veio %+v", refs)
	}
	if refs[0].text != "Hi everyone, thanks for joining. Let me give you a quick update on what I've been working on this week." {
		t.Fatalf("texto %q", refs[0].text)
	}
	if got := joinText([]string{"in the stage environment", " .", "Okay ?"}); got != "in the stage environment. Okay?" {
		t.Fatalf("joinText: %q", got)
	}
}

func TestCalibrateBuildsOptionsAndChooses(t *testing.T) {
	// Whisper falso: frases de 5 s; voz falsa: 0,1 s de áudio por fala
	var refsAsked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/inference":
			var segs []map[string]any
			for i := range 8 {
				segs = append(segs, map[string]any{"start": float64(i) * 5, "end": float64(i)*5 + 4.8, "text": fmt.Sprintf(" Hi everyone, thanks for joining %d.", i+1)})
			}
			json.NewEncoder(w).Encode(map[string]any{"text": "...", "segments": segs})
		case "/speak":
			var req map[string]string
			json.NewDecoder(r.Body).Decode(&req)
			if req["text"] != PreviewText {
				t.Errorf("frase de teste %q", req["text"])
			}
			ref := ""
			if req["ref_audio"] != "" {
				ref = filepath.Base(req["ref_audio"])
			}
			refsAsked = append(refsAsked, ref+"|"+req["ref_text"])
			writeFrame(w, 'A', pcm(2400, 0))
			writeFrame(w, 'F', nil)
		}
	}))
	defer srv.Close()
	v := NewVoice(t.TempDir())
	v.Endpoint = srv.URL
	v.SaveSample(make([]int16, 24000), 24000, "My current sample.")
	original, _ := os.ReadFile(filepath.Join(v.Dir, "amostra.wav"))

	audio := make([]float32, 40*RouterRate)
	for i := range audio {
		audio[i] = float32(0.3 * math.Sin(float64(i)*2*math.Pi*220/RouterRate))
	}
	rec := newRecording(audio)
	var steps []string
	options, err := Calibrate(context.Background(), rec, srv.URL, v, func(s string) { steps = append(steps, s) })
	if err != nil {
		t.Fatal(err)
	}
	if len(options) < 2 || options[0].ID != "atual" || options[0].Text != "My current sample." {
		t.Fatalf("opções: %+v", options)
	}
	if refsAsked[0] != "|" {
		t.Fatalf("a voz atual usa a amostra salva, veio %q", refsAsked[0])
	}
	for k, o := range options[1:] {
		if refsAsked[k+1] != o.Reference+"|"+o.Text || !strings.HasPrefix(o.Text, "Hi everyone") {
			t.Fatalf("opção %d pediu %q", k+1, refsAsked[k+1])
		}
		if o.Seconds < minRefSeconds || o.Seconds > maxRefSeconds+0.5 {
			t.Fatalf("trecho da opção %d com %.1fs", k+1, o.Seconds)
		}
		for _, f := range []string{o.Reference, o.Preview} {
			if _, err := os.Stat(filepath.Join(v.CalibrationDir(), f)); err != nil {
				t.Fatalf("arquivo %s: %v", f, err)
			}
		}
	}
	if len(steps) == 0 || !strings.Contains(steps[len(steps)-1], "Gerando a voz") {
		t.Fatalf("andamento: %q", steps)
	}

	// Escolher uma opção troca a amostra; a lista sobrevive a reabrir o app
	if len(v.CalibrationOptions()) != len(options) {
		t.Fatal("as opções deveriam ficar salvas")
	}
	if err := v.ChooseOption(options[1].ID); err != nil {
		t.Fatal(err)
	}
	if v.SampleText() != options[1].Text {
		t.Fatalf("amostra depois de escolher: %q", v.SampleText())
	}
	if err := v.ChooseOption("opcao-99"); err == nil {
		t.Fatal("opção inexistente deveria dar erro")
	}

	// Arrependeu-se: "Voz atual" traz de volta a amostra de antes da escolha
	// (ela fica guardada com as opções, com o arquivo e o texto)
	if options[0].Reference != "atual.wav" {
		t.Fatalf("a opção atual deveria apontar para a amostra guardada: %+v", options[0])
	}
	if err := v.ChooseOption("atual"); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(filepath.Join(v.Dir, "amostra.wav"))
	if v.SampleText() != "My current sample." || !bytes.Equal(restored, original) {
		t.Fatalf("a voz atual deveria voltar: texto %q, %d bytes (a original tinha %d)", v.SampleText(), len(restored), len(original))
	}
}
