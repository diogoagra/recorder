package interpreter

import (
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gen2brain/malgo"
)

// TestRouterSendsEnglishToVirtualMic usa os dispositivos de verdade: o inglês
// mandado ao roteador precisa chegar à entrada do BlackHole, que é o que o
// Meet ouve. Abre o microfone, então só roda com RECORDER_DEVICE_TESTS=1 e com
// o BlackHole instalado.
func TestRouterSendsEnglishToVirtualMic(t *testing.T) {
	if os.Getenv("RECORDER_DEVICE_TESTS") == "" {
		t.Skip("só com RECORDER_DEVICE_TESTS=1")
	}
	if _, ok := FindVirtualMic(); !ok {
		t.Skip("BlackHole não instalado")
	}
	router, err := StartRouter(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()

	// Grava o que a reunião ouve: a entrada do BlackHole
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { ctx.Uninit(); ctx.Free() }()
	inputs, _ := ctx.Devices(malgo.Capture)
	var id *malgo.DeviceID
	for i := range inputs {
		if strings.HasPrefix(inputs[i].Name(), VirtualMicPrefix) {
			id = &inputs[i].ID
		}
	}
	if id == nil {
		t.Fatal("entrada do BlackHole não encontrada")
	}
	var mu sync.Mutex
	var heard []float32
	cfg := deviceConfig(malgo.Capture)
	cfg.Capture.DeviceID = id.Pointer()
	dev, err := malgo.InitDevice(ctx.Context, cfg, malgo.DeviceCallbacks{
		Data: func(_, in []byte, frames uint32) {
			mu.Lock()
			heard = append(heard, floats(in, int(frames))...)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Uninit()
	dev.Start()

	// Push-to-talk: a sua voz sai da reunião e entra 0,5 s de um tom (o "inglês")
	router.SetTalking(true)
	time.Sleep(300 * time.Millisecond)
	tone := make([]int16, SpeechRate/2)
	for i := range tone {
		tone[i] = int16(12000 * math.Sin(float64(i)*2*math.Pi*440/SpeechRate))
	}
	mu.Lock()
	before := len(heard)
	mu.Unlock()
	router.Enqueue(tone)
	time.Sleep(900 * time.Millisecond)
	router.SetTalking(false)

	mu.Lock()
	got := append([]float32(nil), heard[before:]...)
	mu.Unlock()
	var peak float64
	for _, s := range got {
		peak = math.Max(peak, math.Abs(float64(s)))
	}
	if peak < 0.2 {
		t.Fatalf("o tom não chegou ao BlackHole (pico %.3f em %d amostras)", peak, len(got))
	}
	if router.Pending() != 0 {
		t.Fatalf("o inglês deveria ter acabado de tocar, faltam %v", router.Pending())
	}
	t.Logf("tom recebido no %s com pico %.2f (microfone: %s)", router.MeetingName, peak, router.MicName)
}

// TestRouterAudioIsContinuous manda um tom contínuo pelo roteador, como o
// microfone manda (pedaços de 512 amostras a 48 kHz), e conta os buracos de
// silêncio no que chega ao BlackHole: buracos soam como estalos e picotes.
// Faz o mesmo com o inglês chegando aos poucos, um pouco mais devagar que o
// tempo real no meio (o Mac ocupado). Só roda com RECORDER_DEVICE_TESTS=1.
func TestRouterAudioIsContinuous(t *testing.T) {
	if os.Getenv("RECORDER_DEVICE_TESTS") == "" {
		t.Skip("só com RECORDER_DEVICE_TESTS=1")
	}
	if _, ok := FindVirtualMic(); !ok {
		t.Skip("BlackHole não instalado")
	}
	router, err := startRouter(nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	heard, stopCapture := captureVirtualMic(t)
	defer stopCapture()

	// 2 s da "sua voz" (um tom contínuo), no ritmo do microfone
	phase := 0
	start := time.Now()
	for sent := 0; sent < 2*RouterRate; sent += 512 {
		chunk := make([]float32, 512)
		for i := range chunk {
			chunk[i] = float32(0.3 * math.Sin(float64(phase)*2*math.Pi*440/RouterRate))
			phase++
		}
		router.mix.micIn(chunk)
		time.Sleep(time.Until(start.Add(time.Duration(sent+512) * time.Second / RouterRate)))
	}
	time.Sleep(200 * time.Millisecond)
	voice := heard()
	gaps, longest := countGaps(voice)
	t.Logf("sua voz: %d buracos (o maior com %.1f ms) em %.1f s", gaps, longest, float64(len(voice))/RouterRate)

	// O inglês: 0,4 s de uma vez, depois pedaços de 0,1 s a 0,8x o tempo real
	router.SetTalking(true)
	mark := len(heard())
	tone := func(seconds float64) []int16 {
		out := make([]int16, int(seconds*SpeechRate))
		for i := range out {
			out[i] = int16(9000 * math.Sin(float64(phase)*2*math.Pi*330/SpeechRate))
			phase++
		}
		return out
	}
	router.Enqueue(tone(0.4))
	for range 20 {
		time.Sleep(125 * time.Millisecond)
		router.Enqueue(tone(0.1))
	}
	for router.Pending() > 0 {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	router.SetTalking(false)
	english := heard()[mark:]
	gapsEN, longestEN := countGaps(english)
	t.Logf("inglês: %d buracos (o maior com %.1f ms)", gapsEN, longestEN)
	if gaps > 2 || gapsEN > 2 {
		t.Fatalf("áudio picotado: %d buracos na sua voz e %d no inglês", gaps, gapsEN)
	}
}

// captureVirtualMic grava a entrada do BlackHole (o que a reunião ouve).
func captureVirtualMic(t *testing.T) (heard func() []float32, stop func()) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	inputs, _ := ctx.Devices(malgo.Capture)
	var id *malgo.DeviceID
	for i := range inputs {
		if strings.HasPrefix(inputs[i].Name(), VirtualMicPrefix) {
			id = &inputs[i].ID
		}
	}
	var mu sync.Mutex
	var audio []float32
	cfg := deviceConfig(malgo.Capture)
	cfg.Capture.DeviceID = id.Pointer()
	dev, err := malgo.InitDevice(ctx.Context, cfg, malgo.DeviceCallbacks{Data: func(_, in []byte, frames uint32) {
		mu.Lock()
		audio = append(audio, floats(in, int(frames))...)
		mu.Unlock()
	}})
	if err != nil {
		t.Fatal(err)
	}
	dev.Start()
	return func() []float32 {
			mu.Lock()
			defer mu.Unlock()
			return append([]float32(nil), audio...)
		}, func() {
			dev.Uninit()
			ctx.Uninit()
			ctx.Free()
		}
}

// countGaps conta os trechos de mais de 1 ms em silêncio no meio do som
// (entre a primeira e a última amostra com som).
func countGaps(audio []float32) (gaps int, longestMs float64) {
	first, last := -1, -1
	for i, s := range audio {
		if math.Abs(float64(s)) > 0.01 {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return 0, 0
	}
	run := 0
	for _, s := range audio[first : last+1] {
		if math.Abs(float64(s)) < 1e-4 {
			run++
			continue
		}
		if run > RouterRate/1000 {
			gaps++
			longestMs = math.Max(longestMs, float64(run)*1000/RouterRate)
		}
		run = 0
	}
	return gaps, longestMs
}
