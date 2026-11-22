//go:build !darwin

package mac

func SetBadge(badge string) {
	// Not supported on non-macOS platforms out of the box
}
