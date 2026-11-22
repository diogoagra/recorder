package interpreter

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/gen2brain/malgo"
)

// VirtualMicPrefix é o começo do nome do microfone virtual que o Meet usa
// (BlackHole: brew install blackhole-2ch).
const VirtualMicPrefix = "BlackHole"

// VirtualMicDriverInstalled diz se o driver do BlackHole está instalado. Logo
// depois de instalar, o macOS ainda não o carregou (o dispositivo não aparece)
// até o serviço de áudio reiniciar.
func VirtualMicDriverInstalled() bool {
	drivers, _ := filepath.Glob("/Library/Audio/Plug-Ins/HAL/" + VirtualMicPrefix + "*.driver")
	return len(drivers) > 0
}

// virtualMicMissing explica por que o microfone virtual não aparece.
func virtualMicMissing() error {
	if VirtualMicDriverInstalled() {
		return fmt.Errorf("o BlackHole foi instalado, mas o macOS ainda não o carregou: no Terminal, rode sudo killall coreaudiod (ou reinicie o Mac)")
	}
	return fmt.Errorf("o microfone virtual (BlackHole) não está instalado; instale com: brew install blackhole-2ch")
}

// Router liga os dispositivos ao mixer: o seu microfone de verdade, o
// microfone virtual que vai para a reunião e, se pedido, o seu fone (o
// dispositivo de saída padrão do Mac).
type Router struct {
	ctx     *malgo.AllocatedContext
	devices []*malgo.Device
	mix     mixer
	monitor bool

	MicName     string // seu microfone
	MeetingName string // o microfone virtual
}

func listDevices(ctx *malgo.AllocatedContext, kind malgo.DeviceType) ([]malgo.DeviceInfo, error) {
	return ctx.Devices(kind)
}

// preferredVirtualMic é o BlackHole que as instruções mandam instalar e
// escolher no Meet.
const preferredVirtualMic = VirtualMicPrefix + " 2ch"

// chooseVirtualMic escolhe o microfone virtual entre os nomes dos
// dispositivos de saída: o BlackHole 2ch, se houver; senão, o primeiro
// BlackHole. Devolve -1 se não houver nenhum. É a mesma escolha para a tela
// (FindVirtualMic) e para o roteador: com dois BlackHole instalados (2ch e
// 16ch), a tela indicava um e o áudio ia para o outro.
func chooseVirtualMic(names []string) int {
	first := -1
	for i, name := range names {
		if name == preferredVirtualMic {
			return i
		}
		if first < 0 && strings.HasPrefix(name, VirtualMicPrefix) {
			first = i
		}
	}
	return first
}

// virtualMic acha o microfone virtual entre os dispositivos de saída (nil se
// não houver).
func virtualMic(playback []malgo.DeviceInfo) *malgo.DeviceInfo {
	names := make([]string, len(playback))
	for i := range playback {
		names[i] = playback[i].Name()
	}
	if i := chooseVirtualMic(names); i >= 0 {
		return &playback[i]
	}
	return nil
}

// monitorAllowed diz se o inglês pode ir também para a saída de som padrão
// (o seu fone). Com o BlackHole como saída padrão, não: o inglês entraria
// duas vezes na reunião.
func monitorAllowed(defaultOutput string) bool {
	return !strings.HasPrefix(defaultOutput, VirtualMicPrefix)
}

// FindVirtualMic diz o nome do microfone virtual, se estiver instalado.
func FindVirtualMic() (string, bool) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return "", false
	}
	defer func() { ctx.Uninit(); ctx.Free() }()
	devices, err := listDevices(ctx, malgo.Playback)
	if err != nil {
		return "", false
	}
	if d := virtualMic(devices); d != nil {
		return d.Name(), true
	}
	return "", false
}

// StartRouter começa a repassar o seu microfone para o microfone virtual.
// onTalk recebe o seu áudio a 16 kHz enquanto o atalho está apertado.
// monitor manda o inglês também para o seu fone.
func StartRouter(onTalk func([]int16), monitor bool) (*Router, error) {
	return startRouter(onTalk, monitor, true)
}

