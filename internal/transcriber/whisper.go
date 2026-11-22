package transcriber

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Tempo máximo de uma transcrição. Um bloco de 30s leva poucos segundos; o
// prazo existe para um servidor travado não segurar a fila para sempre (a
// gravação continua, e os blocos seguintes esperariam atrás dele).
const transcribeTimeout = 5 * time.Minute

type WhisperClient struct {
	Endpoint string
	// Timeout de cada transcrição; zero usa transcribeTimeout.
	Timeout time.Duration
}

func NewWhisperClient(endpoint string) *WhisperClient {
	if endpoint == "" {
		endpoint = Endpoint
	}
	return &WhisperClient{
		Endpoint: endpoint,
	}
}

// Ready diz se o whisper-server já carregou o modelo e aceita transcrições.
func (c *WhisperClient) Ready() bool {
	resp, ok := c.health()
	// Enquanto carrega o modelo, ele responde 503 em /health
	return ok && resp.StatusCode != http.StatusServiceUnavailable
}

// Running diz se quem atende no endereço é um whisper-server, mesmo que
// ainda carregando o modelo, e não outro programa na mesma porta.
func (c *WhisperClient) Running() bool {
	_, ok := c.health()
	return ok
}

// health consulta /health e diz se quem respondeu foi um whisper-server, que
// se identifica no cabeçalho Server em todas as respostas.
func (c *WhisperClient) health() (*http.Response, bool) {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(c.Endpoint + "/health")
	if err != nil {
		return nil, false
	}
	resp.Body.Close()
	return resp, strings.Contains(strings.ToLower(resp.Header.Get("Server")), "whisper")
}

// Segment é um trecho transcrito. Start e End ficam zerados quando o servidor
// não informa os tempos.
type Segment struct {
	Start float64 // segundos desde o início do áudio
	End   float64
	Text  string
}

// Text junta o texto dos segmentos.
func Text(segs []Segment) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		if t := strings.TrimSpace(s.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// TranscribeWav envia um arquivo WAV para o whisper-server e retorna os
// trechos transcritos (palavra por palavra no whisper.cpp), com o tempo de
// cada um quando o servidor informa. prompt são palavras esperadas no áudio
// (nomes, termos), que ajudam o Whisper a acertar a grafia; pode ser vazio.
func (c *WhisperClient) TranscribeWav(wavFilePath, prompt string) ([]Segment, error) {
	file, err := os.Open(wavFilePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", filepath.Base(wavFilePath))
	if err != nil {
		return nil, err
	}

	_, err = io.Copy(part, file)
	if err != nil {
		return nil, err
	}

	// verbose_json traz o início e o fim de cada trecho; com max_len=1 e
	// split_on_word cada trecho é uma palavra. Os tempos servem para saber quem falou.
	_ = writer.WriteField("response_format", "verbose_json")
	_ = writer.WriteField("max_len", "1")
	_ = writer.WriteField("split_on_word", "true")
	// Detecção automática de idioma (ex: pt/en). Sem o campo, o whisper-server usa
	// o padrão dele ("en") e traduz a fala para inglês.
	_ = writer.WriteField("language", "auto")
	if prompt != "" {
		_ = writer.WriteField("prompt", prompt)
	}

	err = writer.Close()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", c.Endpoint+"/inference", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = transcribeTimeout
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("falha ao falar com o whisper-server em %s: %w", c.Endpoint, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Code: resp.StatusCode, Body: string(respBody)}
	}

	return parseResponse(respBody), nil
}

// StatusError é uma resposta de erro do whisper-server: ele está de pé e
// respondeu, ao contrário de uma falha de conexão.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("whisper API error %d: %s", e.Code, e.Body)
}

// Loading diz se o servidor só está carregando o modelo e vai aceitar o
// mesmo pedido daqui a pouco.
func (e *StatusError) Loading() bool {
	return e.Code == http.StatusServiceUnavailable
}

// parseResponse entende o verbose_json do whisper.cpp e, como alternativa,
// os formatos de outros servidores (ex: um rodando via Docker), que
// não trazem os tempos.
func parseResponse(respBody []byte) []Segment {
	var jsonResp struct {
		Code int `json:"code"`
		Data []struct {
			Sentence string `json:"sentence"`
		} `json:"data"`
		Text     string `json:"text"`
		Segments []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Text  string  `json:"text"`
		} `json:"segments"`
	}

	rawString := strings.TrimSpace(string(respBody))
	if err := json.Unmarshal(respBody, &jsonResp); err == nil {
		var segs []Segment
		switch {
		case len(jsonResp.Segments) > 0:
			for _, s := range jsonResp.Segments {
				segs = append(segs, Segment{Start: s.Start, End: s.End, Text: strings.TrimSpace(s.Text)})
			}
		case len(jsonResp.Data) > 0:
			for _, d := range jsonResp.Data {
				segs = append(segs, Segment{Text: strings.TrimSpace(d.Sentence)})
			}
		default:
			segs = append(segs, Segment{Text: strings.TrimSpace(jsonResp.Text)})
		}
		return segs
	}

	return []Segment{{Text: rawString}}
}
