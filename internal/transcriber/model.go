package transcriber

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// O modelo do Whisper ia dentro do pacote do app e era quase todo o tamanho
// dele: 539 MB de 550. Agora o pacote vai sem o modelo, e o app o baixa para a
// pasta de dados na primeira vez que abre. Como fica fora do pacote, o mesmo
// arquivo serve para as versões seguintes.

// Model é um arquivo de modelo do Whisper e os endereços de onde ele pode
// ser baixado.
type Model struct {
	File   string   // nome do arquivo
	Size   int64    // em bytes
	SHA256 string   // do arquivo inteiro
	URLs   []string // em ordem de preferência; todos entregam o mesmo arquivo
}

// DefaultModel é o modelo que o app usa. O nome, o tamanho e o SHA-256 são os
// mesmos do Makefile (`make model`); TestDefaultModelMatchesTheMakefile
// confere.
var DefaultModel = Model{
	File:   "ggml-medium-q5_0.bin",
	Size:   539212467,
	SHA256: "19fea4b380c3a618ec4723c3eef2eb785ffba0d0538cf43f8f235e7b3b34220f",
	URLs: []string{
		// O arquivo publicado pelo projeto whisper.cpp. A revisão do
		// repositório é fixa: o endereço entrega sempre o mesmo conteúdo.
		"https://huggingface.co/ggerganov/whisper.cpp/resolve/5359861c739e955e79d9a303bcbc70fb988958b1/ggml-medium-q5_0.bin",
		// Cópia anexada ao Release 1.2.0 do Recorder, para o download não
		// depender de um endereço só. As versões seguintes continuam
		// apontando para ela.
		"https://github.com/diogoagra/recorder/releases/download/1.2.0/ggml-medium-q5_0.bin",
	},
}

// Find procura o modelo neste computador e devolve o caminho dele. Olha, nesta
// ordem: a pasta atual (o `wails dev` na raiz do projeto, com o arquivo
// baixado pelo `make model`), o pacote do app (Contents/Resources no macOS,
// ao lado do Recorder.exe no Windows: é onde ele ia até a 1.1.0, e onde fica
// num pacote montado com o modelo dentro) e dir, a pasta de modelos dos dados
// do app, para onde o download vai.
func (m Model) Find(dir string) (string, bool) {
	dirs := []string{"."}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "..", "Resources"), filepath.Dir(exe))
	}
	return m.find(append(dirs, dir)...)
}

func (m Model) find(dirs ...string) (string, bool) {
	for _, dir := range dirs {
		path := filepath.Join(dir, m.File)
		// Só serve com o tamanho certo: um arquivo copiado pela metade faria
		// o whisper-server cair a cada partida
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() || info.Size() != m.Size {
			continue
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		return path, true
	}
	return "", false
}

// Tempos do download; variáveis para os testes não esperarem.
var (
	// Espera antes de tentar de novo um download que falhou: dobra a cada
	// falha seguida em que nada foi baixado
	modelRetryDelay    = 5 * time.Second
	maxModelRetryDelay = time.Minute
	// Sem receber nada por este tempo, a conexão é dada como perdida. O
	// download não tem prazo total: 539 MB levam o que a conexão permitir.
	modelStallTimeout = time.Minute
	// De quanto em quanto tempo o andamento é avisado (à tela)
	modelProgressEvery = 250 * time.Millisecond
	// Com outra instância do app baixando, de quanto em quanto tempo olhar se
	// ela terminou
	modelLockPoll = 3 * time.Second
)

var errModelStalled = errors.New("a conexão parou de responder")

// modelClient não tem prazo para a resposta inteira, só para cada etapa até
// ela começar; depois disso vale o modelStallTimeout.
var modelClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ForceAttemptHTTP2:     true,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("redirecionamentos demais")
		}
		// O Hugging Face e o GitHub mandam para o servidor de arquivos deles,
		// sempre por HTTPS
		if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("redirecionamento para um endereço sem HTTPS")
		}
		return nil
	},
}

// download baixa o modelo para dir e devolve o caminho dele. Tenta cada
// endereço, na ordem. O que já foi baixado fica em dir num arquivo ".part", e
// a tentativa seguinte (deste ou de outro endereço, nesta ou em outra
// execução do app) continua dali. O arquivo só ganha o nome final depois de
// conferido pelo SHA-256: o que está em dir com o nome do modelo é sempre o
// modelo inteiro e certo.
func (m Model) download(ctx context.Context, dir string, progress func(done int64)) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	part := filepath.Join(dir, m.File+".part")
	var firstErr error
	for _, address := range m.URLs {
		err := m.fetch(ctx, address, part, progress)
		if err == nil {
			if err = m.verify(part); err == nil {
				path := filepath.Join(dir, m.File)
				if err := os.Rename(part, path); err != nil {
					return "", err
				}
				return path, nil
			}
			// Não é o modelo: nada do que foi baixado se aproveita
			os.Remove(part)
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		log.Printf("Aviso: o modelo do Whisper não veio de %s: %v", hostOf(address), err)
		if firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", hostOf(address), err)
		}
	}
	if firstErr == nil {
		firstErr = errors.New("nenhum endereço para baixar o modelo")
	}
	return "", firstErr
}

