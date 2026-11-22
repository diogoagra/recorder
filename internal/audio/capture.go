package audio

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/gen2brain/malgo"
	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
)

const sampleRate = 16000 // Whisper usa 16kHz

// maxSystemBacklog limita quanto áudio do sistema pode ficar esperando para ser
// mixado ao microfone. Os dois dispositivos têm clocks independentes, então
// sem esse limite uma pequena diferença de clock acumularia atraso.
const maxSystemBacklog = sampleRate / 2

// systemSource captura o áudio que o computador está tocando e o entrega como
// amostras mono int16 a 16kHz.
type systemSource interface {
	Start() error
	Close()
}

// frame é um pedaço do microfone e o áudio do sistema que chegou junto com
// ele (nil se nada estava tocando no computador).
type frame struct {
	mic, sys []int16
}

type Capture struct {
	ctx    *malgo.AllocatedContext
	device *malgo.Device
	ch     chan frame

	// Áudio gravado depois do corte do último bloco, que abre o próximo. Só é
	// usado por RecordBlock, que é chamado de uma goroutine só.
	pendingMic, pendingSys []int16

	mu            sync.Mutex // protege isStarted, systemEnabled e system
	isStarted     bool
	systemEnabled bool
	system        systemSource

	sysMu  sync.Mutex
	sysBuf []int16
}

func NewCapture(onAudioLevel func(level float64)) (*Capture, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {})
	if err != nil {
		return nil, fmt.Errorf("falha ao inicializar malgo: %w", err)
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = 1
	deviceConfig.SampleRate = sampleRate
	deviceConfig.Alsa.NoMMap = 1

	// Folga para a gravação seguir enquanto o bloco anterior é finalizado
	c := &Capture{ch: make(chan frame, 2000)}

	onRecvFrames := func(pOutputSample, pInputSamples []byte, framecount uint32) {
		chunk := decodeS16(pInputSamples, int(framecount*deviceConfig.Capture.Channels))

		// O microfone dita o ritmo: cada bloco dele leva junto o áudio do sistema
		// que chegou. A mixagem fica para o fim do bloco, depois de tirar o eco
		sys := c.takeSystemAudio(len(chunk))

		if onAudioLevel != nil && len(chunk) > 0 {
			var sumSq float64
			for i, sample := range chunk {
				if sys != nil {
					sample = clampInt16(int32(sample) + int32(sys[i]))
				}
				sumSq += float64(sample) * float64(sample)
			}
			rms := math.Sqrt(sumSq / float64(len(chunk)))
			// Normaliza o rms (max int16 = 32768)
			level := rms / 32768.0
			onAudioLevel(level)
		}

		select {
		case c.ch <- frame{mic: chunk, sys: sys}:
		default:
			// descarta áudio se o canal estiver cheio (ex: pausado)
		}
	}

	device, err := malgo.InitDevice(ctx.Context, deviceConfig, malgo.DeviceCallbacks{
		Data: onRecvFrames,
	})
	if err != nil {
		ctx.Free()
		return nil, fmt.Errorf("falha ao inicializar dispositivo de audio: %w", err)
	}

	c.ctx = ctx
	c.device = device
	return c, nil
}

func (c *Capture) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closeSystemLocked()
	if c.device != nil {
		c.device.Stop()
		c.device.Uninit()
		c.device = nil
	}
	if c.ctx != nil {
		c.ctx.Free()
		c.ctx = nil
	}
}

// SetSystemAudio liga/desliga a captura do áudio do sistema (o que está tocando
// no computador), que é mixado ao microfone. Se a gravação estiver em
// andamento, a mudança vale imediatamente.
func (c *Capture) SetSystemAudio(enabled bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.systemEnabled = enabled
	if !enabled {
		c.closeSystemLocked()
		return nil
	}
	if c.isStarted {
		return c.startSystemLocked()
	}
	return nil
}

func (c *Capture) startSystemLocked() error {
	if c.system != nil {
		return nil
	}
	sys, err := newSystemSource(c.pushSystemAudio)
	if err != nil {
		return err
	}
	if err := sys.Start(); err != nil {
		sys.Close()
		return err
	}
	c.system = sys
	return nil
}

func (c *Capture) closeSystemLocked() {
	if c.system == nil {
		return
	}
	c.system.Close()
	c.system = nil

	c.sysMu.Lock()
	c.sysBuf = c.sysBuf[:0]
	c.sysMu.Unlock()
}

func (c *Capture) pushSystemAudio(samples []int16) {
	c.sysMu.Lock()
	defer c.sysMu.Unlock()

	c.sysBuf = append(c.sysBuf, samples...)
	if excess := len(c.sysBuf) - maxSystemBacklog; excess > 0 {
		c.sysBuf = c.sysBuf[:copy(c.sysBuf, c.sysBuf[excess:])]
	}
}

