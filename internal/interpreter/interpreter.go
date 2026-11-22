// Package interpreter é o Intérprete do Recorder: enquanto você segura o
// atalho (push-to-talk), a sua voz deixa de ir para a reunião; o que você diz
// em português é traduzido e vai para a reunião em inglês, com a sua voz.
//
// Caminho de cada frase, todo neste Mac:
//
//	microfone -> frases (pausas) -> Whisper (português e a tradução literal)
//	-> correção pelo Claude Haiku ou Ollama (corrector.go) -> servidor de voz
//	(sua voz clonada) -> microfone virtual (BlackHole)
//
// O atraso que a reunião sente é do fim da sua fala ao começo do inglês.
// Para ele ser o menor possível sem mudar o que é dito:
//
//   - entender uma frase (Whisper e correção) não espera a anterior acabar de
//     ser falada: cada frase é entendida assim que chega (understand) e só a
//     fala segue a ordem (worker);
//   - o Whisper começa com earlySilence de pausa, sem esperar ela se
//     confirmar: o áudio é o mesmo que seria cortado (segment.go);
//   - conferir se o que chegou é inglês (o retorno da tradução) roda junto
//     com a correção, e não antes de tudo;
//   - o tempo de cada etapa vai para o log.
//
// Todo aperto que não vira inglês diz por quê na tela (Status.Message): sem
// isso, uma frase perdida parecia uma falha aleatória.
//
// O Meet usa o BlackHole como microfone. Fora do push-to-talk, o roteador
// (router.go) repassa o seu microfone para ele com atraso mínimo.
package interpreter

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Output é para onde vai o áudio: o roteador, nos testes um falso.
type Output interface {
	// SetTalking liga o push-to-talk: a sua voz deixa de ir para a reunião
	SetTalking(on bool)
	// SetBusy mantém a sua voz fora da reunião enquanto há frases sendo
	// traduzidas: os outros ouvem o inglês inteiro, sem você por cima
	SetBusy(on bool)
	// Enqueue manda à reunião (e ao fone) a fala em inglês, a 24 kHz
	Enqueue(speech []int16)
	// Pending é quanto inglês ainda falta tocar
	Pending() time.Duration
}

// Status é o que a tela mostra.
type Status struct {
	State   string  `json:"state"`   // "pronto", "ouvindo", "traduzindo", "falando" ou "erro"
	Message string  `json:"message"` // erro ou aviso do último aperto
	PT      string  `json:"pt"`      // última frase em português
	EN      string  `json:"en"`      // e em inglês
	Literal string  `json:"literal"` // a tradução do Whisper, se a correção mudou algo
	Delay   float64 `json:"delay"`   // segundos entre parar de falar e o inglês começar
}

// Config são os serviços usados.
type Config struct {
	WhisperEndpoint string
	// Correctors corrigem a tradução literal do Whisper, em ordem de
	// preferência; sem nenhum (ou se nenhum responder), vai a literal
	Correctors []Corrector
	Voice      interface {
		Speak(ctx context.Context, text string, onChunk func([]int16)) error
	}
	OnStatus func(Status)
	// Limits são os prazos de cada frase; o que ficar em zero vale o padrão
	Limits Limits
	// KeepDir, se não for vazio, é a pasta onde fica o áudio das últimas
	// keptPhrases frases que foram para o Whisper. Quando uma tradução sai com
	// o que você não disse, dá para ouvir o que o microfone captou naquele
	// aperto (o log diz o arquivo de cada frase)
	KeepDir string
}

// keptPhrases é quantas frases ficam guardadas em Config.KeepDir.
const keptPhrases = 20

