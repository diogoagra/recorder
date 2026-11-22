package transcriber

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeModel é um modelo de mentira, pequeno, com o tamanho e o SHA-256 do
// conteúdo devolvido.
func fakeModel(urls ...string) (Model, []byte) {
	content := make([]byte, 300<<10)
	for i := range content {
		content[i] = byte(i*31 + i>>8)
	}
	sum := sha256.Sum256(content)
	return Model{File: "modelo.bin", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), URLs: urls}, content
}

// rangeServer entrega content aceitando pedidos de trecho (Range), e guarda o
// Range de cada pedido que recebeu.
type rangeServer struct {
	*httptest.Server
	mu     sync.Mutex
	ranges []string
}

func newRangeServer(t *testing.T, content []byte) *rangeServer {
	t.Helper()
	s := &rangeServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.ranges = append(s.ranges, r.Header.Get("Range"))
		s.mu.Unlock()
		http.ServeContent(w, r, "modelo.bin", time.Time{}, bytes.NewReader(content))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *rangeServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ranges...)
}

func fastModelDownload(t *testing.T) {
	t.Helper()
	oldDelay, oldMax, oldEvery, oldPoll := modelRetryDelay, maxModelRetryDelay, modelProgressEvery, modelLockPoll
	modelRetryDelay, maxModelRetryDelay, modelProgressEvery, modelLockPoll = 5*time.Millisecond, 20*time.Millisecond, 0, 5*time.Millisecond
	t.Cleanup(func() {
		modelRetryDelay, maxModelRetryDelay, modelProgressEvery, modelLockPoll = oldDelay, oldMax, oldEvery, oldPoll
	})
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestFindWantsTheWholeModel(t *testing.T) {
	m, content := fakeModel()
	pack, data := t.TempDir(), t.TempDir()

	if path, ok := m.find(pack, data); ok {
		t.Fatalf("achou um modelo que não existe: %s", path)
	}
	// Um arquivo pela metade (uma cópia interrompida) não serve
	os.WriteFile(filepath.Join(pack, m.File), content[:1000], 0644)
	if path, ok := m.find(pack, data); ok {
		t.Fatalf("aceitou um arquivo pela metade: %s", path)
	}
	os.WriteFile(filepath.Join(data, m.File), content, 0644)
	if path, ok := m.find(pack, data); !ok || path != filepath.Join(data, m.File) {
		t.Fatalf("esperava o modelo da pasta de dados, veio %q (%v)", path, ok)
	}
	// O que vem no pacote do app tem preferência sobre o baixado
	os.WriteFile(filepath.Join(pack, m.File), content, 0644)
	if path, _ := m.find(pack, data); path != filepath.Join(pack, m.File) {
		t.Fatalf("esperava o modelo do pacote, veio %q", path)
	}
}

func TestDownloadChecksTheModelBeforeNamingIt(t *testing.T) {
	m, content := fakeModel()
	srv := newRangeServer(t, content)
	m.URLs = []string{srv.URL}
	dir := filepath.Join(t.TempDir(), "models") // a pasta ainda não existe

	var last int64
	path, err := m.download(context.Background(), dir, func(done int64) { last = done })
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, m.File) || !bytes.Equal(mustRead(t, path), content) {
		t.Fatalf("o modelo baixado em %s não é o do servidor", path)
	}
	if last != m.Size {
		t.Fatalf("o andamento parou em %d de %d", last, m.Size)
	}
	if exists(path + ".part") {
		t.Fatal("sobrou o arquivo parcial")
	}
}

