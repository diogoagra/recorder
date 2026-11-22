//go:build windows

package audio

import (
	"fmt"

	"github.com/gen2brain/malgo"
)

// loopbackSource captura o áudio do sistema via loopback do WASAPI.
type loopbackSource struct {
	ctx    *malgo.AllocatedContext
	device *malgo.Device
}

func newSystemSource(onSamples func([]int16)) (systemSource, error) {
	ctx, err := malgo.InitContext([]malgo.Backend{malgo.BackendWasapi}, malgo.ContextConfig{}, func(message string) {})
	if err != nil {
		return nil, fmt.Errorf("falha ao inicializar WASAPI: %w", err)
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Loopback)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = 1
	deviceConfig.SampleRate = sampleRate

	device, err := malgo.InitDevice(ctx.Context, deviceConfig, malgo.DeviceCallbacks{
		Data: func(pOutputSample, pInputSamples []byte, framecount uint32) {
			onSamples(decodeS16(pInputSamples, int(framecount)))
		},
	})
	if err != nil {
		_ = ctx.Uninit()
		ctx.Free()
		return nil, fmt.Errorf("falha ao abrir o loopback do sistema: %w", err)
	}

	return &loopbackSource{ctx: ctx, device: device}, nil
}

func (s *loopbackSource) Start() error {
	return s.device.Start()
}

func (s *loopbackSource) Close() {
	s.device.Stop()
	s.device.Uninit()
	_ = s.ctx.Uninit()
	s.ctx.Free()
}