// Limits são os prazos de cada frase. Enquanto uma frase é traduzida, a sua
// voz fica fora da reunião (SetBusy): sem prazo, uma etapa travada (a rede
// caiu no meio da correção, a voz parou de responder) deixava você mudo na
// reunião até desligar o Intérprete. Estourou o prazo, a frase é abandonada
// com um aviso na tela e a sua voz volta.
type Limits struct {
	// Understand é o prazo para entender a frase: o Whisper e a correção
	// (o normal são 2-5 s)
	Understand time.Duration
	// Correct é o prazo de cada corretor; depois dele vai o próximo ou a
	// tradução literal (o Haiku leva 1,5-3 s)
	Correct time.Duration
	// Cooldown é por quanto tempo um corretor que falhou fica de fora: sem
	// rede, cada frase esperaria o prazo inteiro dele antes de ir literal
	Cooldown time.Duration
	// VoiceStall é o máximo que a voz pode ficar sem mandar áudio (o começo
	// da fala leva até uns 4 s)
	VoiceStall time.Duration
	// EchoTail é por quanto tempo depois de o inglês acabar de tocar uma
	// frase ainda pode ser o retorno dele (a sala, o atraso do alto-falante)
	EchoTail time.Duration
}

func (l Limits) withDefaults() Limits {
	if l.Understand == 0 {
		l.Understand = 30 * time.Second
	}
	if l.Correct == 0 {
		l.Correct = 8 * time.Second
	}
	if l.Cooldown == 0 {
		l.Cooldown = 30 * time.Second
	}
	if l.VoiceStall == 0 {
		l.VoiceStall = 15 * time.Second
	}
	if l.EchoTail == 0 {
		l.EchoTail = 2 * time.Second
	}
	return l
}

// englishSure é a certeza a partir da qual uma frase é tida como inglês.
const englishSure = 0.7

// utterance é uma frase: o áudio dela, o que foi entendido (res, pronto
// quando ready fecha) e os momentos de que o resto depende.
type utterance struct {
	audio    []int16
	started  time.Time // quando a frase começou a ser gravada
	recorded time.Time // até quando ela foi gravada
	ended    time.Time // quando você parou de falar (ou soltou o atalho): daqui conta o atraso

	ctx    context.Context // o prazo para entender a frase; cancelado se ela for descartada
	cancel context.CancelFunc
	// confirmed fecha quando a frase é mesmo uma frase: a pausa se confirmou
	// ou o atalho foi solto. Antes disso ela só foi adiantada (segEarly), e a
	// fala pode voltar
	confirmed chan struct{}
	ready     chan struct{}
	res       understood
}

// understood é o que saiu de entender uma frase: o inglês a falar ou por que
// ela não vira inglês.
type understood struct {
	pt, literal, en string
	warning         string // um corretor falhou e outro (ou nenhum) corrigiu
	notice          string // por que a frase não vira inglês (não era fala, era o retorno...)
	failure         error  // o Whisper falhou ou não respondeu a tempo
	abandoned       bool   // o Intérprete foi desligado, ou a frase adiantada foi descartada

	// Quanto levou cada etapa, para o log
	lane, heard, translated, language, corrected time.Duration
	readyAt                                      time.Time
}

// spoken é uma tradução que foi para a reunião e quando ela tocou.
type spoken struct {
	text        string
	from, until time.Time
}

// Interpreter liga o microfone, a tradução e a voz.
type Interpreter struct {
	cfg    Config
	limits Limits
	out    Output

	ctx     context.Context // termina no Close: a frase em andamento é abandonada
	cancel  context.CancelFunc
	working sync.WaitGroup // o worker
	closing sync.Once

	mu         sync.Mutex
	talking    bool
	seg        *Segmenter
	segStart   time.Time  // quando começou a frase em andamento no seg
	spec       *utterance // a frase em andamento, já sendo entendida desde a pausa (segEarly)
	sent       int        // frases mandadas para a tradução neste aperto
	correctors []Corrector
	down       map[Corrector]time.Time // corretor que falhou -> até quando fica de fora
	busy       int                     // frases na fila ou sendo traduzidas
	last       Status
	recent     []spoken // inglês dito há pouco, para não traduzir o próprio retorno
	queue      chan *utterance

	// O whisper-server atende um pedido de cada vez. Os pedidos de uma frase
	// vão juntos, na ordem em que as frases chegaram: sem isso, os de uma frase
	// nova passariam na frente do resto da anterior, que é a que vai ser
	// falada primeiro
	lane chan struct{}
}

func New(cfg Config, out Output) *Interpreter {
	ctx, cancel := context.WithCancel(context.Background())
	i := &Interpreter{cfg: cfg, limits: cfg.Limits.withDefaults(), out: out, ctx: ctx, cancel: cancel,
		queue: make(chan *utterance, 32), last: Status{State: "pronto"}, correctors: cfg.Correctors,
		down: map[Corrector]time.Time{}, lane: make(chan struct{}, 1)}
	i.working.Add(1)
	go i.worker()
	go i.watch()
	return i
}

