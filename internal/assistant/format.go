package assistant

import (
	"fmt"
	"recorder/internal/storage"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// transcriptLines formata as falas para o Ollama, com o momento do áudio
// ("[12:34] [Pessoa 3]: ...") para o resumo e o chat poderem citá-lo.
func transcriptLines(transcripts []storage.Transcript) []string {
	lines := make([]string, len(transcripts))
	for i, t := range transcripts {
		lines[i] = transcriptLine(t)
	}
	return lines
}

// Colchetes só no momento: com o rótulo também entre colchetes ("[Pessoa 3]"),
// o modelo imitava esse formato e deixava de citar os momentos.
func transcriptLine(t storage.Transcript) string {
	if t.Offset < 0 {
		return fmt.Sprintf("%s: %s\n", t.Who(), t.Text)
	}
	return fmt.Sprintf("[%s] %s: %s\n", FormatOffset(t.Offset), t.Who(), t.Text)
}

// FormatOffset escreve segundos como "12:34" (ou "1:02:03" a partir de uma hora).
func FormatOffset(seconds float64) string {
	total := int(seconds)
	if h := total / 3600; h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, total%3600/60, total%60)
	}
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}

// parseOffset lê "12:34", "1:02:03" ou "[12:34]" em segundos; -1 se não for um momento.
func parseOffset(text string) float64 {
	parts := strings.Split(strings.Trim(strings.TrimSpace(text), "[]"), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return -1
	}
	total := 0
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (i > 0 && n > 59) {
			return -1
		}
		total = total*60 + n
	}
	return float64(total)
}

// chunkLines junta as linhas em partes de até budget caracteres, sem quebrar
// linhas, a não ser as que sozinhas passam do limite (ex: um monólogo longo).
func chunkLines(lines []string, budget int) []string {
	var parts []string
	var cur strings.Builder
	for _, line := range lines {
		for _, piece := range splitLong(line, budget) {
			if cur.Len() > 0 && cur.Len()+len(piece) > budget {
				parts = append(parts, cur.String())
				cur.Reset()
			}
			cur.WriteString(piece)
		}
	}
	if cur.Len() > 0 || len(parts) == 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

// splitLong quebra text em pedaços de até budget bytes, de preferência depois
// de um ponto final ou de um espaço.
func splitLong(text string, budget int) []string {
	var pieces []string
	for len(text) > budget {
		cut := strings.LastIndex(text[:budget], ". ")
		if cut < budget/2 {
			cut = strings.LastIndex(text[:budget], " ")
		}
		if cut <= 0 {
			cut = budget
			for !utf8.RuneStart(text[cut]) {
				cut--
			}
		} else {
			cut++
		}
		pieces = append(pieces, text[:cut]+"\n")
		text = text[cut:]
	}
	return append(pieces, text)
}

// recentWithin devolve as falas mais recentes que cabem em budget caracteres.
func recentWithin(transcripts []storage.Transcript, budget int) []storage.Transcript {
	used := 0
	for i := len(transcripts) - 1; i >= 0; i-- {
		used += len(transcripts[i].Text) + 16
		if used > budget {
			return transcripts[i+1:]
		}
	}
	return transcripts
}

// chunkTranscripts divide as falas em grupos seguidos de até budget
// caracteres, devolvendo o início e o fim (exclusivo) de cada grupo.
func chunkTranscripts(transcripts []storage.Transcript, budget int) [][2]int {
	var chunks [][2]int
	start, used := 0, 0
	for i, t := range transcripts {
		size := len(transcriptLine(t))
		if i > start && used+size > budget {
			chunks = append(chunks, [2]int{start, i})
			start, used = i, 0
		}
		used += size
	}
	if start < len(transcripts) {
		chunks = append(chunks, [2]int{start, len(transcripts)})
	}
	return chunks
}

// normalizeWords deixa só as palavras, em minúsculas, separadas por um espaço.
func normalizeWords(text string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

// WriteTranscripts escreve as falas em b com um título, no formato usado nos pedidos ao Ollama.
func WriteTranscripts(b *strings.Builder, title string, transcripts []storage.Transcript) {
	if len(transcripts) == 0 {
		return
	}
	fmt.Fprintf(b, "--- %s ---\n", title)
	for _, line := range transcriptLines(transcripts) {
		b.WriteString(line)
	}
	b.WriteString("--------------------\n\n")
}
