//go:build !darwin

package mac

// IsActive diz se o Recorder é o app em primeiro plano (só é usado no macOS).
func IsActive() bool {
	return false
}
