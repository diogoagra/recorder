package audio

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// SessionFile é onde fica o áudio de uma sessão: m4a no macOS e wav no
// Windows, que não tem o afconvert.
func SessionFile(dir, sessionID string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, sessionID+".wav")
	}
	return filepath.Join(dir, sessionID+".m4a")
}

// LoadFile lê um m4a ou wav como amostras mono de 16kHz.
func LoadFile(path string) ([]int16, error) {
	if strings.HasSuffix(strings.ToLower(path), ".wav") {
		return LoadWav(path)
	}
	tmp, err := os.CreateTemp("", "recorder-*.wav")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if out, err := exec.Command("afconvert", "-f", "WAVE", "-d", "LEI16@16000", "-c", "1", path, tmp.Name()).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("falha ao converter %s: %v: %s", path, err, out)
	}
	return LoadWav(tmp.Name())
}

// AppendSession grava samples no áudio da sessão, depois do que já estava
// salvo dela, e devolve o arquivo gravado. Antes, ao pausar e retomar uma
// sessão, o arquivo era sobrescrito só com a parte nova e o começo da gravação
// se perdia.
func AppendSession(dir, sessionID string, samples []int16) (string, error) {
	if len(samples) == 0 {
		return SessionFile(dir, sessionID), nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	fresh := filepath.Join(dir, sessionID+"_novo_tmp.wav")
	if err := SaveWav(fresh, samples, sampleRate); err != nil {
		return "", err
	}
	defer os.Remove(fresh)
	return appendSessionWav(dir, sessionID, fresh)
}

// AppendSessionRaw é como AppendSession, com o áudio bruto de rawPath (ver
// AppendRaw). O áudio passa de arquivo para arquivo, sem ser carregado na
// memória: no fim de uma reunião de 2h eram mais de 1 GB.
func AppendSessionRaw(dir, sessionID, rawPath string) (string, error) {
	if info, err := os.Stat(rawPath); err != nil {
		return "", err
	} else if info.Size() < 2 {
		return SessionFile(dir, sessionID), nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	fresh := filepath.Join(dir, sessionID+"_novo_tmp.wav")
	if err := rawToWav(rawPath, fresh); err != nil {
		return "", err
	}
	defer os.Remove(fresh)
	return appendSessionWav(dir, sessionID, fresh)
}

// appendSessionWav acrescenta o WAV fresh ao áudio da sessão. O arquivo da
// sessão só é trocado no fim, por um já pronto, para uma falha no meio não
// estragar o áudio que já existia.
//
// No macOS só o trecho novo é comprimido; ele é juntado ao que já estava
// salvo copiando os pacotes de áudio (concatCompressed). Antes, a cada
// retomada a sessão inteira era decodificada e comprimida de novo, e o começo
// da reunião perdia um pouco de qualidade a cada vez.
func appendSessionWav(dir, sessionID, fresh string) (string, error) {
	target := SessionFile(dir, sessionID)
	_, statErr := os.Stat(target)
	exists := statErr == nil

	if strings.HasSuffix(target, ".wav") {
		if !exists {
			return target, os.Rename(fresh, target)
		}
		joined := filepath.Join(dir, sessionID+"_tmp.wav")
		if err := concatWav([]string{target, fresh}, joined); err != nil {
			return keepAsPart(dir, sessionID, fresh, err)
		}
		return target, os.Rename(joined, target)
	}

	encoded := filepath.Join(dir, sessionID+"_novo_tmp.m4a")
	if err := encodeAAC(fresh, encoded); err != nil {
		return "", err
	}
	if !exists {
		return target, os.Rename(encoded, target)
	}
	defer os.Remove(encoded)

	joined := filepath.Join(dir, sessionID+"_tmp.mp4")
	err := concatCompressed([]string{target, encoded}, joined)
	if err == nil {
		// Como no arquivo que o afconvert gera, a data de criação é o fim da gravação
		err = setMP4Created(joined, time.Now())
	}
	if err != nil {
		log.Printf("Aviso: não foi possível juntar o áudio novo ao da sessão sem recomprimir (%v); recomprimindo a sessão inteira", err)
		os.Remove(joined)
		joined = filepath.Join(dir, sessionID+"_tmp.m4a")
		err = reencodeWith(dir, sessionID, target, fresh, joined)
	}
	if err != nil {
		os.Remove(joined)
		return keepAsPart(dir, sessionID, encoded, err)
	}
	return target, os.Rename(joined, target)
}

// encodeAAC comprime o WAV em m4a (AAC a 32 kbps, o bastante para voz a 16kHz).
func encodeAAC(wavPath, m4aPath string) error {
	if out, err := exec.Command("afconvert", "-f", "m4af", "-d", "aac", "-b", "32000", wavPath, m4aPath).CombinedOutput(); err != nil {
		os.Remove(m4aPath)
		return fmt.Errorf("erro ao converter para m4a: %v: %s", err, out)
	}
	return nil
}

// reencodeWith é o caminho antigo, para quando a junção sem recomprimir
// falha: decodifica o áudio já salvo da sessão, junta o trecho novo e
// comprime tudo de novo em out. Tudo de arquivo para arquivo.
func reencodeWith(dir, sessionID, target, fresh, out string) error {
	previous := filepath.Join(dir, sessionID+"_antes_tmp.wav")
	defer os.Remove(previous)
	if o, err := exec.Command("afconvert", "-f", "WAVE", "-d", "LEI16@16000", "-c", "1", target, previous).CombinedOutput(); err != nil {
		return fmt.Errorf("falha ao converter %s: %v: %s", target, err, o)
	}
	all := filepath.Join(dir, sessionID+"_tudo_tmp.wav")
	defer os.Remove(all)
	if err := concatWav([]string{previous, fresh}, all); err != nil {
		return err
	}
	return encodeAAC(all, out)
}

// keepAsPart guarda o trecho novo num arquivo à parte, que a sessão também
// lista: sem conseguir ler ou juntar o áudio que já existe, ele não é
// sobrescrito.
func keepAsPart(dir, sessionID, fresh string, cause error) (string, error) {
	part := filepath.Join(dir, fmt.Sprintf("%s_part_%d%s", sessionID, time.Now().Unix(), filepath.Ext(fresh)))
	log.Printf("Aviso: não foi possível juntar o áudio novo ao já salvo da sessão (%v); salvando a parte nova em %s", cause, part)
	return part, os.Rename(fresh, part)
}

// SessionDuration devolve quanto áudio a sessão já tem salvo, em segundos.
func SessionDuration(dir, sessionID string) float64 {
	path := SessionFile(dir, sessionID)
	if _, err := os.Stat(path); err != nil {
		return 0
	}
	_, duration, err := Info(path)
	if err != nil {
		log.Printf("Aviso: não foi possível ler a duração do áudio da sessão %s: %v", sessionID, err)
		return 0
	}
	return duration.Seconds()
}
