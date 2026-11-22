package interpreter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// O Intérprete só fala o que você disse. Estes testes são dos casos em que
// ele falava outra coisa (13/11/2026): uma frase inventada pelo Whisper sobre
// um aperto sem fala, e o começo da frase anterior repetido pela correção.

// click põe um estalo de 30 ms (a tecla do atalho, um clique) no áudio.
func click(audio []int16, at float64) []int16 {
	start := int(at * WhisperRate)
	for i := start; i < start+frameLen && i < len(audio); i++ {
		audio[i] = 12000
		if i%2 == 1 {
			audio[i] = -12000
		}
	}
	return audio
}

func TestSegmenterDropsSilenceWithAClick(t *testing.T) {
	// Soltou o atalho sem falar, e o microfone captou o estalo da tecla
	s := NewSegmenter()
	if got := feedAll(s, click(tone(2.5, false), 2.2)); len(got) != 0 {
		t.Fatalf("silêncio com um estalo não deveria fechar frase: %d", len(got))
	}
	if u := s.Flush(); u != nil {
		t.Fatalf("silêncio com um estalo não é frase: %.1f s iriam para o Whisper", float64(len(u))/WhisperRate)
	}
	// Segurando o atalho muito tempo, com estalos no meio do silêncio
	s = NewSegmenter()
	long := click(click(tone(maxUtterance+1, false), 3), 9)
	if got := feedAll(s, long); len(got) != 0 {
		t.Fatalf("silêncio longo com estalos não deveria virar frase: %d", len(got))
	}
	// Uma palavra curta e baixa continua indo para o Whisper
	s = NewSegmenter()
	feedAll(s, append(tone(0.5, false), speechAt(0.3, true, 250)...))
	if s.Flush() == nil {
		t.Fatal("uma palavra curta deveria ir para o Whisper")
	}
}

func TestSpokenTextDropsWhatWasNotSpoken(t *testing.T) {
	for text, want := range map[string]string{
		" [silêncio]":                      "",
		"[BLANK_AUDIO]":                    "",
		"(música) ♪♪":                      "",
		"*risos*":                          "",
		" [silence] Bye.":                  "Bye.",
		" Vamos fazer o  deploy  (risos) ": "Vamos fazer o deploy",
		"Acho que quem tá acompanhando é o Mauro.": "Acho que quem tá acompanhando é o Mauro.",
	} {
		if got := spokenText(text); got != want {
			t.Errorf("spokenText(%q) = %q, quero %q", text, got, want)
		}
	}
}

// O que o whisper-server do app respondeu ao ruído do microfone do fone, sem
// ninguém falando: "[silêncio]" na transcrição e "Thank you for watching!" na
// tradução. A frase inventada ia para a reunião.
func TestInterpreterDoesNotSpeakWhatWhisperMadeUp(t *testing.T) {
	haiku := &fakeCorrector{name: "Claude Haiku", fixed: "Thank you for watching!"}
	out, voice, statuses := runInterpreter(t, "sem-fala", tone(1.2, true), haiku)
	if len(voice.said) != 0 || len(out.speech) != 0 {
		t.Fatalf("não deveria falar nada: %q", voice.said)
	}
	if haiku.calls() != 0 {
		t.Fatalf("o que não é fala não vai para a correção: %q", haiku.got)
	}
	if out.busy {
		t.Fatal("a sua voz deveria voltar para a reunião")
	}
	if len(statuses) == 0 || !strings.Contains(statuses[len(statuses)-1].Message, "Não entendi nenhuma fala") {
		t.Fatalf("a tela deveria explicar: %+v", statuses)
	}
}

