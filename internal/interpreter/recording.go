package interpreter

import (
	"fmt"
	"sync"

	"github.com/gen2brain/malgo"
)

// CalibrationText é o texto lido para calibrar a voz (uns 2-3 minutos):
// frases de reunião com afirmações, perguntas, números, nomes e entonações
// diferentes, para haver de onde escolher os trechos que mais soam como você.
const CalibrationText = `Hi everyone, thanks for joining. Let me give you a quick update on what I've been working on this week.

First, the payment integration is almost done. I fixed the timeout issue we saw on Tuesday, and the new version is already running in the staging environment. I still need to review the logs before we move it to production.

Second, about the deployment pipeline: the tests are passing, but the build takes around twelve minutes, which is too slow. I have a couple of ideas to make it faster, and I'd like to discuss them with you after this call.

Does anyone have any questions so far? If not, let's move on to the next topic.

Okay, so about the new report. The numbers look good overall: we processed about forty thousand transactions last month, which is twenty percent more than in September. The main problem is still the latency on Mondays, when the volume is higher.

I talked to Marcos and Juliana yesterday, and we agreed on a plan. Marcos will check the database indexes, and Juliana will take a look at the cache configuration. If everything goes well, we should see an improvement by the end of next week.

Honestly, I'm a bit worried about the deadline. We promised the client a demo on the fifteenth, and we still have three open bugs. I think we can make it, but we need to be careful about adding new features right now.

Could you share the document with the rest of the team? I'd also like to hear what the QA team thinks before we decide. Great, thank you all. Let's talk again on Thursday.`

const (
	minRecordingSeconds = 20.0
	maxRecordingSeconds = 6 * 60.0
)

// Recording é o que foi gravado na calibração, sem o silêncio das pontas.
type Recording struct {
	At24k []int16 // para a voz
	At16k []int16 // para o Whisper
}

func (r Recording) Seconds() float64 {
	return float64(len(r.At16k)) / WhisperRate
}

// Recorder grava a calibração pelo microfone padrão.
type Recorder struct {
	ctx    *malgo.AllocatedContext
	device *malgo.Device
	mu     sync.Mutex
	audio  []float32 // 48 kHz
}

// StartRecording começa a gravar.
func StartRecording() (*Recorder, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return nil, err
	}
	r := &Recorder{ctx: ctx}
	cfg := deviceConfig(malgo.Capture)
	cfg.PeriodSizeInMilliseconds = 0
	r.device, err = malgo.InitDevice(ctx.Context, cfg, malgo.DeviceCallbacks{
		Data: func(_, in []byte, frames uint32) {
			r.mu.Lock()
			if len(r.audio) < int(maxRecordingSeconds*RouterRate) {
				r.audio = append(r.audio, floats(in, int(frames))...)
			}
			r.mu.Unlock()
		},
	})
	if err == nil {
		err = r.device.Start()
	}
	if err != nil {
		r.close()
		return nil, fmt.Errorf("falha ao abrir o microfone: %w", err)
	}
	return r, nil
}

func (r *Recorder) close() {
	if r.device != nil {
		r.device.Uninit()
		r.device = nil
	}
	if r.ctx != nil {
		r.ctx.Uninit()
		r.ctx.Free()
		r.ctx = nil
	}
}

// Stop para de gravar e devolve a gravação sem o silêncio das pontas.
func (r *Recorder) Stop() (Recording, error) {
	r.close()
	r.mu.Lock()
	audio := trimSilence(r.audio)
	r.mu.Unlock()
	if seconds := float64(len(audio)) / RouterRate; seconds < minRecordingSeconds {
		return Recording{}, fmt.Errorf("a gravação teve só %.0f s de fala; leia pelo menos %.0f s do texto (o ideal são uns 2 minutos)", seconds, minRecordingSeconds)
	}
	return newRecording(audio), nil
}

func newRecording(audio []float32) Recording {
	at24k := make([]int16, len(audio)/2)
	for i := range at24k {
		at24k[i] = toInt16((audio[2*i] + audio[2*i+1]) / 2)
	}
	return Recording{At24k: at24k, At16k: downsample3(audio[:len(audio)/3*3])}
}

// Cancel descarta a gravação.
func (r *Recorder) Cancel() {
	r.close()
}

// trimSilence tira o silêncio do começo e do fim (trechos de 30 ms bem abaixo
// do volume da fala), deixando 0,2 s de folga.
func trimSilence(audio []float32) []float32 {
	const win = RouterRate * 30 / 1000
	n := len(audio) / win
	if n == 0 {
		return nil
	}
	levels := make([]float64, n)
	loudest := -100.0
	for i := range n {
		levels[i] = levelDB(downsample3(audio[i*win : (i+1)*win]))
		loudest = max(loudest, levels[i])
	}
	first, last := -1, -1
	for i, l := range levels {
		if l > loudest-30 {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return nil
	}
	pad := RouterRate / 5
	return audio[max(first*win-pad, 0):min((last+1)*win+pad, len(audio))]
}