// fetch baixa de address o que falta de part, até o arquivo ter o tamanho do
// modelo.
func (m Model) fetch(parent context.Context, address, part string, progress func(done int64)) error {
	var offset int64
	if info, err := os.Stat(part); err == nil {
		offset = info.Size()
	}
	if offset > m.Size {
		offset = 0 // não é um pedaço deste modelo
	}
	if offset == m.Size {
		progress(offset)
		return nil // só faltava conferir
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stall := time.AfterFunc(modelStallTimeout, cancel)
	defer stall.Stop()
	// O erro de uma conexão cortada pelo prazo acima diz "context canceled":
	// troca pelo motivo de verdade
	explain := func(err error) error {
		if parent.Err() == nil && ctx.Err() != nil {
			return errModelStalled
		}
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	// Sem compressão no caminho: o tamanho e as posições são os do arquivo
	req.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := modelClient.Do(req)
	if err != nil {
		return explain(err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// O arquivo inteiro: o servidor não continua de onde parou
		offset = 0
		if resp.ContentLength >= 0 && resp.ContentLength != m.Size {
			return fmt.Errorf("o arquivo oferecido tem %d bytes, e o modelo tem %d", resp.ContentLength, m.Size)
		}
	case http.StatusPartialContent:
		start, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != offset || total != m.Size {
			return fmt.Errorf("o servidor respondeu com outro trecho ou outro arquivo (%q)", resp.Header.Get("Content-Range"))
		}
	case http.StatusRequestedRangeNotSatisfiable:
		// O que está no disco não é um pedaço do arquivo do servidor
		os.Remove(part)
		return errors.New("o servidor não aceitou continuar o download; ele recomeça do início")
	default:
		return fmt.Errorf("o servidor respondeu %s", resp.Status)
	}

	file, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Truncate(offset); err != nil {
		return err
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return err
	}

	done := offset
	progress(done)
	buf := make([]byte, 256<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			stall.Reset(modelStallTimeout)
			if done+int64(n) > m.Size {
				return errors.New("o servidor mandou mais do que o tamanho do modelo")
			}
			if _, err := file.Write(buf[:n]); err != nil {
				return err
			}
			done += int64(n)
			progress(done)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return explain(readErr)
		}
	}
	if done != m.Size {
		return fmt.Errorf("a conexão caiu em %d de %d bytes", done, m.Size)
	}
	return file.Close()
}

// verify confere o arquivo baixado com o SHA-256 do modelo.
func (m Model) verify(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != m.SHA256 {
		return fmt.Errorf("o arquivo baixado não é o modelo esperado (SHA-256 %s)", got)
	}
	return nil
}

// parseContentRange lê "bytes 100-538/539": o começo do trecho e o tamanho
// do arquivo inteiro.
func parseContentRange(header string) (start, total int64, ok bool) {
	spec, found := strings.CutPrefix(header, "bytes ")
	if !found {
		return 0, 0, false
	}
	span, size, found := strings.Cut(spec, "/")
	if !found {
		return 0, 0, false
	}
	first, _, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, false
	}
	start, err1 := strconv.ParseInt(first, 10, 64)
	total, err2 := strconv.ParseInt(size, 10, 64)
	return start, total, err1 == nil && err2 == nil
}

func hostOf(address string) string {
	if u, err := url.Parse(address); err == nil && u.Host != "" {
		return u.Host
	}
	return address
}

// Estados do modelo (ModelStatus.State).
const (
	ModelReady       = "ready"       // no disco, inteiro
	ModelDownloading = "downloading" // sendo baixado
	ModelWaiting     = "waiting"     // o download falhou; o app tenta de novo sozinho
)

// ModelStatus é o que a tela mostra do modelo.
type ModelStatus struct {
	State string `json:"state"`
	Done  int64  `json:"done"`  // bytes já baixados
	Total int64  `json:"total"` // tamanho do modelo
	Error string `json:"error"` // em "waiting", por que o download parou
}

// Percent é quanto do modelo já está no disco, de 0 a 100.
func (s ModelStatus) Percent() int {
	if s.State == ModelReady {
		return 100
	}
	if s.Total <= 0 {
		return 0
	}
	return int(s.Done * 100 / s.Total)
}

// ModelFile é o arquivo do modelo neste computador: o caminho dele, quando
// está no disco, ou o download em andamento.
type ModelFile struct {
	model    Model
	dir      string
	onChange func(ModelStatus)

	mu       sync.Mutex
	status   ModelStatus
	path     string
	lastEmit time.Time
	logged   int // último décimo do download que foi para o log

	ready  chan struct{} // fechado quando o modelo está no disco
	cancel context.CancelFunc
	done   chan struct{} // fechado quando o download termina ou é parado
}

// FetchModel devolve o modelo já no disco ou, se ele não estiver neste
// computador, começa a baixá-lo para dir em segundo plano, tentando de novo
// enquanto não conseguir. onChange (pode ser nil) recebe cada mudança de
// estado e o andamento do download.
func FetchModel(m Model, dir string, onChange func(ModelStatus)) *ModelFile {
	f := &ModelFile{
		model: m, dir: dir, onChange: onChange,
		status: ModelStatus{State: ModelDownloading, Total: m.Size},
		ready:  make(chan struct{}), done: make(chan struct{}),
		cancel: func() {},
	}
	if path, ok := m.Find(dir); ok {
		f.status.State, f.status.Done, f.path = ModelReady, m.Size, path
		close(f.ready)
		close(f.done)
		return f
	}
	// A tela já abre com o que foi baixado numa execução anterior
	if info, err := os.Stat(filepath.Join(dir, m.File+".part")); err == nil && info.Size() <= m.Size {
		f.status.Done = info.Size()
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	go f.run(ctx)
	return f
}

// LocalModel é um modelo que já está no disco, em path.
func LocalModel(path string) *ModelFile {
	f := &ModelFile{
		status: ModelStatus{State: ModelReady},
		path:   path,
		ready:  make(chan struct{}), done: make(chan struct{}),
		cancel: func() {},
	}
	close(f.ready)
	close(f.done)
	return f
}

func (f *ModelFile) run(ctx context.Context) {
	defer close(f.done)
	log.Printf("-> O modelo do Whisper (%s) não está neste computador; baixando para %s...", f.model.File, f.dir)
	delay := modelRetryDelay
	for {
		before := f.Status().Done
		path, err := f.downloadAlone(ctx)
		if err == nil {
			log.Printf("-> Modelo do Whisper pronto em %s", path)
			f.set(ModelStatus{State: ModelReady, Done: f.model.Size, Total: f.model.Size}, path)
			close(f.ready)
			return
		}
		if ctx.Err() != nil {
			return
		}
		status := f.Status()
		if status.Done > before {
			delay = modelRetryDelay // a conexão andou: não é hora de espaçar as tentativas
		}
		log.Printf("Aviso: o download do modelo do Whisper parou em %d%% (nova tentativa em %v): %v", status.Percent(), delay, err)
		status.State, status.Error = ModelWaiting, err.Error()
		f.set(status, "")

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, maxModelRetryDelay)
	}
}

// downloadAlone baixa o modelo com a pasta travada para este processo. Duas
// instâncias do app abertas na primeira execução escreveriam no mesmo arquivo
// ao mesmo tempo, e nenhuma terminaria com o modelo certo: a segunda espera a
// primeira e usa o arquivo dela.
func (f *ModelFile) downloadAlone(ctx context.Context) (string, error) {
	if err := os.MkdirAll(f.dir, 0755); err != nil {
		return "", err
	}
	for {
		// O arquivo pode ter chegado por outro caminho: outra instância do
		// app, ou copiado para a pasta à mão
		if path, ok := f.model.Find(f.dir); ok {
			return path, nil
		}
		unlock, locked := lockFile(filepath.Join(f.dir, f.model.File+".lock"))
		if locked {
			defer unlock()
			break
		}
		f.progress(partSize(f.dir, f.model))
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(modelLockPoll):
		}
	}
	status := f.Status()
	status.State, status.Error = ModelDownloading, ""
	f.set(status, "")
	return f.model.download(ctx, f.dir, f.progress)
}

