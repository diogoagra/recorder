package interpreter

import (
	"sync"
	"time"
)

// Limites do repasse do microfone para a reunião. Os dois dispositivos têm
// relógios independentes: sem um teto, uma pequena diferença acumularia atraso.
const (
	maxPassBacklog  = RouterRate / 10      // 100 ms
	passBacklogKeep = RouterRate * 3 / 100 // volta para 30 ms
)

// mixer decide, a cada pedaço de áudio, o que vai para a reunião: a sua voz
// (repasse do microfone), silêncio ou o inglês. É a parte do roteador que não
// depende dos dispositivos.
type mixer struct {
	mu      sync.Mutex
	pass    []float32 // microfone a repassar para a reunião (48 kHz)
	speech  []float32 // inglês para a reunião (48 kHz)
	monitor []float32 // o mesmo inglês, para o seu fone
	rest    []float32 // resto do microfone que não completou 3 amostras (16 kHz)
	talking bool
	busy    bool
	prev    float32 // última amostra do inglês, para emendar os pedaços
}

// muted diz se a sua voz está fora da reunião: com o atalho apertado,
// enquanto há frases sendo traduzidas e até o inglês acabar de tocar.
func (m *mixer) mutedLocked() bool {
	return m.talking || m.busy || len(m.speech) > 0
}

// micIn recebe o microfone (48 kHz). Com o atalho apertado, devolve o mesmo
// áudio a 16 kHz, para a tradução; senão, guarda para o repasse.
func (m *mixer) micIn(in []float32) (talk []int16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.talking {
		buf := append(m.rest, in...)
		n := len(buf) / 3 * 3
		talk = downsample3(buf[:n])
		m.rest = append(m.rest[:0], buf[n:]...)
		return talk
	}
	m.rest = m.rest[:0]
	if m.mutedLocked() {
		m.pass = m.pass[:0]
		return nil
	}
	m.pass = append(m.pass, in...)
	if len(m.pass) > maxPassBacklog {
		m.pass = append(m.pass[:0], m.pass[len(m.pass)-passBacklogKeep:]...)
	}
	return nil
}

// meetingOut preenche o próximo pedaço que vai para a reunião (microfone virtual).
func (m *mixer) meetingOut(out []float32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	muted := m.mutedLocked()
	n := 0
	if !muted {
		n = copy(out, m.pass)
		m.pass = m.pass[n:]
	}
	for i := n; i < len(out); i++ {
		out[i] = 0
	}
	// O inglês entra no lugar da sua voz (que está cortada enquanto ele toca)
	k := copy(out, m.speech)
	m.speech = m.speech[k:]
}

// monitorOut preenche o próximo pedaço do seu fone (o inglês enviado).
func (m *mixer) monitorOut(out []float32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := copy(out, m.monitor)
	m.monitor = m.monitor[n:]
	for i := n; i < len(out); i++ {
		out[i] = 0
	}
}

func (m *mixer) setTalking(on bool) {
	m.mu.Lock()
	m.talking = on
	m.rest = m.rest[:0]
	m.mu.Unlock()
}

func (m *mixer) setBusy(on bool) {
	m.mu.Lock()
	m.busy = on
	m.mu.Unlock()
}

// enqueue recebe o inglês (24 kHz) para a reunião e para o fone.
func (m *mixer) enqueue(speech []int16, monitor bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	up, last := upsample2(speech, m.prev)
	m.prev = last
	m.speech = append(m.speech, up...)
	if monitor {
		m.monitor = append(m.monitor, up...)
	}
}

func (m *mixer) pending() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Duration(len(m.speech)) * time.Second / RouterRate
}
