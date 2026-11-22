//go:build !darwin

package screen

import "context"

// Supported diz se a captura de tela existe neste sistema (só no macOS).
const Supported = false

func Allowed() bool { return false }

func RequestAccess() bool { return false }

func Capture(ctx context.Context, path string) error {
	return ErrUnsupported
}