// partSize é quanto do modelo já foi baixado para dir.
func partSize(dir string, m Model) int64 {
	info, err := os.Stat(filepath.Join(dir, m.File+".part"))
	if err != nil || info.Size() > m.Size {
		return 0
	}
	return info.Size()
}

// progress guarda quanto já foi baixado e avisa, sem passar de um aviso a
// cada modelProgressEvery.
func (f *ModelFile) progress(done int64) {
	f.mu.Lock()
	f.status.Done = done
	status := f.status
	notify := time.Since(f.lastEmit) >= modelProgressEvery
	if notify {
		f.lastEmit = time.Now()
	}
	tenth := status.Percent() / 10
	logIt := tenth > f.logged
	if tenth != f.logged {
		f.logged = tenth
	}
	f.mu.Unlock()

	if logIt {
		log.Printf("-> Baixando o modelo do Whisper: %d%%", tenth*10)
	}
	if notify && f.onChange != nil {
		f.onChange(status)
	}
}

func (f *ModelFile) set(status ModelStatus, path string) {
	f.mu.Lock()
	f.status = status
	if path != "" {
		f.path = path
	}
	f.lastEmit = time.Now()
	f.mu.Unlock()
	if f.onChange != nil {
		f.onChange(status)
	}
}

// Status diz se o modelo está pronto ou em que ponto está o download.
func (f *ModelFile) Status() ModelStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

// Path devolve o caminho do modelo, e false enquanto ele não está no disco.
func (f *ModelFile) Path() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.path, f.path != ""
}

// Stop interrompe o download (o que foi baixado fica para a próxima vez) e
// espera ele parar.
func (f *ModelFile) Stop() {
	f.cancel()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
	}
}