// startRouter é StartRouter; sem openMic (nos testes), o microfone não é
// aberto e o áudio entra pelo mixer.
func startRouter(onTalk func([]int16), monitor, openMic bool) (*Router, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return nil, fmt.Errorf("falha ao abrir o áudio: %w", err)
	}
	r := &Router{ctx: ctx, monitor: monitor}
	fail := func(err error) (*Router, error) {
		r.Close()
		return nil, err
	}

	playback, err := listDevices(ctx, malgo.Playback)
	if err != nil {
		return fail(err)
	}
	meeting := virtualMic(playback)
	if meeting == nil {
		return fail(virtualMicMissing())
	}
	r.MeetingName = meeting.Name()
	for _, d := range playback {
		if monitor && d.IsDefault == 1 && !monitorAllowed(d.Name()) {
			log.Printf("Intérprete: a saída de som padrão é o %s; o inglês não vai também para ela", d.Name())
			monitor, r.monitor = false, false
		}
	}

	capture, err := listDevices(ctx, malgo.Capture)
	if err != nil {
		return fail(err)
	}
	for _, d := range capture {
		if d.IsDefault == 1 {
			r.MicName = d.Name()
		}
	}
	// Com o BlackHole como entrada padrão, o roteador repassaria o próprio
	// microfone virtual para ele mesmo
	if openMic && strings.HasPrefix(r.MicName, VirtualMicPrefix) {
		return fail(fmt.Errorf("a entrada de som padrão do Mac é o %s; escolha o seu microfone em Ajustes do Sistema > Som > Entrada (no Meet, sim, o microfone é o %s)", r.MicName, r.MicName))
	}

	// Seu microfone: repasse e, com o atalho apertado, a tradução
	if openMic {
		mic := deviceConfig(malgo.Capture)
		micDevice, err := malgo.InitDevice(ctx.Context, mic, malgo.DeviceCallbacks{
			Data: func(_, in []byte, frames uint32) {
				if talk := r.mix.micIn(floats(in, int(frames))); talk != nil && onTalk != nil {
					onTalk(talk) // fora do lock do mixer
				}
			},
		})
		if err != nil {
			return fail(fmt.Errorf("falha ao abrir o microfone: %w", err))
		}
		r.devices = append(r.devices, micDevice)
	}

	// Microfone virtual: o que a reunião ouve
	out := deviceConfig(malgo.Playback)
	out.Playback.DeviceID = meeting.ID.Pointer()
	meetingDevice, err := malgo.InitDevice(ctx.Context, out, malgo.DeviceCallbacks{
		Data: func(outBytes, _ []byte, frames uint32) {
			r.mix.meetingOut(floats(outBytes, int(frames)))
		},
	})
	if err != nil {
		return fail(fmt.Errorf("falha ao abrir o %s: %w", r.MeetingName, err))
	}
	r.devices = append(r.devices, meetingDevice)

	// Seu fone: o inglês que foi para a reunião
	if monitor {
		mon := deviceConfig(malgo.Playback)
		monDevice, err := malgo.InitDevice(ctx.Context, mon, malgo.DeviceCallbacks{
			Data: func(outBytes, _ []byte, frames uint32) {
				r.mix.monitorOut(floats(outBytes, int(frames)))
			},
		})
		if err != nil {
			return fail(fmt.Errorf("falha ao abrir a saída de som: %w", err))
		}
		r.devices = append(r.devices, monDevice)
	}

	for _, d := range r.devices {
		if err := d.Start(); err != nil {
			return fail(fmt.Errorf("falha ao iniciar o áudio: %w", err))
		}
	}
	return r, nil
}

func deviceConfig(kind malgo.DeviceType) malgo.DeviceConfig {
	cfg := malgo.DefaultDeviceConfig(kind)
	cfg.SampleRate = RouterRate
	cfg.PeriodSizeInMilliseconds = 10 // atraso mínimo no repasse
	cfg.PerformanceProfile = malgo.LowLatency
	if kind == malgo.Capture {
		cfg.Capture.Format = malgo.FormatF32
		cfg.Capture.Channels = 1
	} else {
		cfg.Playback.Format = malgo.FormatF32
		cfg.Playback.Channels = 1
	}
	return cfg
}

// floats vê os bytes do malgo como amostras float32 (mono), sem copiar.
func floats(b []byte, frames int) []float32 {
	if len(b) < frames*4 || frames == 0 {
		return nil
	}
	return unsafe.Slice((*float32)(unsafe.Pointer(&b[0])), frames)
}

// Close para os dispositivos.
func (r *Router) Close() {
	for _, d := range r.devices {
		d.Uninit()
	}
	r.devices = nil
	if r.ctx != nil {
		r.ctx.Uninit()
		r.ctx.Free()
		r.ctx = nil
	}
}

// Names devolve o nome do seu microfone e o do microfone virtual em uso.
func (r *Router) Names() (mic, meeting string) { return r.MicName, r.MeetingName }

func (r *Router) SetTalking(on bool)     { r.mix.setTalking(on) }
func (r *Router) SetBusy(on bool)        { r.mix.setBusy(on) }
func (r *Router) Enqueue(speech []int16) { r.mix.enqueue(speech, r.monitor) }
func (r *Router) Pending() time.Duration { return r.mix.pending() }