func TestTidyKeepsOnlyThePhrase(t *testing.T) {
	for answer, want := range map[string]string{
		" I think it's Mauro.\n":                      "I think it's Mauro.",
		"EN: integration with the bank until Friday.": "integration with the bank until Friday.",
		`"by parachute together with me."`:            "by parachute together with me.",
		"“Good morning, everyone.”":                   "Good morning, everyone.",
		`He said "no" and left.`:                      `He said "no" and left.`,
		`"Yes" or "no"?`:                              `"Yes" or "no"?`,
	} {
		if got := tidy(answer); got != want {
			t.Errorf("tidy(%q) = %q, quero %q", answer, got, want)
		}
	}
}

// As respostas são as do Claude Haiku de verdade a frases cortadas pela pausa
// (13/11/2026, e a do log de 12/11/2026): com a conversa guardada, ele
// completa a frase nova com o começo que já tinha sido falado.
func TestClaudeCorrectorDropsWhatWasAlreadySaid(t *testing.T) {
	c := &ClaudeCorrector{}
	if got, err := c.accept("O Renato ficou de revisar a", "Renato was supposed to review the", "Renato was supposed to review the"); err != nil || got != "Renato was supposed to review the" {
		t.Fatalf("1ª frase: %q, %v", got, err)
	}
	_, err := c.accept("integração com o banco até sexta.", "integration with the bank until Friday.",
		"Renato was supposed to review the integration with the bank until Friday.")
	if err == nil || !strings.Contains(err.Error(), "repetiu uma frase anterior") {
		t.Fatalf("a correção que repete o começo já falado deveria ser descartada: %v", err)
	}
	// A frase seguinte, corrigida direito, passa
	if got, err := c.accept("Mas eu acho que não vai dar tempo.", "But I think it won't be time.", "But I think there won't be time."); err != nil || got != "But I think there won't be time." {
		t.Fatalf("3ª frase: %q, %v", got, err)
	}

	c = &ClaudeCorrector{}
	c.accept("Esse novato caiu na...", "This Novato fell in...", "This novice fell in...")
	if _, err := c.accept("de paraquedas junto comigo.", "of parachute with me.", "This novice fell in... of parachute with me."); err == nil {
		t.Fatal("a frase do log de 12/11 deveria ser descartada")
	}

	// Quem repetiu foi você: o português desta frase tem o trecho
	c = &ClaudeCorrector{}
	c.accept("A gente precisa fazer o deploy hoje.", "We need to deploy today.", "We need to deploy today.")
	if got, err := c.accept("A gente precisa fazer o deploy em produção amanhã cedo.", "We need to tomorrow early.", "We need to deploy to production early tomorrow."); err != nil {
		t.Fatalf("recuperar o que o Whisper perdeu é uma correção válida: %q, %v", got, err)
	}
	// E a mesma frase dita de novo, que o Whisper traduziu mal da 2ª vez
	if got, err := c.accept("A gente precisa fazer o deploy hoje.", "We need to the ploy today.", "We need to deploy today."); err != nil {
		t.Fatalf("a mesma frase dita de novo é válida: %q, %v", got, err)
	}
}

func TestInterpreterKeepsTheAudioOfTheLastPhrases(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "interprete")
	in := New(Config{KeepDir: dir}, &fakeOutput{})
	defer in.Close()
	start := time.Date(2026, 11, 13, 10, 30, 0, 0, time.Local)
	for n := 0; n < keptPhrases+3; n++ {
		in.keep(tone(0.5, true), start.Add(time.Duration(n)*time.Second))
	}
	kept, _ := filepath.Glob(filepath.Join(dir, "*.wav"))
	if len(kept) != keptPhrases {
		t.Fatalf("deveriam ficar as %d últimas frases, ficaram %d", keptPhrases, len(kept))
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-11-13_10-30-00.000.wav")); err == nil {
		t.Fatal("a frase mais antiga deveria ter sido apagada")
	}
	last := filepath.Join(dir, "2026-11-13_10-30-22.000.wav")
	if info, err := os.Stat(last); err != nil || info.Size() != 44+2*WhisperRate/2 {
		t.Fatalf("a última frase deveria estar guardada inteira: %v", err)
	}
}
