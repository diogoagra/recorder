//go:build darwin

package audio

/*
#cgo LDFLAGS: -framework CoreAudio -framework Foundation
#include "systap_darwin.h"
*/
import "C"

import (
	"errors"
	"runtime/cgo"
	"unsafe"
)

// tapSource captura o áudio do sistema via Core Audio Process Taps (macOS 14.2+),
// sem precisar de drivers virtuais como o BlackHole.
type tapSource struct {
	tap       *C.systap
	handle    cgo.Handle
	rate      float64
	resampler *resampler
	onSamples func([]int16)
}

func newSystemSource(onSamples func([]int16)) (systemSource, error) {
	s := &tapSource{
		onSamples: onSamples,
		resampler: newResampler(sampleRate),
	}
	s.handle = cgo.NewHandle(s)

	var rate C.double
	var errBuf [256]C.char
	s.tap = C.systap_create(C.uintptr_t(s.handle), C.double(sampleRate), &rate, &errBuf[0], C.int(len(errBuf)))
	if s.tap == nil {
		s.handle.Delete()
		return nil, errors.New(C.GoString(&errBuf[0]))
	}
	s.rate = float64(rate)
	return s, nil
}

func (s *tapSource) Start() error {
	var errBuf [256]C.char
	if C.systap_start(s.tap, &errBuf[0], C.int(len(errBuf))) != 0 {
		return errors.New(C.GoString(&errBuf[0]))
	}
	return nil
}

func (s *tapSource) Close() {
	C.systap_destroy(s.tap)
	s.tap = nil
	s.handle.Delete()
}

//export goSystemTapData
func goSystemTapData(h C.uintptr_t, data *C.float, frames C.int, channels C.int) {
	s := cgo.Handle(h).Value().(*tapSource)
	in := unsafe.Slice((*float32)(unsafe.Pointer(data)), int(frames)*int(channels))
	if out := s.resampler.process(in, int(channels), s.rate); len(out) > 0 {
		s.onSamples(out)
	}
}
