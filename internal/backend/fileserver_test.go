package backend

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"recorder/internal/audio"
	"recorder/internal/datadir"
)

// O servidor local só entrega arquivos a quem tem o endereço com o segredo,
// em pedidos feitos a 127.0.0.1, e nunca lista uma pasta. Antes, qualquer
// programa do computador listava e baixava o áudio de todas as reuniões.
func TestFileServerNeedsTheSecretAndListsNothing(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")
	os.MkdirAll(datadir.SessionsAudio(), 0755)
	audioFile := filepath.Base(audio.SessionFile(datadir.SessionsAudio(), sess.ID))
	if err := os.WriteFile(filepath.Join(datadir.SessionsAudio(), audioFile), []byte("áudio"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveScreenshot(sess.ID, 5, fakeCapture(200, 100)); err != nil {
		t.Fatal(err)
	}
	shots, _ := a.GetScreenshots(sess.ID)

	a.startup(context.Background())
	base := a.GetAudioServerURL()
	u, err := url.Parse(base)
	if err != nil || len(u.Path) < 33 {
		t.Fatalf("esperava um endereço com o segredo, veio %q", base)
	}
	open := "http://" + u.Host // o mesmo servidor, sem o segredo

	status := func(address, host string) int {
		req, err := http.NewRequest("GET", address, nil)
		if err != nil {
			t.Fatal(err)
		}
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, c := range []struct {
		what, address, host string
		want                int
	}{
		{"o áudio da sessão", base + "/" + audioFile, "", 200},
		{"uma captura", base + shots[0].URL, "", 200},
		{"o áudio, sem o segredo", open + "/" + audioFile, "", 404},
		{"uma captura, sem o segredo", open + shots[0].URL, "", 404},
		{"o áudio, com um segredo errado", open + "/0123456789abcdef0123456789abcdef/" + audioFile, "", 404},
		{"a lista dos áudios", base + "/", "", 404},
		{"a lista dos áudios, sem o segredo", open + "/", "", 404},
		{"a lista das capturas", base + "/capturas/", "", 404},
		{"a lista das capturas da sessão", base + "/capturas/" + sess.ID + "/", "", 404},
		{"a lista das opções de voz", base + "/voz/", "", 404},
		{"o áudio, pedido por outro nome", base + "/" + audioFile, "atacante.example:" + u.Port(), 404},
	} {
		if got := status(c.address, c.host); got != c.want {
			t.Errorf("%s: status %d, esperava %d", c.what, got, c.want)
		}
	}
}

// Apagar uma sessão que não existe não apaga arquivo nenhum: o id entra no
// nome do áudio bruto e dos blocos da pasta temporária, e um "*" casava com os
// blocos de todas as sessões; um "../", com arquivos de fora da pasta.
func TestDeleteUnknownSessionTouchesNoFiles(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")
	tmp := datadir.TmpAudio()
	os.MkdirAll(tmp, 0755)
	block := audio.BlockFile(tmp, sess.ID, 30)
	raw := audio.RawFile(tmp, sess.ID)
	outside := filepath.Join(datadir.Dir(), "fora.pcm")
	for _, f := range []string{block, raw, outside} {
		if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// "x/../../fora" faria do áudio bruto <pasta de dados>/fora.pcm
	if got := audio.RawFile(tmp, "x/../../fora"); got != outside {
		t.Fatalf("o teste esperava que o id alcançasse %s, alcançou %s", outside, got)
	}
	for _, id := range []string{"", "*", sess.ID[:8] + "*", "x/../../fora", "não-existe"} {
		if err := a.DeleteSession(id); err == nil {
			t.Errorf("DeleteSession(%q) deveria recusar uma sessão que não existe", id)
		}
	}
	for _, f := range []string{block, raw, outside} {
		if !exists(f) {
			t.Errorf("%s foi apagado", f)
		}
	}
	if !store.SessionExists(sess.ID) {
		t.Fatal("a sessão de verdade foi apagada")
	}
}
