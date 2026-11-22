package diarization

import (
	"archive/tar"
	"compress/bzip2"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// model é um arquivo .onnx usado pelo sherpa-onnx. Na primeira vez que o app
// abre ele é baixado do GitHub do sherpa-onnx e conferido pelo SHA-256.
type model struct {
	file   string // nome do arquivo salvo em modelsDir
	url    string
	member string // caminho do .onnx dentro do .tar.bz2; vazio se o download já é o .onnx
	sha256 string // do .onnx final
}

var (
	// Segmentação do pyannote 3.0: descobre os trechos com fala e as trocas de locutor.
	segmentationModel = model{
		file:   "pyannote-segmentation-3-0.onnx",
		url:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-segmentation-models/sherpa-onnx-pyannote-segmentation-3-0.tar.bz2",
		member: "sherpa-onnx-pyannote-segmentation-3-0/model.onnx",
		sha256: "220ad67ca923bef2fa91f2390c786097bf305bceb5e261d4af67b38e938e1079",
	}
	// "Impressão digital" da voz do 3D-Speaker (ERes2Net). Comparado com outros
	// modelos do sherpa-onnx, foi o que melhor separou vozes diferentes por
	// similaridade de cosseno, inclusive da mesma pessoa falando outra língua.
	//
	// O arquivo foi republicado pelo sherpa-onnx em 19/11/2026 e o SHA-256
	// mudou (era 1a331345...e4b): o download deixou de ser aceito e, numa
	// instalação nova, as falas ficavam sem identificação. Comparados byte a
	// byte, os dois só diferem no texto da versão do exportador, no cabeçalho
	// ("2.4.1" virou "2.14.1"); o grafo e os pesos são os mesmos.
	embeddingModel = model{
		file:   "3dspeaker_speech_eres2net_base_sv_zh-cn_3dspeaker_16k.onnx",
		url:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/3dspeaker_speech_eres2net_base_sv_zh-cn_3dspeaker_16k.onnx",
		sha256: "f1ee61be04000b50392a63442e5dd9feaea7e81dddd494d3647cf1c3d88a1498",
	}
)

// ensure devolve o caminho do modelo em dir, baixando-o se ainda não existir.
func (m model) ensure(dir string) (string, error) {
	path := filepath.Join(dir, m.file)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	log.Printf("-> Baixando modelo de identificação de locutores %s...", m.file)
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(m.url)
	if err != nil {
		return "", fmt.Errorf("falha ao baixar %s: %w", m.file, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("falha ao baixar %s: status %d", m.file, resp.StatusCode)
	}

	var src io.Reader = resp.Body
	if m.member != "" {
		if src, err = findInTarBz2(resp.Body, m.member); err != nil {
			return "", fmt.Errorf("falha ao extrair %s: %w", m.file, err)
		}
	}

	// Grava num arquivo temporário e só renomeia no fim, para um download
	// interrompido não deixar um modelo pela metade
	tmp, err := os.CreateTemp(dir, m.file+".*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())

	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, hash), src)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("falha ao baixar %s: %w", m.file, err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != m.sha256 {
		return "", fmt.Errorf("checksum inválido para %s: %s", m.file, got)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

func findInTarBz2(r io.Reader, member string) (io.Reader, error) {
	tr := tar.NewReader(bzip2.NewReader(r))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s não encontrado no pacote", member)
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name == member {
			return tr, nil
		}
	}
}