// Close para o intérprete e espera as frases em andamento serem abandonadas
// (os pedidos ao Whisper, ao corretor e à voz são cancelados): depois dele,
// nada mais usa os corretores nem a saída.
func (i *Interpreter) Close() {
	i.closing.Do(func() {
		// Com a trava: depois daqui nenhuma frase nova começa a ser entendida
		i.mu.Lock()
		i.cancel()
		i.mu.Unlock()
		i.working.Wait()
	})
}

// Talk liga (apertou o atalho) ou desliga (soltou) o push-to-talk.
func (i *Interpreter) Talk(on bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if on == i.talking {
		return // o atalho repete enquanto está apertado
	}
	i.talking = on
	if on {
		i.seg, i.sent, i.segStart = NewSegmenter(), 0, time.Now()
		i.dropSpecLocked()
		i.last.Message = "" // o aviso do aperto anterior já foi visto
		i.out.SetTalking(true)
		return
	}
	// Soltou: a frase em andamento vai para a tradução; a sua voz continua
	// fora da reunião até o inglês acabar (SetBusy)
	if u := i.seg.Flush(); u != nil {
		i.enqueueLocked(u, time.Now())
	}
	i.dropSpecLocked() // o que foi adiantado e não virou frase
	if i.sent == 0 {
		i.last.Message = nothingHeard(i.seg.Heard())
		log.Printf("Intérprete: aperto sem fala (volume máximo %.0f dB)", i.seg.Heard())
	}
	i.seg = nil
	i.out.SetTalking(false)
}

// nothingHeard explica um aperto em que não chegou fala nenhuma.
func nothingHeard(heardDB float64) string {
	if heardDB <= minVoiceDB {
		return "Não chegou som do microfone. Confira a entrada em Ajustes do Sistema > Som."
	}
	return "Não ouvi fala: segure o atalho enquanto fala e solte no fim da frase."
}

// SetCorrectors troca os corretores (o usuário mudou a escolha).
func (i *Interpreter) SetCorrectors(cs []Corrector) {
	i.mu.Lock()
	i.correctors = cs
	i.down = map[Corrector]time.Time{}
	i.mu.Unlock()
}

// Feed recebe o áudio do microfone (16 kHz) enquanto o atalho está apertado.
func (i *Interpreter) Feed(samples []int16) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.talking || i.seg == nil {
		return
	}
	for _, ev := range i.seg.feed(samples) {
		now := time.Now()
		switch ev.kind {
		case segPhrase:
			// A frase terminou com endSilence de pausa
			i.enqueueLocked(ev.audio, now.Add(-seconds(endSilence)))
		case segEarly:
			// earlySilence de pausa: o Whisper já começa, com o áudio que a
			// frase vai ter se a pausa se confirmar
			i.dropSpecLocked()
			i.spec = i.understandLocked(ev.audio, now.Add(-seconds(earlySilence)))
		case segResumed:
			i.dropSpecLocked() // era só uma pausa no meio da frase
		}
	}
}

func seconds(s float64) time.Duration {
	return time.Duration(s * float64(time.Second))
}

// dropSpecLocked descarta a frase adiantada: a fala voltou, ou a frase
// terminou diferente do que tinha sido adiantado.
func (i *Interpreter) dropSpecLocked() {
	if i.spec != nil {
		i.spec.cancel()
		i.spec = nil
	}
}

// understandLocked começa a entender uma frase (understand) e a devolve; nil
// se o Intérprete já foi desligado. recorded é até quando ela foi gravada.
func (i *Interpreter) understandLocked(audio []int16, recorded time.Time) *utterance {
	if i.ctx.Err() != nil {
		return nil
	}
	// O prazo para entender a frase (Limits.Understand)
	ctx, cancel := context.WithTimeout(i.ctx, i.limits.Understand)
	u := &utterance{audio: audio, started: i.segStart, recorded: recorded, ctx: ctx, cancel: cancel,
		confirmed: make(chan struct{}), ready: make(chan struct{})}
	i.working.Add(1)
	go i.understand(u)
	return u
}

