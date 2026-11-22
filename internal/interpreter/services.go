package interpreter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
)

// WhisperPort é a porta do whisper-server só do Intérprete: o do app pode
// estar ocupado com um bloco de 30 s da gravação, e a sua frase esperaria.
const WhisperPort = "47825"

// whisperRequest manda o áudio ao whisper-server e devolve o JSON da resposta.
// O prazo é o de ctx: o das frases ao vivo é curto (interpreter.go) e o da
// calibração cresce com a gravação (calibration.go).
func whisperRequest(ctx context.Context, endpoint string, audio []int16, fields map[string]string) (map[string]any, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k, v := range fields {
		w.WriteField(k, v)
	}
	part, err := w.CreateFormFile("file", "fala.wav")
	if err != nil {
		return nil, err
	}
	part.Write(wavBytes(audio, WhisperRate))
	w.Close()
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/inference", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("o Whisper não respondeu: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("o Whisper não respondeu: %w", err)
	}
	var out map[string]any
	jsonErr := json.Unmarshal(raw, &out)
	if msg, ok := out["error"].(string); ok && msg != "" {
		return nil, fmt.Errorf("whisper: %s", msg)
	}
	// Carregando o modelo (503) ou outro programa na porta: sem isto, o erro
	// mostrado era o do JSON ("invalid character...")
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("whisper: HTTP %d: %s", resp.StatusCode, tail(strings.TrimSpace(string(raw)), 200))
	}
	if jsonErr != nil {
		return nil, fmt.Errorf("whisper: resposta fora do formato: %w", jsonErr)
	}
	return out, nil
}

// detectLanguage só detecta o idioma (~0,4 s), sem transcrever: ex "portuguese".
func detectLanguage(ctx context.Context, endpoint string, audio []int16) (string, error) {
	out, err := whisperRequest(ctx, endpoint, audio, map[string]string{
		"response_format": "json", "temperature": "0", "language": "auto", "detect_language": "true"})
	if err != nil {
		return "", err
	}
	lang, _ := out["language"].(string)
	return lang, nil
}

// englishConfidence é a certeza (0-1) de que o áudio é inglês. Custa o dobro
// de detectLanguage (~0,7 s), e só é pedida quando ela diz "english": um
// pedaço curto de ruído no fim da fala também sai "english", mas com ~40%.
func englishConfidence(ctx context.Context, endpoint string, audio []int16) (float64, error) {
	out, err := whisperRequest(ctx, endpoint, audio, map[string]string{
		"response_format": "verbose_json", "temperature": "0", "language": "auto", "detect_language": "true"})
	if err != nil {
		return 0, err
	}
	probs, _ := out["language_probabilities"].(map[string]any)
	p, _ := probs["en"].(float64)
	return p, nil
}

// transcribePT transcreve em português; com toEnglish, o próprio Whisper
// traduz para o inglês (literal, usado se nenhum tradutor responder). O texto
// vem só com o que foi falado (spokenText): vazio, se não havia fala.
func transcribePT(ctx context.Context, endpoint string, audio []int16, toEnglish bool) (string, error) {
	fields := map[string]string{"response_format": "json", "temperature": "0", "language": "pt"}
	if toEnglish {
		fields["translate"] = "true"
	}
	out, err := whisperRequest(ctx, endpoint, audio, fields)
	if err != nil {
		return "", err
	}
	text, _ := out["text"].(string)
	return spokenText(text), nil
}

// hallucinations são frases que o Whisper inventa sobre ruído ou silêncio.
var hallucinations = map[string]bool{
	"obrigado por assistir": true, "obrigado por assistir!": true, "obrigado.": true, "tchau.": true,
	"legendas pela comunidade amara.org": true, "...": true, "e aí": true,
}

func isHallucination(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	return t == "" || hallucinations[t]
}

// notSpoken são as marcas que o Whisper põe onde não há fala: "[silêncio]",
// "(música)", "*risos*", as notas de uma música.
var notSpoken = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)|\*[^*]*\*|[♪♫]+`)

// spokenText tira do texto do Whisper as marcas do que não é fala. Um aperto
// sem fala (só o ruído do microfone) é transcrito como "[silêncio]", e a
// tradução do mesmo áudio sai inventada ("Thank you for watching!"): sem
// isto, a frase inventada ia para a reunião na sua voz.
func spokenText(text string) string {
	return strings.Join(strings.Fields(notSpoken.ReplaceAllString(text, " ")), " ")
}

// words são as palavras de 3 letras ou mais, para comparar textos.
func words(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
	}) {
		if len([]rune(w)) >= 3 {
			out[w] = true
		}
	}
	return out
}

// repeats diz se o texto ouvido repete o inglês dito há pouco: o retorno no
// fone ou no alto-falante chegando ao microfone.
func repeats(heard string, recent []string) bool {
	h := words(heard)
	if len(h) < 3 {
		return false
	}
	said := map[string]bool{}
	for _, r := range recent {
		for w := range words(r) {
			said[w] = true
		}
	}
	common := 0
	for w := range h {
		if said[w] {
			common++
		}
	}
	return float64(common) >= 0.5*float64(len(h))
}
