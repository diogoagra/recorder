package backend

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"recorder/internal/datadir"
	"recorder/internal/interpreter"
)

// O servidor local de arquivos entrega à janela do app o que ela toca e
// mostra: o áudio das sessões, em /capturas/ as capturas de tela delas e, em
// /voz/, as opções da calibração do Intérprete. Só escuta em 127.0.0.1.
//
// Uma porta local é alcançada por qualquer programa do computador (de qualquer
// usuário dele), e uma página aberta no navegador também consegue tentar. Por
// isso:
//
//   - o endereço leva um segredo sorteado a cada abertura do app, que só a
//     janela recebe (GetAudioServerURL). Sem ele, tudo responde 404;
//   - só são atendidos pedidos feitos a 127.0.0.1:<porta>: um site que aponte
//     o próprio nome para 127.0.0.1 chega com outro Host;
//   - o servidor entrega arquivos, nunca a lista de uma pasta. Antes, a raiz
//     listava o áudio de todas as reuniões.

// filesOnly é uma pasta servida sem a listagem do conteúdo.
type filesOnly struct{ dir http.Dir }

func (f filesOnly) Open(name string) (http.File, error) {
	file, err := f.dir.Open(name)
	if err != nil {
		return nil, err
	}
	if info, err := file.Stat(); err != nil || info.IsDir() {
		file.Close()
		return nil, os.ErrNotExist
	}
	return file, nil
}

// startFileServer sobe o servidor local de arquivos numa porta livre.
func (a *App) startFileServer() {
	audioDir := datadir.SessionsAudio()
	os.MkdirAll(audioDir, 0755)

	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		log.Printf("Aviso: sem o servidor local de arquivos (o áudio e as capturas não aparecem na tela): %v", err)
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Printf("Aviso: sem o servidor local de arquivos (o áudio e as capturas não aparecem na tela): %v", err)
		return
	}
	token := hex.EncodeToString(secret)
	host := listener.Addr().String()

	files := http.NewServeMux()
	files.Handle("/", http.FileServer(filesOnly{http.Dir(audioDir)}))
	files.Handle(screenshotsPath, http.StripPrefix(screenshotsPath, http.FileServer(filesOnly{http.Dir(screenshotsRoot())})))
	calibration := interpreter.NewVoice(interpreter.DefaultVoiceDir()).CalibrationDir()
	files.Handle("/voz/", http.StripPrefix("/voz/", http.FileServer(filesOnly{http.Dir(calibration)})))

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// O primeiro trecho do caminho é o segredo
		given, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if r.Host != host || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + rest
		r2.URL.RawPath = ""
		files.ServeHTTP(w, r2)
	})

	a.fileServerMu.Lock()
	a.fileServerURL = fmt.Sprintf("http://%s/%s", host, token)
	a.fileServerMu.Unlock()
	go http.Serve(listener, handler)
}

// GetAudioServerURL devolve à janela o endereço do servidor local de arquivos,
// já com o segredo: o áudio da sessão fica em <endereço>/<arquivo>, e as
// capturas e as opções da calibração, nos caminhos que o app informa. Vazio se
// o servidor não subiu.
func (a *App) GetAudioServerURL() string {
	a.fileServerMu.RLock()
	defer a.fileServerMu.RUnlock()
	return a.fileServerURL
}