// enqueueLocked põe na fila da fala uma frase que terminou: a que já vinha
// sendo entendida desde a pausa, se o áudio é o mesmo, ou uma nova.
func (i *Interpreter) enqueueLocked(audio []int16, ended time.Time) {
	u := i.spec
	i.spec = nil
	if u != nil && !slices.Equal(u.audio, audio) {
		u.cancel()
		u = nil
	}
	if u == nil {
		if u = i.understandLocked(audio, ended); u == nil {
			return
		}
	}
	u.ended = ended
	go i.keep(u.audio, ended)
	i.segStart = time.Now() // o que vier agora já é da próxima frase
	i.sent++
	i.busy++
	i.out.SetBusy(true)
	select {
	case i.queue <- u:
		close(u.confirmed)
	default:
		u.cancel()
		i.busy--
		if i.busy == 0 {
			i.out.SetBusy(false)
		}
		i.last.Message = "Frases demais esperando tradução: uma foi descartada."
		log.Printf("Intérprete: fila cheia, frase descartada")
	}
}

// keep guarda em Config.KeepDir o áudio de uma frase que foi para o Whisper e
// apaga as mais antigas.
func (i *Interpreter) keep(audio []int16, ended time.Time) {
	dir := i.cfg.KeepDir
	if dir == "" {
		return
	}
	name := ended.Format("2006-01-02_15-04-05.000") + ".wav"
	err := os.MkdirAll(dir, 0755)
	if err == nil {
		err = writeWav(filepath.Join(dir, name), audio, WhisperRate)
	}
	if err != nil {
		log.Printf("Intérprete: não foi possível guardar o áudio da frase: %v", err)
		return
	}
	log.Printf("Intérprete: frase de %.1f s (%.0f dB) guardada em %s", float64(len(audio))/WhisperRate, levelDB(audio), filepath.Join(dir, name))
	kept, _ := filepath.Glob(filepath.Join(dir, "*.wav"))
	slices.Sort(kept) // o nome é a hora: as mais antigas primeiro
	for len(kept) > keptPhrases {
		os.Remove(kept[0])
		kept = kept[1:]
	}
}

func (i *Interpreter) done() {
	i.mu.Lock()
	i.busy--
	if i.busy == 0 {
		i.out.SetBusy(false)
	}
	i.mu.Unlock()
}

// worker fala as frases, na ordem em que foram ditas, conforme cada uma
// termina de ser entendida.
func (i *Interpreter) worker() {
	defer i.working.Done()
	for {
		select {
		case <-i.ctx.Done():
			return
		case u := <-i.queue:
			select {
			case <-u.ready:
				i.speak(u)
			case <-i.ctx.Done():
			}
			u.cancel()
			i.done()
		}
	}
}

func (i *Interpreter) setLast(update func(*Status)) {
	i.mu.Lock()
	update(&i.last)
	i.mu.Unlock()
}

// understand transcreve, traduz e corrige a frase u, e deixa o resultado em
// u.res. Roda assim que a frase chega (ou é adiantada por uma pausa), sem
// esperar as anteriores serem faladas.
func (i *Interpreter) understand(u *utterance) {
	defer i.working.Done()
	defer close(u.ready)
	u.res = i.understood(u)
	u.res.readyAt = time.Now()
}