// takeSystemAudio tira do buffer o áudio do sistema que acompanha n amostras
// do microfone, completando com silêncio se faltar. Devolve nil se não houver
// nenhum.
func (c *Capture) takeSystemAudio(n int) []int16 {
	c.sysMu.Lock()
	defer c.sysMu.Unlock()

	if len(c.sysBuf) == 0 {
		return nil
	}
	out := make([]int16, n)
	taken := copy(out, c.sysBuf)
	c.sysBuf = c.sysBuf[:copy(c.sysBuf, c.sysBuf[taken:])]
	return out
}

// mixWithSystem junta o microfone e o áudio do sistema (sys é nil se nada
// tocou no computador). Antes, tira do microfone o eco do som do computador
// que ele captou pelos alto-falantes.
func mixWithSystem(mic, sys []int16) []int16 {
	if sys == nil {
		return mic
	}
	sys = append(sys, make([]int16, len(mic)-len(sys))...)
	clean := cancelEcho(mic, sys)
	out := make([]int16, len(mic))
	for i := range out {
		out[i] = clampInt16(int32(clean[i]) + int32(sys[i]))
	}
	return out
}

func (c *Capture) startIfNeeded() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isStarted {
		return nil
	}
	// Descarta o que sobrou no buffer de antes da pausa
	for len(c.ch) > 0 {
		<-c.ch
	}
	if err := c.device.Start(); err != nil {
		return err
	}
	c.isStarted = true

	if c.systemEnabled {
		if err := c.startSystemLocked(); err != nil {
			log.Printf("Aviso: não foi possível capturar o áudio do sistema, gravando apenas o microfone: %v", err)
		}
	}
	return nil
}

func (c *Capture) stopIfNeeded() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isStarted {
		c.closeSystemLocked()
		if c.device != nil {
			c.device.Stop()
		}
		c.isStarted = false
	}
}

// Para não cortar palavras ao meio, o bloco é cortado numa pausa da fala até
// cutSearch antes ou depois do tamanho pedido. O que vem depois do corte abre
// o próximo bloco, então nenhum áudio se perde.
const (
	cutSearch = 5 * time.Second
	cutFrame  = sampleRate / 50 // trechos de 20ms
	cutPause  = 15              // 300ms seguidos de silêncio
)

// RecordBlock grava um bloco de cerca de target e devolve o microfone já
// mixado com o áudio do sistema. A gravação não para entre blocos: o que
// chega enquanto o bloco anterior é transcrito fica no buffer.
func (c *Capture) RecordBlock(ctx context.Context, target time.Duration, outputPath string) ([]int16, error) {
	if err := c.startIfNeeded(); err != nil {
		return nil, fmt.Errorf("falha ao iniciar captura: %w", err)
	}

	mic, sys := c.pendingMic, c.pendingSys
	c.pendingMic, c.pendingSys = nil, nil
	minLen := int((target - cutSearch).Seconds() * sampleRate)
	maxLen := int((target + cutSearch).Seconds() * sampleRate)
	var cutter blockCutter

	// Se o microfone parar de mandar áudio, não espera para sempre
	timer := time.NewTimer(target + cutSearch + 5*time.Second)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			return c.finishBlock(mic, sys, len(mic), outputPath)
		case <-ctx.Done():
			// Cancelamento (botão pause) -> paramo o dispositivo de hardware
			c.stopIfNeeded()
			return mixWithSystem(mic, sys), ctx.Err()
		case f := <-c.ch:
			mic = append(mic, f.mic...)
			if f.sys != nil {
				// Completa com silêncio o trecho anterior em que nada tocava
				sys = append(sys, make([]int16, len(mic)-len(f.mic)-len(sys))...)
				sys = append(sys, f.sys...)
			}
			cutter.update(mic, sys)
			if cut, ok := cutter.cut(minLen, maxLen, len(mic)); ok {
				// NÃO PARAMOS O DISPOSITIVO AQUI! Ele continua quente para o próximo bloco.
				return c.finishBlock(mic, sys, cut, outputPath)
			}
		}
	}
}

// SplitBlocks divide um áudio já gravado em blocos de cerca de target,
// cortados nas pausas da fala do mesmo jeito que na gravação ao vivo.
func SplitBlocks(samples []int16, target time.Duration) [][]int16 {
	minLen := int((target - cutSearch).Seconds() * sampleRate)
	maxLen := int((target + cutSearch).Seconds() * sampleRate)
	var blocks [][]int16
	for len(samples) > maxLen {
		// Percorre o áudio como se ele estivesse chegando, até achar o corte
		var cutter blockCutter
		cut := maxLen
		for end := minLen; end <= maxLen; end += cutFrame {
			cutter.update(samples[:end], nil)
			if c, ok := cutter.cut(minLen, maxLen, end); ok {
				cut = c
				break
			}
		}
		blocks = append(blocks, samples[:cut])
		samples = samples[cut:]
	}
	if len(samples) > 0 {
		blocks = append(blocks, samples)
	}
	return blocks
}

