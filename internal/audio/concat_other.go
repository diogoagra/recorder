//go:build !darwin

package audio

import "errors"

// concatCompressed só existe no macOS, onde o áudio das sessões é comprimido
// (m4a). No Windows ele é WAV, que se junta sem perda (concatWav).
func concatCompressed(parts []string, out string) error {
	return errors.New("junção de áudio comprimido não suportada nesta plataforma")
}