func TestDownloadContinuesFromThePartialFile(t *testing.T) {
	m, content := fakeModel()
	srv := newRangeServer(t, content)
	m.URLs = []string{srv.URL}
	dir := t.TempDir()
	// O que uma execução anterior do app já tinha baixado
	os.WriteFile(filepath.Join(dir, m.File+".part"), content[:100000], 0644)

	var first int64 = -1
	path, err := m.download(context.Background(), dir, func(done int64) {
		if first < 0 {
			first = done
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := srv.requests(); len(got) != 1 || got[0] != "bytes=100000-" {
		t.Fatalf("esperava um pedido só, do byte 100000 em diante; vieram %q", got)
	}
	if first != 100000 {
		t.Fatalf("o andamento deveria começar no que já estava baixado, começou em %d", first)
	}
	if !bytes.Equal(mustRead(t, path), content) {
		t.Fatal("o modelo completado não é o do servidor")
	}
}

func TestDownloadStartsOverWhenTheServerSendsTheWholeFile(t *testing.T) {
	m, content := fakeModel()
	// Um servidor que não aceita continuar: responde sempre com o arquivo inteiro
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.Write(content)
	}))
	defer srv.Close()
	m.URLs = []string{srv.URL}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, m.File+".part"), bytes.Repeat([]byte{'x'}, 5000), 0644)

	path, err := m.download(context.Background(), dir, func(int64) {})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustRead(t, path), content) {
		t.Fatal("o começo antigo ficou misturado com o arquivo novo")
	}
}

func TestDownloadRefusesAFileThatIsNotTheModel(t *testing.T) {
	m, content := fakeModel()
	other := bytes.Repeat([]byte{'x'}, len(content)) // mesmo tamanho, outro conteúdo
	wrong := newRangeServer(t, other)
	right := newRangeServer(t, content)
	dir := t.TempDir()

	m.URLs = []string{wrong.URL}
	if _, err := m.download(context.Background(), dir, func(int64) {}); err == nil || !strings.Contains(err.Error(), "não é o modelo esperado") {
		t.Fatalf("esperava a recusa pelo SHA-256, veio %v", err)
	}
	if exists(filepath.Join(dir, m.File)) || exists(filepath.Join(dir, m.File+".part")) {
		t.Fatal("o arquivo recusado ficou no disco")
	}

	// Com um segundo endereço que entrega o arquivo certo, o download termina,
	// sem aproveitar nada do primeiro
	m.URLs = []string{wrong.URL, right.URL}
	path, err := m.download(context.Background(), dir, func(int64) {})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustRead(t, path), content) {
		t.Fatal("o modelo baixado não é o do segundo endereço")
	}
	if got := right.requests(); len(got) != 1 || got[0] != "" {
		t.Fatalf("o segundo endereço deveria ser baixado do início; pedidos %q", got)
	}
}

func TestDownloadRefusesAnotherSizeWithoutSavingIt(t *testing.T) {
	m, content := fakeModel()
	srv := newRangeServer(t, content[:len(content)-1])
	m.URLs = []string{srv.URL}
	dir := t.TempDir()

	if _, err := m.download(context.Background(), dir, func(int64) {}); err == nil || !strings.Contains(err.Error(), "o modelo tem") {
		t.Fatalf("esperava a recusa pelo tamanho, veio %v", err)
	}
	if exists(filepath.Join(dir, m.File+".part")) {
		t.Fatal("um arquivo de outro tamanho começou a ser salvo")
	}
}

func TestDownloadUsesTheSecondAddressWhenTheFirstIsGone(t *testing.T) {
	m, content := fakeModel()
	gone := httptest.NewServer(http.NotFoundHandler())
	defer gone.Close()
	mirror := newRangeServer(t, content)
	m.URLs = []string{gone.URL, mirror.URL}

	path, err := m.download(context.Background(), t.TempDir(), func(int64) {})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustRead(t, path), content) {
		t.Fatal("o modelo baixado não é o da cópia")
	}
}