func (i *Interpreter) understood(u *utterance) (res understood) {
	whisper, ctx := i.cfg.WhisperEndpoint, u.ctx
	length := float64(len(u.audio)) / WhisperRate
	// failed é a falha do Whisper; se a frase foi descartada (ou o Intérprete
	// desligado), ninguém está esperando por ela
	failed := func(err error) understood {
		if errors.Is(ctx.Err(), context.Canceled) {
			return understood{abandoned: true}
		}
		res.failure = err
		return res
	}

	asked := time.Now()
	select {
	case i.lane <- struct{}{}:
	case <-ctx.Done():
		return failed(ctx.Err())
	}
	inLane := true
	leave := func() {
		if inLane {
			inLane = false
			<-i.lane
		}
	}
	defer leave()
	res.lane = time.Since(asked)

	step := time.Now()
	pt, err := transcribePT(ctx, whisper, u.audio, false)
	if err != nil {
		return failed(err)
	}
	res.pt, res.heard = pt, time.Since(step)
	if isHallucination(pt) {
		res.notice = fmt.Sprintf("Não entendi nenhuma fala (%.1f s de áudio).", length)
		return res
	}
	// Só pode ser o retorno da tradução a frase gravada enquanto ela tocava
	// (ou logo depois). Comparar com tudo o que foi dito antes descartava
	// frases novas: o português de reunião repete os termos em inglês
	// ("deploy", "backend", "API") da tradução anterior
	if echo := i.playedDuring(u); len(echo) > 0 && repeats(pt, echo) {
		res.notice = "Ignorei uma frase que repetia a última tradução (o retorno no alto-falante?): " + pt
		return res
	}

	step = time.Now()
	literal, err := transcribePT(ctx, whisper, u.audio, true)
	if err != nil {
		return failed(err)
	}
	res.literal, res.translated = literal, time.Since(step)

	// A correção vai para a rede e não usa a GPU: enquanto ela corre, o
	// Whisper confere o idioma. Uma frase só adiantada, que ainda pode ser
	// descartada, não gasta o corretor antes de se confirmar
	var correction chan corrected
	if isClosed(u.confirmed) {
		correction = i.correctAsync(ctx, pt, literal)
	}

	// O inglês voltando pelo microfone (o retorno no alto-falante) não é
	// traduzido de novo; o retorno sai com ~93% de certeza. Abaixo de
	// englishSure, segue como português: ruído no fim da fala também sai
	// "english", e o filtro de alucinações o descarta. Outros idiomas também
	// passam: numa frase curta, o Whisper às vezes acha que o português é
	// galego ou espanhol. Vale para toda frase, e não só com a tradução
	// tocando: sem fone, o inglês dos outros participantes também chega ao
	// microfone
	step = time.Now()
	lang, err := detectLanguage(ctx, whisper, u.audio)
	if err != nil {
		return failed(err)
	}
	if lang == "english" {
		p, err := englishConfidence(ctx, whisper, u.audio)
		if err != nil {
			return failed(err)
		}
		if p >= englishSure {
			// A correção, se já foi pedida, termina sozinha e é ignorada
			res.notice = fmt.Sprintf("Ouvi inglês e não traduzi (%.1f s). Se não foi você, o microfone captou o som do computador.", length)
			return res
		}
		log.Printf("Intérprete: inglês com %.0f%% de certeza (%.1f s), seguindo como português", p*100, length)
	}
	res.language = time.Since(step)
	leave() // a próxima frase já pode usar o Whisper

	if correction == nil {
		select {
		case <-u.confirmed:
		case <-ctx.Done():
			return failed(ctx.Err())
		}
		correction = i.correctAsync(ctx, pt, literal)
	}
	c := <-correction
	if i.ctx.Err() != nil {
		return understood{abandoned: true} // o Intérprete foi desligado
	}
	res.corrected = c.took
	res.en, res.warning = c.en, c.warning
	if c.err != nil {
		res.en, res.warning = literal, "Sem correção ("+c.err.Error()+"): foi a tradução literal do Whisper"
	}
	if strings.TrimSpace(res.en) == "" {
		res.notice = "O Whisper não traduziu: " + pt
	}
	return res
}

// corrected é a resposta dos corretores para uma frase.
type corrected struct {
	en, warning string
	err         error
	took        time.Duration
}

// correctAsync corrige em segundo plano; a resposta chega no canal. A
// correção tem o prazo da frase (o de ctx), mas não é cancelada junto com
// ela: uma frase descartada no meio (era inglês) deixa a correção terminar
// sozinha. Cancelada, ela derrubaria o processo do corretor e o tiraria de
// uso, e a frase seguinte iria sem correção.
func (i *Interpreter) correctAsync(ctx context.Context, pt, literal string) chan corrected {
	out := make(chan corrected, 1)
	deadline, _ := ctx.Deadline()
	i.working.Add(1)
	go func() {
		defer i.working.Done()
		ctx, cancel := context.WithDeadline(i.ctx, deadline)
		defer cancel()
		started := time.Now()
		en, warning, err := i.correct(ctx, pt, literal)
		out <- corrected{en, warning, err, time.Since(started)}
	}()
	return out
}

