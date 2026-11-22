package audio

import (
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Enquanto uma sessão grava, ficam na pasta temporária o áudio bruto da
// gravação (session_<id>.pcm), que só vira o áudio da sessão ao parar de
// gravar, e os blocos esperando transcrição (block_<id>_<ms>.wav, com a
// posição do bloco no áudio da sessão). Se o app fecha ou trava no meio, esses
// arquivos ficam para trás e Recover os aproveita na próxima vez que ele abrir.

// RawFile é onde fica o áudio bruto (16 bits, mono, 16kHz) que está sendo
// gravado para a sessão.
func RawFile(tmpDir, sessionID string) string {
	return filepath.Join(tmpDir, "session_"+sessionID+".pcm")
}

// BlockFile é onde fica um bloco gravado até ser transcrito. offset é onde o
// bloco começa no áudio da sessão, em segundos.
func BlockFile(tmpDir, sessionID string, offset float64) string {
	return filepath.Join(tmpDir, fmt.Sprintf("block_%s_%d.wav", sessionID, int64(math.Round(offset*1000))))
}

// SessionBlocks lista os blocos da sessão que estão em tmpDir, esperando
// transcrição.
func SessionBlocks(tmpDir, sessionID string) []string {
	blocks, _ := filepath.Glob(filepath.Join(tmpDir, "block_"+sessionID+"_*.wav"))
	return blocks
}

// parseBlockFile lê a sessão e a posição do nome de um arquivo de BlockFile.
func parseBlockFile(path string) (sessionID string, offset float64, ok bool) {
	name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "block_"), ".wav")
	i := strings.LastIndex(name, "_")
	if i <= 0 {
		return "", 0, false
	}
	ms, err := strconv.ParseInt(name[i+1:], 10, 64)
	if err != nil || ms < 0 {
		return "", 0, false
	}
	return name[:i], float64(ms) / 1000, true
}

// AppendRaw acrescenta samples ao áudio bruto em path.
func AppendRaw(path string, samples []int16) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	raw := make([]byte, len(samples)*2)
	for i, s := range samples {
		raw[i*2] = byte(s)
		raw[i*2+1] = byte(s >> 8)
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// LoadRaw lê o áudio bruto gravado por AppendRaw.
func LoadRaw(path string) ([]int16, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeS16(raw, len(raw)/2), nil
}

// Interrupted é uma gravação que o app não terminou de processar. O áudio dela
// já foi juntado ao da sessão; faltam transcrever Blocks e fazer o resumo.
type Interrupted struct {
	SessionID string
	Blocks    []PendingBlock // em ordem
}

// PendingBlock é um bloco gravado que ainda espera transcrição.
type PendingBlock struct {
	Path   string
	Offset float64 // onde o bloco começa no áudio da sessão, em segundos
}

// Recover salva o que ficou em tmpDir de gravações interrompidas: junta o
// áudio bruto ao áudio da sessão em sessionsDir e devolve os blocos que
// faltaram transcrever. exists diz se a sessão ainda existe; create cria uma
// sessão para o áudio de uma sessão que não existe mais (ou de uma versão
// antiga do app, que não guardava a sessão no nome), a partir de quando a
// gravação começou. Precisa rodar antes de o app voltar a gravar.
func Recover(tmpDir, sessionsDir string, exists func(sessionID string) bool, create func(start time.Time) (string, error)) []Interrupted {
	// Restos de um AppendSession interrompido; o áudio deles ainda está no
	// arquivo bruto
	for _, pattern := range []string{"*_tmp.wav", "*_tmp.m4a", "*_tmp.mp4"} {
		junk, _ := filepath.Glob(filepath.Join(sessionsDir, pattern))
		for _, path := range junk {
			os.Remove(path)
		}
	}

	interrupted := map[string]bool{}
	raws, _ := filepath.Glob(filepath.Join(tmpDir, "*.pcm"))
	for _, path := range raws {
		id, err := recoverRaw(path, tmpDir, sessionsDir, exists, create)
		if err != nil {
			log.Printf("Erro ao recuperar a gravação interrompida %s: %v", path, err)
			continue
		}
		if id != "" {
			log.Printf("Áudio de uma gravação interrompida salvo na sessão %s", id)
			interrupted[id] = true
		}
	}

	pending := map[string][]PendingBlock{}
	blocks, _ := filepath.Glob(filepath.Join(tmpDir, "block_*.wav"))
	for _, path := range blocks {
		id, offset, ok := parseBlockFile(path)
		if !ok || !exists(id) {
			// Bloco de uma sessão apagada ou de uma versão antiga do app, sem
			// a sessão no nome
			os.Remove(path)
			continue
		}
		pending[id] = append(pending[id], PendingBlock{Path: path, Offset: offset})
		interrupted[id] = true
	}

	var out []Interrupted
	for id := range interrupted {
		b := pending[id]
		sort.Slice(b, func(i, j int) bool { return b[i].Offset < b[j].Offset })
		out = append(out, Interrupted{SessionID: id, Blocks: b})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

// recoverRaw junta ao áudio da sessão o áudio bruto em path e devolve a sessão
// ("" se o arquivo estava vazio).
func recoverRaw(path, tmpDir, sessionsDir string, exists func(string) bool, create func(time.Time) (string, error)) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() < 2 {
		return "", os.Remove(path)
	}
	// O arquivo bruto é escrito a cada bloco: a última escrita é o fim da gravação
	end := info.ModTime()

	name := filepath.Base(path)
	id := strings.TrimSuffix(strings.TrimPrefix(name, "session_"), ".pcm")
	if !strings.HasPrefix(name, "session_") || !exists(id) {
		duration := time.Duration(info.Size()/2) * time.Second / sampleRate
		if id, err = create(end.Add(-duration)); err != nil {
			return "", err
		}
		// Renomeia antes do resto: se algo falhar daqui em diante, a próxima
		// tentativa junta o áudio a esta sessão em vez de criar outra
		renamed := RawFile(tmpDir, id)
		if err := os.Rename(path, renamed); err != nil {
			return "", err
		}
		path = renamed
		// Nada desse áudio foi transcrito: divide em blocos como na gravação
		samples, err := LoadRaw(path)
		if err != nil {
			return "", err
		}
		var offset float64
		for _, block := range SplitBlocks(samples, 30*time.Second) {
			if err := SaveWav(BlockFile(tmpDir, id, offset), block, sampleRate); err != nil {
				return "", err
			}
			offset += float64(len(block)) / sampleRate
		}
	}

	saved, err := AppendSessionRaw(sessionsDir, id, path)
	if err != nil {
		return "", err
	}
	// O arquivo convertido agora teria como data de gravação o momento da conversão
	if err := SetRecordedAt(saved, end); err != nil {
		log.Printf("Aviso: não foi possível ajustar a data de gravação de %s: %v", saved, err)
	}
	return id, os.Remove(path)
}
