//go:build !darwin && !windows

package audio

import "errors"

func newSystemSource(onSamples func([]int16)) (systemSource, error) {
	return nil, errors.New("captura do áudio do sistema não suportada nesta plataforma")
}