func isClosed(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// speak manda para a reunião a frase u, já entendida, ou mostra por que ela
// não virou inglês.
func (i *Interpreter) speak(u *utterance) {
	res := u.res
	switch {
	case res.abandoned:
		return
	case res.failure != nil:
		i.whisperFailed(res.failure)
		return
	case res.notice != "":
		i.notice(res.notice)
		return
	}
	pt, en := res.pt, res.en
	status := Status{PT: pt, EN: en, Message: res.warning}
	if en != res.literal {
		status.Literal = res.literal
	}
	i.setLast(func(s *Status) { *s = status })

	// A voz não tem prazo para a fala inteira (uma frase longa leva o tempo
	// dela), mas não pode ficar VoiceStall sem mandar áudio
	speakCtx, stop := context.WithCancel(i.ctx)
	defer stop()
	stall := time.AfterFunc(i.limits.VoiceStall, stop)
	defer stall.Stop()
	asked := time.Now()
	first := true
	err := i.cfg.Voice.Speak(speakCtx, en, func(chunk []int16) {
		stall.Reset(i.limits.VoiceStall)
		if first {
			delay := time.Since(u.ended).Seconds()
			i.setLast(func(s *Status) { s.Delay = delay })
			if status.Literal != "" {
				log.Printf("Intérprete: %.1fs | %s -> %s (literal: %s)", delay, pt, en, status.Literal)
			} else {
				log.Printf("Intérprete: %.1fs | %s -> %s", delay, pt, en)
			}
			// Onde o tempo foi: "esperou" é a fila do Whisper mais a fala das
			// frases anteriores
			waited := res.lane + asked.Sub(res.readyAt)
			log.Printf("Intérprete: etapas: transcrição %.2fs, tradução %.2fs, correção %.2fs (idioma %.2fs, junto), voz %.2fs, esperou %.2fs",
				res.heard.Seconds(), res.translated.Seconds(), res.corrected.Seconds(), res.language.Seconds(),
				time.Since(asked).Seconds(), waited.Seconds())
		}
		i.out.Enqueue(chunk)
		i.played(en, first)
		first = false
	})
	if i.ctx.Err() != nil {
		return
	}
	if err != nil {
		if speakCtx.Err() != nil {
			err = fmt.Errorf("ficou %v sem mandar áudio", i.limits.VoiceStall)
		}
		if first {
			i.fail(fmt.Errorf("a voz falhou: %w", err))
		} else {
			i.fail(fmt.Errorf("a voz falhou no meio da frase (a reunião ouviu só o começo): %w", err))
		}
		return
	}
	if first {
		i.notice("A voz não gerou áudio para: " + en)
	}
}

// played registra que mais um pedaço da tradução text foi para a reunião:
// ela toca de agora (o primeiro pedaço) até acabar o que está na fila.
func (i *Interpreter) played(text string, first bool) {
	now := time.Now()
	until := now.Add(i.out.Pending())
	i.mu.Lock()
	defer i.mu.Unlock()
	if first || len(i.recent) == 0 {
		i.recent = append(i.recent, spoken{text: text, from: now})
		if len(i.recent) > 5 {
			i.recent = i.recent[1:]
		}
	}
	i.recent[len(i.recent)-1].until = until
}

// playedDuring devolve as traduções que tocaram enquanto a frase u era
// gravada, ou até EchoTail antes de ela começar.
func (i *Interpreter) playedDuring(u *utterance) []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	end := u.recorded
	if end.Before(u.started) {
		end = u.started
	}
	var texts []string
	for _, s := range i.recent {
		if u.started.Before(s.until.Add(i.limits.EchoTail)) && !end.Before(s.from) {
			texts = append(texts, s.text)
		}
	}
	return texts
}