func TestDownloadGivesUpOnAConnectionThatStopsAnswering(t *testing.T) {
	old := modelStallTimeout
	modelStallTimeout = 100 * time.Millisecond
	defer func() { modelStallTimeout = old }()

	m, content := fakeModel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.Write(content[:50000])
		w.(http.Flusher).Flush()
		<-release // a conexão fica aberta, sem mandar mais nada
	}))
	defer srv.Close()
	defer close(release)
	m.URLs = []string{srv.URL}
	dir := t.TempDir()

	started := time.Now()
	_, err := m.download(context.Background(), dir, func(int64) {})
	if !errors.Is(err, errModelStalled) {
		t.Fatalf("esperava o erro de conexão parada, veio %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("o download ficou esperando uma conexão parada")
	}
	// O que chegou fica para a próxima tentativa continuar
	if info, err := os.Stat(filepath.Join(dir, m.File+".part")); err != nil || info.Size() != 50000 {
		t.Fatalf("esperava 50000 bytes guardados para continuar, veio %v (%v)", info, err)
	}
}

func TestFetchModelUsesTheFileAlreadyOnDisk(t *testing.T) {
	m, content := fakeModel()
	srv := newRangeServer(t, content)
	m.URLs = []string{srv.URL}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, m.File), content, 0644)

	f := FetchModel(m, dir, func(ModelStatus) { t.Error("um modelo que já está no disco não tem andamento a avisar") })
	defer f.Stop()
	if path, ok := f.Path(); !ok || path != filepath.Join(dir, m.File) {
		t.Fatalf("esperava o modelo do disco, veio %q (%v)", path, ok)
	}
	if st := f.Status(); st.State != ModelReady || st.Percent() != 100 {
		t.Fatalf("estado %+v", st)
	}
	select {
	case <-f.ready:
	default:
		t.Fatal("o modelo do disco deveria estar pronto na hora")
	}
	if n := len(srv.requests()); n != 0 {
		t.Fatalf("o modelo foi pedido ao servidor %d vez(es)", n)
	}
}

// A primeira execução sem internet: o app tenta de novo sozinho, e a tela
// sabe por que o download parou.
func TestFetchModelKeepsTryingUntilItWorks(t *testing.T) {
	fastModelDownload(t)
	m, content := fakeModel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			http.Error(w, "fora do ar", http.StatusServiceUnavailable)
			return
		}
		http.ServeContent(w, r, "modelo.bin", time.Time{}, bytes.NewReader(content))
	}))
	defer srv.Close()
	m.URLs = []string{srv.URL}
	dir := t.TempDir()

	var mu sync.Mutex
	var seen []ModelStatus
	f := FetchModel(m, dir, func(st ModelStatus) {
		mu.Lock()
		seen = append(seen, st)
		mu.Unlock()
	})
	defer f.Stop()

	if st := f.Status(); st.State == ModelReady {
		t.Fatalf("sem o arquivo no disco, o modelo não pode estar pronto: %+v", st)
	}
	if _, ok := f.Path(); ok {
		t.Fatal("o caminho do modelo foi dado antes de ele existir")
	}
	waitFor(t, "o modelo ficar pronto depois de duas falhas", func() bool { return f.Status().State == ModelReady })

	path, ok := f.Path()
	if !ok || !bytes.Equal(mustRead(t, path), content) {
		t.Fatalf("o modelo em %q não é o do servidor", path)
	}
	mu.Lock()
	defer mu.Unlock()
	var waited bool
	for _, st := range seen {
		if st.State == ModelWaiting {
			waited = true
			if !strings.Contains(st.Error, "503") {
				t.Fatalf("a espera deveria dizer o que o servidor respondeu, veio %q", st.Error)
			}
		}
	}
	if !waited {
		t.Fatalf("a tela não soube que o download parou: %+v", seen)
	}
	if last := seen[len(seen)-1]; last.State != ModelReady || last.Done != m.Size || last.Error != "" {
		t.Fatalf("o último aviso deveria ser o do modelo pronto, veio %+v", last)
	}
}

