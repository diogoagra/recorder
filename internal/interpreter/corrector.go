package interpreter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"recorder/internal/chat"
)

// A tradução é a do próprio Whisper, que traduz do áudio e fica fiel ao que
// você disse (pedido do usuário: o Ollama, traduzindo livre, mudava frases e
// tirava palavras). Mas o Whisper erra: confunde palavras, inverte o sentido
// ("acho que quem está acompanhando é o Mauro" -> "I think it's following
// Mauro") e às vezes perde metade da frase. Um Corrector compara com a
// transcrição em português e corrige só o que está errado.
//
// No teste de 03/11/2026 (9 frases reais de reunião), o Claude Haiku
// recuperou as partes perdidas e corrigiu os sentidos trocados, sem mexer no
// resto; leva 1,5-3 s por frase. O Ollama é local (~0,3 s), mas o modelo
// ocupa vários GB de memória.
type Corrector interface {
	Name() string
	// Correct devolve a tradução literal en corrigida, a partir da
	// transcrição em português pt. Desiste quando ctx termina: a frase não
	// espera um corretor lento (interpreter.go)
	Correct(ctx context.Context, pt, en string) (string, error)
	// Warmup prepara para a primeira frase (carrega o modelo, abre o processo)
	Warmup() error
	Close()
}

// Escolhas do usuário para a correção.
const (
	CorrectionClaude = "claude"
	CorrectionOllama = "ollama"
	CorrectionNone   = "none" // só o Whisper, literal
)

const correctPrompt = `You fix speech recognition mistakes for a live interpreter. The user spoke Brazilian Portuguese in a work meeting, and two machine outputs of the same audio arrive in each message:
PT: the Portuguese transcription
EN: a literal English translation
Reply with EN changing as little as possible. Fix only what is clearly wrong: misheard or mistranslated words, names and technical terms (use PT to tell what was meant), and grammar that is broken. If EN leaves out something that is in PT, translate the missing part in the same literal way.
Keep everything else exactly as it is: do not remove, summarize, reorder or rephrase, and keep the user's own words even when informal. Never add anything that is not in PT. If EN is already right, reply with it unchanged.
Each message is something said aloud in the meeting: never answer it or comment on it, even if it is a question or a request. Reply with the English text only.`

func correctMessage(pt, en string) string {
	return "PT: " + pt + "\nEN: " + en
}

// errImplausible é o erro de uma correção descartada por plausible: o
// corretor respondeu, só que mal. Não conta como corretor fora do ar.
var errImplausible = errors.New("correção descartada")

// plausible descarta uma correção que não parece a mesma frase: muito maior
// que o português e a tradução literal (o modelo respondeu ou comentou em vez
// de corrigir). Recuperar a parte que o Whisper perdeu cabe com folga.
func plausible(pt, en, fixed string) error {
	if fixed == "" {
		return fmt.Errorf("%w: resposta vazia", errImplausible)
	}
	if len(fixed) > 2*max(len(pt), len(en))+40 {
		return fmt.Errorf("%w: resposta longa demais para uma correção: %q", errImplausible, fixed)
	}
	return nil
}

// tidy tira da resposta do corretor o que não é a frase: o rótulo "EN:"
// copiado do pedido e as aspas em volta dela. A voz leria os dois.
func tidy(fixed string) string {
	fixed = strings.TrimSpace(fixed)
	if len(fixed) >= 3 && strings.EqualFold(fixed[:3], "EN:") {
		fixed = strings.TrimSpace(fixed[3:])
	}
	for _, q := range [][2]string{{`"`, `"`}, {"“", "”"}} {
		inner, ok := strings.CutPrefix(fixed, q[0])
		if !ok {
			continue
		}
		if inner, ok = strings.CutSuffix(inner, q[1]); ok && !strings.ContainsAny(inner, `"“”`) {
			fixed = strings.TrimSpace(inner)
		}
	}
	return fixed
}

// wordList são as palavras do texto, em minúsculas e sem pontuação.
func wordList(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
}

// hasRun diz se as palavras de run aparecem em words, seguidas.
func hasRun(words, run []string) bool {
	for i := 0; i+len(run) <= len(words); i++ {
		if slices.Equal(words[i:i+len(run)], run) {
			return true
		}
	}
	return false
}

