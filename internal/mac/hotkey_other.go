//go:build !darwin

package mac

// PushToTalkShortcut é o atalho global do Intérprete (só existe no macOS).
const PushToTalkShortcut = ""

// EnablePushToTalk não faz nada fora do macOS: o Intérprete só funciona nele.
func EnablePushToTalk(on bool, onChange func(pressed bool)) {}

// CaptureShortcut é o atalho global da captura de tela (só existe no macOS).
const CaptureShortcut = ""

// EnableCaptureShortcut não faz nada fora do macOS.
func EnableCaptureShortcut(on bool, onPress func()) {}
