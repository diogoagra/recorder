package transcriber

import (
	"reflect"
	"testing"
)

func TestOrphanWhisperPIDs(t *testing.T) {
	ps := `    1     0 /sbin/launchd
34331     1 /opt/homebrew/bin/whisper-server -m ggml-medium-q5_0.bin --port 8080
41318 41315 /opt/homebrew/bin/whisper-server -m /Applications/Recorder.app/Contents/Resources/ggml-medium-q5_0.bin --port 8080
50000     1 /opt/homebrew/bin/whisper-server -m outro.bin --port 9090
50001     1 /usr/bin/vim notas-whisper-server.txt --port 8080
50002     1 /opt/homebrew/bin/whisper-server -m outro.bin --port 80801
`
	// Só o órfão na porta do app: o filho do app em execução, o de outra porta
	// (mesmo uma que comece com os mesmos números) e outros programas ficam de fora
	if got := orphanWhisperPIDs(ps, "8080"); !reflect.DeepEqual(got, []int{34331}) {
		t.Fatalf("esperava [34331], veio %v", got)
	}
}