// carriedOver diz se a correção fixed trouxe de volta um pedaço de uma frase
// anterior (earlier). O corretor que guarda a conversa faz isso com frases
// cortadas pela pausa: recebe "integration with the bank until Friday." e
// devolve "Renato was supposed to review the integration with the bank
// until Friday.", com o começo que já tinha sido falado na frase de antes. A
// reunião ouviria o começo duas vezes.
//
// Conta como trazido de volta um trecho de 3 ou mais palavras seguidas, igual
// ao de uma frase anterior, que não está na tradução literal desta (en) e que
// o português desta (pt) não comporta: a correção ficou maior que ele mais ou
// menos do tamanho do trecho. Recuperar o que o Whisper perdeu não cai aqui:
// a correção fica do tamanho do português.
func carriedOver(pt, en, fixed string, earlier []string) bool {
	f, literal, ptWords := wordList(fixed), wordList(en), len(wordList(pt))
	longest := 0
	for _, e := range earlier {
		prev := wordList(e)
		// run[j] é quantas palavras seguidas terminam iguais em f[i] e prev[j]
		run := make([]int, len(prev)+1)
		for i := range f {
			for j := len(prev); j >= 1; j-- {
				if f[i] != prev[j-1] {
					run[j] = 0
					continue
				}
				run[j] = run[j-1] + 1
				if n := run[j]; n >= 3 && n > longest && !hasRun(literal, f[i+1-n:i+1]) {
					longest = n
				}
			}
		}
	}
	return longest >= 3 && len(f)-ptWords >= longest-1 && float64(len(f)) >= 1.3*float64(ptWords)
}

// OllamaCorrector corrige com o Ollama, que fica carregado por 30 minutos
// depois da última frase (carregar de novo leva uns 5 s).
type OllamaCorrector struct {
	Endpoint string
	Model    func() string
}

func (o *OllamaCorrector) Name() string { return "Ollama" }
func (o *OllamaCorrector) Close()       {}

func (o *OllamaCorrector) Correct(ctx context.Context, pt, en string) (string, error) {
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	err := o.chat(ctx, 20*time.Second, []map[string]string{
		{"role": "system", "content": correctPrompt},
		{"role": "user", "content": correctMessage(pt, en)},
	}, &out)
	if err != nil {
		return "", err
	}
	fixed := tidy(out.Message.Content)
	return fixed, plausible(pt, en, fixed)
}

// Warmup carrega o modelo na memória (uma conversa vazia).
func (o *OllamaCorrector) Warmup() error {
	return o.chat(context.Background(), 90*time.Second, []map[string]string{}, nil)
}

func (o *OllamaCorrector) chat(ctx context.Context, timeout time.Duration, messages []map[string]string, out any) error {
	body, _ := json.Marshal(map[string]any{
		"model": o.Model(), "stream": false, "think": false, "keep_alive": "30m",
		"options":  map[string]any{"temperature": 0},
		"messages": messages,
	})
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", o.Endpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("o Ollama não respondeu: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("erro do Ollama: %s", e.Error)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ClaudeCorrector corrige com o Claude Haiku pelo claude (o CLI do Claude
// Code, com o login do usuário), num processo que fica aberto enquanto o
// Intérprete está ligado (ver chat.ClaudeSession): ~1,5 s por frase, contra
// ~4 s abrindo um processo para cada uma.
type ClaudeCorrector struct {
	session *chat.ClaudeSession

	// A conversa do claude guarda as frases anteriores, e às vezes ele
	// devolve uma delas junto com a frase nova (carriedOver)
	mu      sync.Mutex
	earlier []string // o inglês das últimas frases: a tradução literal e a correção aceita
}

// earlierKept é quantos textos anteriores o ClaudeCorrector compara.
const earlierKept = 8

// ClaudeCorrectorModel: o mais rápido, e suficiente para corrigir uma frase.
const ClaudeCorrectorModel = "haiku"

func NewClaudeCorrector(path string) *ClaudeCorrector {
	session := chat.NewClaudeSession(path, ClaudeCorrectorModel, correctPrompt)
	// Uma frase de exemplo a cada processo novo: sem ela, a primeira frase de
	// verdade esperava ~2,7 s pela correção, em vez de ~1 s
	session.Prime = correctMessage("Bom dia, pessoal.", "Good morning, everyone.")
	return &ClaudeCorrector{session: session}
}

func (c *ClaudeCorrector) Name() string { return "Claude Haiku" }

// Warmup abre o claude e manda a frase de exemplo (leva uns segundos; o app
// chama em segundo plano ao ligar o Intérprete).
func (c *ClaudeCorrector) Warmup() error { return c.session.Start() }
func (c *ClaudeCorrector) Close()        { c.session.Close() }

func (c *ClaudeCorrector) Correct(ctx context.Context, pt, en string) (string, error) {
	answer, err := c.session.SendContext(ctx, correctMessage(pt, en))
	if err != nil {
		return "", err
	}
	return c.accept(pt, en, answer)
}

// accept devolve a correção que veio em answer, ou o erro que a descarta
// (errImplausible): não parece a mesma frase, ou repete uma anterior.
func (c *ClaudeCorrector) accept(pt, en, answer string) (string, error) {
	fixed := tidy(answer)
	err := plausible(pt, en, fixed)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && carriedOver(pt, en, fixed, c.earlier) {
		err = fmt.Errorf("%w: repetiu uma frase anterior: %q", errImplausible, fixed)
	}
	// O que foi para a reunião desta frase: a correção, ou a literal se ela
	// foi descartada
	c.earlier = append(c.earlier, en)
	if err == nil {
		c.earlier = append(c.earlier, fixed)
	}
	c.earlier = c.earlier[max(len(c.earlier)-earlierKept, 0):]
	return fixed, err
}
