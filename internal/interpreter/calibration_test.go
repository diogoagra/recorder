package interpreter

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTranscriptionTimeoutGrowsWithTheRecording(t *testing.T) {
	if got := transcriptionTimeout(20); got != 80*time.Second {
		t.Fatalf("gravação de 20 s: %v", got)
	}
	// A gravação mais longa (6 min) não pode cair no prazo de uma frase ao vivo
	if got := transcriptionTimeout(maxRecordingSeconds); got != 7*time.Minute {
		t.Fatalf("gravação de 6 min: %v", got)
	}
}

func TestCleanSegmentsDropsWhatIsNotInTheAudio(t *testing.T) {
	got := cleanSegments([]segment{
		{0, 5, " Hi  everyone. "},
		{5, 5, "vazio"},     // sem duração
		{6, 9, "   "},       // sem texto
		{28, 33, "The end"}, // passa do fim: encurtado
		{31, 40, "Thank you."},
		{45, 50, "Thank you."}, // inventados no silêncio do fim
	}, 30)
	if len(got) != 2 || got[0].Text != "Hi everyone." || got[1].End != 30 || got[1].Text != "The end" {
		t.Fatalf("segmentos: %+v", got)
	}
}

func TestReferenceSpanStaysInsideTheRecording(t *testing.T) {
	total := 30 * SpeechRate
	for _, r := range []reference{
		{start: 0, end: 12}, {start: 10, end: 29.9}, {start: 25, end: 45}, {start: 40, end: 55}, {start: -3, end: 2},
	} {
		a, b := referenceSpan(r, total)
		if a < 0 || a > b || b > total {
			t.Fatalf("trecho %+v virou [%d:%d] numa gravação de %d amostras", r, a, b, total)
		}
		_ = make([]int16, total)[a:b] // não pode dar panic
	}
	if a, b := referenceSpan(reference{start: 40, end: 55}, total); a != b {
		t.Fatalf("um trecho além do fim não tem áudio: [%d:%d]", a, b)
	}
	if a, b := referenceSpan(reference{start: 10, end: 20}, total); a != int(9.85*SpeechRate) || b != int(20.25*SpeechRate) {
		t.Fatalf("folga nas pontas: [%d:%d]", a, b)
	}
}

// calibrationServer responde como o Whisper (com os segmentos dados) e como
// a voz (0,1 s de áudio por fala, ou erro com failVoice).
func calibrationServer(t *testing.T, segs []segment, failVoice bool) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/inference":
			var out []map[string]any
			for _, s := range segs {
				out = append(out, map[string]any{"start": s.Start, "end": s.End, "text": s.Text})
			}
			json.NewEncoder(w).Encode(map[string]any{"text": "...", "segments": out})
		case "/speak":
			if failVoice {
				writeFrame(w, 'E', []byte("faltou memória"))
				return
			}
			writeFrame(w, 'A', pcm(2400, 0))
			writeFrame(w, 'F', nil)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func recordingOf(seconds int) Recording {
	audio := make([]float32, seconds*RouterRate)
	for i := range audio {
		audio[i] = float32(0.3 * math.Sin(float64(i)*2*math.Pi*220/RouterRate))
	}
	return newRecording(audio)
}

func sentences(n int) []segment {
	var segs []segment
	for i := range n {
		segs = append(segs, segment{float64(i) * 5, float64(i)*5 + 4.8, fmt.Sprintf(" Hi everyone, thanks for joining %d.", i+1)})
	}
	return segs
}

// O Whisper às vezes devolve frases com tempos além do fim da gravação: elas
// não podem virar opção nem derrubar o app (era um índice fora do áudio).
func TestCalibrateIgnoresSegmentsBeyondTheRecording(t *testing.T) {
	segs := append(sentences(4), // 20 s de gravação de verdade
		segment{60, 64.8, "Hi everyone, thanks for joining."},
		segment{65, 69.8, "Hi everyone, thanks for joining."},
		segment{70, 74.8, "Hi everyone, thanks for joining."})
	srv := calibrationServer(t, segs, false)
	v := NewVoice(t.TempDir())
	v.Endpoint = srv.URL
	options, err := Calibrate(context.Background(), recordingOf(20), srv.URL, v, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(options) == 0 {
		t.Fatal("os trechos de dentro da gravação deveriam virar opções")
	}
	for _, o := range options {
		if o.Seconds < minRefSeconds || o.Seconds > 20.5 || strings.Contains(o.Text, "joining.") {
			t.Fatalf("opção fora da gravação: %+v", o)
		}
	}
}

// Uma calibração que falha não apaga as opções da anterior.
func TestFailedCalibrationKeepsThePreviousOptions(t *testing.T) {
	v := NewVoice(t.TempDir())
	good := calibrationServer(t, sentences(8), false)
	v.Endpoint = good.URL
	before, err := Calibrate(context.Background(), recordingOf(40), good.URL, v, func(string) {})
	if err != nil || len(before) == 0 {
		t.Fatalf("1ª calibração: %v", err)
	}

	for name, srv := range map[string]*httptest.Server{
		"a voz falhou":       calibrationServer(t, sentences(8), true),
		"sem trechos bons":   calibrationServer(t, sentences(1), false),
		"whisper fora do ar": httptest.NewServer(http.NotFoundHandler()),
	} {
		v.Endpoint = srv.URL
		if _, err := Calibrate(context.Background(), recordingOf(40), srv.URL, v, func(string) {}); err == nil {
			t.Fatalf("%s: a calibração deveria falhar", name)
		}
		srv.Close()
		after := v.CalibrationOptions()
		if len(after) != len(before) {
			t.Fatalf("%s: as opções de antes deveriam continuar (%d), ficaram %d", name, len(before), len(after))
		}
		for _, o := range after {
			for _, f := range []string{o.Reference, o.Preview} {
				if _, err := os.Stat(filepath.Join(v.CalibrationDir(), f)); err != nil {
					t.Fatalf("%s: o arquivo %s da calibração anterior sumiu", name, f)
				}
			}
		}
		if _, err := os.Stat(v.CalibrationDir() + ".nova"); err == nil {
			t.Fatalf("%s: a pasta da calibração que falhou deveria ser apagada", name)
		}
	}

	// Cancelada no meio (o usuário desistiu): também acaba, sem mexer na anterior
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v.Endpoint = good.URL
	if _, err := Calibrate(ctx, recordingOf(40), good.URL, v, func(string) {}); err == nil {
		t.Fatal("cancelada, a calibração deveria parar")
	}
	if len(v.CalibrationOptions()) != len(before) {
		t.Fatal("cancelar não apaga a calibração anterior")
	}
}