// A conexão cai no meio: a tentativa seguinte pede só o que falta.
func TestFetchModelContinuesAfterTheConnectionDrops(t *testing.T) {
	fastModelDownload(t)
	m, content := fakeModel()
	var mu sync.Mutex
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		first := len(ranges) == 1
		mu.Unlock()
		if first {
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			w.Write(content[:120000])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler) // derruba a conexão sem terminar a resposta
		}
		http.ServeContent(w, r, "modelo.bin", time.Time{}, bytes.NewReader(content))
	}))
	defer srv.Close()
	m.URLs = []string{srv.URL}

	f := FetchModel(m, t.TempDir(), nil)
	defer f.Stop()
	waitFor(t, "o modelo ficar pronto depois de a conexão cair", func() bool { return f.Status().State == ModelReady })

	path, _ := f.Path()
	if !bytes.Equal(mustRead(t, path), content) {
		t.Fatal("o modelo emendado não é o do servidor")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ranges) != 2 || ranges[0] != "" || ranges[1] != "bytes=120000-" {
		t.Fatalf("esperava o segundo pedido continuar do byte 120000; pedidos %q", ranges)
	}
}

// Duas instâncias do app abertas na primeira execução: só uma baixa, e a
// outra usa o arquivo dela.
func TestFetchModelWaitsForAnotherInstanceDownloading(t *testing.T) {
	fastModelDownload(t)
	m, content := fakeModel()
	srv := newRangeServer(t, content)
	m.URLs = []string{srv.URL}
	dir := t.TempDir()

	unlock, ok := lockFile(filepath.Join(dir, m.File+".lock"))
	if !ok {
		t.Fatal("não foi possível travar a pasta como a outra instância faria")
	}
	defer unlock()

	f := FetchModel(m, dir, nil)
	defer f.Stop()
	time.Sleep(50 * time.Millisecond)
	if n := len(srv.requests()); n != 0 {
		t.Fatalf("com outra instância baixando, esta também pediu o modelo (%d pedidos)", n)
	}
	if st := f.Status(); st.State != ModelDownloading {
		t.Fatalf("esperando a outra instância, o estado deveria ser o de download: %+v", st)
	}

	// A outra instância termina: o arquivo aparece com o nome final
	os.WriteFile(filepath.Join(dir, m.File+".tmp"), content, 0644)
	os.Rename(filepath.Join(dir, m.File+".tmp"), filepath.Join(dir, m.File))
	waitFor(t, "o modelo baixado pela outra instância ser usado", func() bool { return f.Status().State == ModelReady })
	if n := len(srv.requests()); n != 0 {
		t.Fatalf("o modelo foi baixado de novo (%d pedidos)", n)
	}
}

func TestStopInterruptsTheDownloadAndKeepsWhatCame(t *testing.T) {
	fastModelDownload(t)
	m, content := fakeModel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.Write(content[:70000])
		w.(http.Flusher).Flush()
		<-release
	}))
	defer srv.Close()
	defer close(release)
	m.URLs = []string{srv.URL}
	dir := t.TempDir()

	f := FetchModel(m, dir, nil)
	waitFor(t, "o começo do modelo chegar", func() bool { return f.Status().Done == 70000 })
	f.Stop()
	select {
	case <-f.done:
	default:
		t.Fatal("o Stop deveria esperar o download parar")
	}
	if st := f.Status(); st.State == ModelReady {
		t.Fatalf("um download interrompido não deixa o modelo pronto: %+v", st)
	}

	// Na próxima abertura do app, a tela já começa do que estava baixado
	again := FetchModel(m, dir, nil)
	defer again.Stop()
	if st := again.Status(); st.Done != 70000 && st.State != ModelReady {
		t.Fatalf("esperava recomeçar dos 70000 bytes já baixados, veio %+v", st)
	}
}

func TestParseContentRange(t *testing.T) {
	if start, total, ok := parseContentRange("bytes 100-538/539"); !ok || start != 100 || total != 539 {
		t.Fatalf("veio %d, %d, %v", start, total, ok)
	}
	for _, bad := range []string{"", "bytes */539", "bytes 100-538/*", "items 0-1/2", "bytes 100/539"} {
		if _, _, ok := parseContentRange(bad); ok {
			t.Errorf("%q deveria ser recusado", bad)
		}
	}
}