// correct tenta os corretores em ordem e devolve a correção (sem corretores,
// en como veio). warning avisa quando um falhou e outro corrigiu; err, quando
// nenhum corrigiu. Cada corretor tem Limits.Correct para responder, e o que
// falha fica Limits.Cooldown de fora: sem rede, só a primeira frase espera.
func (i *Interpreter) correct(ctx context.Context, pt, en string) (fixed, warning string, err error) {
	// Os corretores de agora: o usuário pode ter trocado enquanto o Whisper
	// transcrevia (os antigos já foram fechados)
	i.mu.Lock()
	cs := i.correctors
	i.mu.Unlock()
	var failed []string
	for _, c := range cs {
		if ctx.Err() != nil {
			// O prazo da frase acabou antes da vez deste: não é culpa dele
			failed = append(failed, "sem tempo para o "+c.Name())
			continue
		}
		if i.coolingDown(c) {
			failed = append(failed, c.Name()+" indisponível")
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, i.limits.Correct)
		fixed, err := c.Correct(cctx, pt, en)
		slow := cctx.Err() != nil
		cancel()
		if err == nil {
			if len(failed) > 0 {
				warning = strings.Join(failed, "; ") + ": corrigido pelo " + c.Name()
			}
			return fixed, warning, nil
		}
		if i.ctx.Err() != nil {
			return "", "", err // o Intérprete foi desligado
		}
		switch {
		case errors.Is(err, errImplausible):
			// Respondeu, só que mal: vale para esta frase, e não como fora do ar
			log.Printf("Intérprete: correção do %s: %v", c.Name(), err)
			failed = append(failed, "resposta do "+c.Name()+" descartada")
		case slow:
			log.Printf("Intérprete: o %s não respondeu em %v; fica %v de fora", c.Name(), i.limits.Correct, i.limits.Cooldown)
			i.trip(c)
			failed = append(failed, c.Name()+" indisponível")
		default:
			log.Printf("Intérprete: correção do %s: %v; fica %v de fora", c.Name(), err, i.limits.Cooldown)
			i.trip(c)
			failed = append(failed, c.Name()+" indisponível")
		}
	}
	if len(failed) > 0 {
		return "", "", fmt.Errorf("%s", strings.Join(failed, "; "))
	}
	return en, "", nil
}

// coolingDown diz se o corretor c falhou há pouco e ainda está de fora.
func (i *Interpreter) coolingDown(c Corrector) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	until, ok := i.down[c]
	if ok && time.Now().Before(until) {
		return true
	}
	delete(i.down, c)
	return false
}

// trip tira o corretor c de uso por Limits.Cooldown. É este corretor, e não
// o nome dele: um que falhou por ter sido trocado (e fechado) no meio da
// frase não tira de uso o novo, do mesmo tipo.
func (i *Interpreter) trip(c Corrector) {
	i.mu.Lock()
	i.down[c] = time.Now().Add(i.limits.Cooldown)
	i.mu.Unlock()
}

// whisperFailed avisa que a frase foi abandonada porque o Whisper falhou ou
// não respondeu a tempo.
func (i *Interpreter) whisperFailed(err error) {
	if i.ctx.Err() != nil {
		return // o Intérprete foi desligado
	}
	if errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("o Whisper não respondeu em %v: a frase foi descartada", i.limits.Understand)
	}
	i.fail(err)
}

func (i *Interpreter) fail(err error) {
	log.Printf("Intérprete: %v", err)
	i.setLast(func(s *Status) { s.Message = err.Error() })
}

// notice avisa por que uma frase não virou inglês.
func (i *Interpreter) notice(msg string) {
	log.Printf("Intérprete: %s", msg)
	i.setLast(func(s *Status) { s.Message = msg })
}

// state diz o que está acontecendo agora.
func (i *Interpreter) state() Status {
	i.mu.Lock()
	s := i.last
	talking, busy := i.talking, i.busy
	i.mu.Unlock()
	switch {
	case talking:
		s.State = "ouvindo"
	case busy > 0:
		s.State = "traduzindo"
	case i.out.Pending() > 0:
		s.State = "falando"
	default:
		s.State = "pronto"
	}
	return s
}

// watch avisa a tela quando o estado muda.
func (i *Interpreter) watch() {
	var last Status
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-i.ctx.Done():
			return
		case <-ticker.C:
			s := i.state()
			if s != last && i.cfg.OnStatus != nil {
				i.cfg.OnStatus(s)
			}
			last = s
		}
	}
}

// Status devolve o estado atual.
func (i *Interpreter) Status() Status {
	return i.state()
}