// DiscardPending descarta o áudio guardado para o próximo bloco (ex: quando a
// gravação passa para outra sessão).
func (c *Capture) DiscardPending() {
	c.pendingMic, c.pendingSys = nil, nil
}

// finishBlock fecha o bloco em cut e guarda o resto para o próximo.
func (c *Capture) finishBlock(mic, sys []int16, cut int, outputPath string) ([]int16, error) {
	c.pendingMic = append([]int16(nil), mic[cut:]...)
	if sys != nil {
		sys = append(sys, make([]int16, len(mic)-len(sys))...)
		c.pendingSys = append([]int16(nil), sys[cut:]...)
		sys = sys[:cut]
	}
	samples := mixWithSystem(mic[:cut], sys)
	return samples, SaveWav(outputPath, samples, sampleRate)
}

// blockCutter acompanha o volume do bloco em trechos de 20ms para achar uma
// pausa da fala onde cortar.
type blockCutter struct {
	energy []float64 // RMS de cada trecho (microfone + sistema)
}

func (b *blockCutter) update(mic, sys []int16) {
	for start := len(b.energy) * cutFrame; start+cutFrame <= len(mic); start += cutFrame {
		var sum float64
		for i := start; i < start+cutFrame; i++ {
			v := float64(mic[i])
			if i < len(sys) {
				v += float64(sys[i])
			}
			sum += v * v
		}
		b.energy = append(b.energy, math.Sqrt(sum/cutFrame))
	}
}

// cut devolve onde cortar (em amostras), se já houver um bom lugar: a primeira
// pausa clara depois de minLen ou, ao chegar a maxLen, o trecho mais baixo.
func (b *blockCutter) cut(minLen, maxLen, length int) (int, bool) {
	first := minLen / cutFrame
	n := len(b.energy)
	if n < first+cutPause {
		return 0, false
	}

	// Pausa clara: os últimos 300ms perto do ruído de fundo do bloco e bem
	// abaixo do volume típico dele. Sem a segunda condição, numa fala contínua
	// o "ruído de fundo" seria a própria fala e tudo pareceria pausa
	sorted := append([]float64(nil), b.energy...)
	sort.Float64s(sorted)
	threshold := min(2.5*sorted[len(sorted)/10], 0.3*sorted[len(sorted)/2]) + 1
	quiet := true
	for _, e := range b.energy[n-cutPause:] {
		if e > threshold {
			quiet = false
			break
		}
	}
	if quiet {
		return (n - cutPause/2) * cutFrame, true
	}
	if length < maxLen {
		return 0, false
	}

	best, bestSum := first, math.Inf(1)
	for start := first; start+cutPause <= n; start++ {
		var sum float64
		for _, e := range b.energy[start : start+cutPause] {
			sum += e
		}
		if sum < bestSum {
			best, bestSum = start, sum
		}
	}
	return (best + cutPause/2) * cutFrame, true
}

// LoadWav lê um WAV mono de 16 bits a 16kHz, o formato gravado por SaveWav.
func LoadWav(path string) ([]int16, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	d := wav.NewDecoder(f)
	if !d.IsValidFile() {
		return nil, fmt.Errorf("arquivo WAV inválido: %s", path)
	}
	if d.NumChans != 1 || d.SampleRate != sampleRate || d.BitDepth != 16 {
		return nil, fmt.Errorf("formato de áudio não suportado em %s: %d canais, %dHz, %d bits",
			path, d.NumChans, d.SampleRate, d.BitDepth)
	}

	// Lê em pedaços: o buffer do go-audio usa 8 bytes por amostra, o que
	// pesaria numa sessão de horas
	samples := make([]int16, 0, d.PCMLen()/2)
	buf := &audio.IntBuffer{Data: make([]int, sampleRate), Format: d.Format()}
	for {
		n, err := d.PCMBuffer(buf)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return samples, nil
		}
		for _, s := range buf.Data[:n] {
			samples = append(samples, int16(s))
		}
	}
}

func decodeS16(in []byte, count int) []int16 {
	count = min(count, len(in)/2)
	out := make([]int16, count)
	for i := range out {
		out[i] = int16(in[2*i]) | int16(in[2*i+1])<<8
	}
	return out
}

func clampInt16(v int32) int16 {
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return int16(v)
}