// O `make model` baixa para a pasta do projeto o mesmo arquivo que o app
// baixa sozinho: o nome, o endereço e o SHA-256 ficam nos dois lugares.
func TestDefaultModelMatchesTheMakefile(t *testing.T) {
	makefile := string(mustRead(t, filepath.Join("..", "..", "Makefile")))
	variable := func(name string) string {
		t.Helper()
		match := regexp.MustCompile(`(?m)^` + name + `\s*:=\s*(\S+)\s*$`).FindStringSubmatch(makefile)
		if match == nil {
			t.Fatalf("%s não está no Makefile", name)
		}
		return strings.ReplaceAll(match[1], "$(MODEL)", DefaultModel.File)
	}
	if got := variable("MODEL"); got != DefaultModel.File {
		t.Errorf("MODEL no Makefile é %q, e o app usa %q", got, DefaultModel.File)
	}
	if got := variable("MODEL_SHA256"); got != DefaultModel.SHA256 {
		t.Errorf("MODEL_SHA256 no Makefile é %q, e o app confere com %q", got, DefaultModel.SHA256)
	}
	if got := variable("MODEL_URL"); got != DefaultModel.URLs[0] {
		t.Errorf("MODEL_URL no Makefile é %q, e o app baixa de %q", got, DefaultModel.URLs[0])
	}
	for _, address := range DefaultModel.URLs {
		if !strings.HasPrefix(address, "https://") || !strings.HasSuffix(address, "/"+DefaultModel.File) {
			t.Errorf("endereço inesperado para o modelo: %s", address)
		}
	}
}

// Os endereços de verdade, pela internet: cada um precisa entregar um arquivo
// do tamanho do modelo e aceitar continuar um download. Só roda a pedido:
//
//	RECORDER_MODEL_TEST=1 go test ./internal/transcriber -run TestModelAddressesAnswer -v
//
// O segundo endereço só existe depois de o modelo ser anexado ao Release.
func TestModelAddressesAnswer(t *testing.T) {
	if os.Getenv("RECORDER_MODEL_TEST") == "" {
		t.Skip("usa a internet; rode com RECORDER_MODEL_TEST=1")
	}
	m := DefaultModel
	for _, address := range m.URLs {
		t.Run(hostOf(address), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
			req.Header.Set("Accept-Encoding", "identity")
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", m.Size-1024))
			resp, err := modelClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			start, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
			if resp.StatusCode != http.StatusPartialContent || !ok || start != m.Size-1024 || total != m.Size {
				t.Fatalf("resposta %s, Content-Range %q; esperava o fim de um arquivo de %d bytes", resp.Status, resp.Header.Get("Content-Range"), m.Size)
			}
		})
	}
}

// O download inteiro, de verdade (539 MB), conferido pelo SHA-256, como na
// primeira execução do app. Só roda a pedido, e guarda o arquivo na pasta
// indicada (um download interrompido continua na próxima vez):
//
//	RECORDER_MODEL_DIR=/tmp/modelo go test ./internal/transcriber -run TestDownloadsTheRealModel -v -timeout 30m
func TestDownloadsTheRealModel(t *testing.T) {
	dir := os.Getenv("RECORDER_MODEL_DIR")
	if dir == "" {
		t.Skip("baixa 539 MB; rode com RECORDER_MODEL_DIR=<pasta>")
	}
	m := DefaultModel
	started := time.Now()
	var first int64 = -1
	path, err := m.download(context.Background(), dir, func(done int64) {
		if first < 0 {
			first = done
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != m.Size {
		t.Fatalf("o modelo em %s tem %v bytes (%v)", path, info, err)
	}
	t.Logf("modelo em %s: %d bytes baixados em %s (o resto já estava no disco)", path, m.Size-first, time.Since(started).Round(time.Second))
}
