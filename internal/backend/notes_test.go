package backend

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"recorder/internal/screen"
	"recorder/internal/storage"
	"strings"
	"testing"
	"time"
)

// fakeCapture grava um PNG de w x h em path, como a captura de tela faria.
func fakeCapture(w, h int) func(path string) error {
	return func(path string) error {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for i := range img.Pix {
			img.Pix[i] = 200
		}
		img.Set(0, 0, color.RGBA{R: 255, A: 255})
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		defer f.Close()
		return png.Encode(f, img)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestNotesAreKeptWithTheSession(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")

	if notes, err := a.GetNotes(sess.ID); err != nil || notes != "" {
		t.Fatalf("sessão nova: notas %q, erro %v", notes, err)
	}
	text := "Perguntar do prazo\n\n- migração: \"ano a ano\"\n"
	if err := a.SetNotes(sess.ID, text); err != nil {
		t.Fatal(err)
	}
	if notes, err := a.GetNotes(sess.ID); err != nil || notes != text {
		t.Fatalf("notas %q, erro %v", notes, err)
	}
	// A lista de reuniões leva as notas, para a busca
	sessions, err := a.GetSessions()
	if err != nil || len(sessions) == 0 {
		t.Fatalf("GetSessions: %v", err)
	}
	found := false
	for _, s := range sessions {
		if s.ID == sess.ID {
			found = s.Notes == text
		}
	}
	if !found {
		t.Fatal("GetSessions deveria trazer as notas da sessão")
	}
	if _, err := a.GetNotes("não existe"); err == nil {
		t.Fatal("sessão que não existe deveria dar erro")
	}
}

func TestSaveScreenshotKeepsTheFileTheThumbAndTheMoment(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")

	shot, err := a.saveScreenshot(sess.ID, 754.2, fakeCapture(1600, 1000))
	if err != nil {
		t.Fatal(err)
	}
	dir := screenshotsDir(sess.ID)
	if !exists(filepath.Join(dir, shot.File)) || !exists(filepath.Join(dir, thumbName(shot.File))) {
		t.Fatalf("faltou a captura ou a miniatura em %s", dir)
	}
	// Duas capturas no mesmo segundo não podem ter o mesmo nome
	second, err := a.saveScreenshot(sess.ID, -1, fakeCapture(300, 200))
	if err != nil {
		t.Fatal(err)
	}
	if second.File == shot.File {
		t.Fatalf("as duas capturas ficaram em %s", shot.File)
	}

	shots, err := a.GetScreenshots(sess.ID)
	if err != nil || len(shots) != 2 {
		t.Fatalf("GetScreenshots: %d capturas, erro %v", len(shots), err)
	}
	base := "/capturas/" + sess.ID + "/"
	if shots[0].URL != base+shot.File || shots[0].Thumb != base+"thumbs/"+strings.TrimSuffix(shot.File, ".png")+".jpg" {
		t.Fatalf("endereços: %q e %q", shots[0].URL, shots[0].Thumb)
	}
	if shots[0].Offset != 754.2 || shots[1].Offset != -1 {
		t.Fatalf("momentos: %v e %v", shots[0].Offset, shots[1].Offset)
	}
	// Sem a miniatura, a lista mostra a própria captura
	os.Remove(filepath.Join(dir, thumbName(second.File)))
	shots, _ = a.GetScreenshots(sess.ID)
	if shots[1].Thumb != shots[1].URL {
		t.Fatalf("sem miniatura deveria usar a captura, veio %q", shots[1].Thumb)
	}

	// Apagar uma captura leva os arquivos dela, e só os dela
	if err := a.DeleteScreenshot(shot.ID); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, shot.File)) || exists(filepath.Join(dir, thumbName(shot.File))) {
		t.Fatal("os arquivos da captura apagada continuam lá")
	}
	if !exists(filepath.Join(dir, second.File)) {
		t.Fatal("apagou a captura errada")
	}
	if err := a.DeleteScreenshot(shot.ID); err == nil {
		t.Fatal("apagar de novo deveria dar erro")
	}

	// Apagar a sessão leva as capturas que sobraram
	if err := a.DeleteSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	if exists(dir) {
		t.Fatal("a pasta das capturas da sessão apagada continua lá")
	}
	if shots, _ := store.GetScreenshots(sess.ID); len(shots) != 0 {
		t.Fatalf("sobraram %d capturas no banco", len(shots))
	}
}

// O que CaptureScreen recusa antes de mexer na tela.
func TestCaptureScreenRefusesWithoutTouchingTheScreen(t *testing.T) {
	if !screen.Supported {
		t.Skip("a captura de tela só existe no macOS")
	}
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")

	if shot, err := a.CaptureScreen("não existe"); err == nil || shot != nil {
		t.Fatalf("reunião que não existe: captura %v, erro %v", shot, err)
	}
	// Com uma seleção já aberta, o segundo pedido não abre outra
	a.capturing.Store(true)
	if shot, err := a.CaptureScreen(sess.ID); err != nil || shot != nil {
		t.Fatalf("captura em andamento: captura %v, erro %v", shot, err)
	}
	if !a.capturing.Load() {
		t.Fatal("o pedido recusado não pode liberar a captura em andamento")
	}
	if shots, _ := a.GetScreenshots(sess.ID); len(shots) != 0 {
		t.Fatalf("nada deveria ter sido guardado, vieram %d", len(shots))
	}
}

func TestCanceledCaptureLeavesNothing(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")

	_, err := a.saveScreenshot(sess.ID, 10, func(path string) error { return screen.ErrCanceled })
	if err != screen.ErrCanceled {
		t.Fatalf("esperava ErrCanceled, veio %v", err)
	}
	if shots, _ := a.GetScreenshots(sess.ID); len(shots) != 0 {
		t.Fatalf("desistir não deveria guardar nada, guardou %d", len(shots))
	}
	if exists(screenshotsDir(sess.ID)) {
		t.Fatal("sobrou a pasta vazia da sessão")
	}
}

// Apagar uma sessão sem id (ou com um id que saia da pasta) não pode levar
// as capturas das outras.
func TestDeleteSessionKeepsOtherScreenshots(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")
	shot, err := a.saveScreenshot(sess.ID, -1, fakeCapture(200, 100))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", ".", "..", "../" + sess.ID, sess.ID + "/thumbs"} {
		if dir := screenshotsDir(id); dir != "" {
			t.Errorf("screenshotsDir(%q) = %q, esperava vazio", id, dir)
		}
		a.DeleteSession(id)
	}
	if !exists(filepath.Join(screenshotsDir(sess.ID), shot.File)) {
		t.Fatal("a captura de outra sessão foi apagada")
	}
}

func TestRecordingOffsetFollowsTheSessionAudio(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")

	if got := a.recordingOffset(sess.ID); got >= 0 {
		t.Fatalf("sem gravar, esperava negativo, veio %v", got)
	}
	// Gravando há 30 s numa sessão que já tinha 10 min de áudio
	rec := a.recorder()
	rec.Start(sess.ID, 600)
	rec.mu.Lock()
	rec.started = time.Now().Add(-30 * time.Second)
	rec.mu.Unlock()
	if got := a.recordingOffset(sess.ID); got < 630 || got > 632 {
		t.Fatalf("esperava ~630 s, veio %v", got)
	}
	if got := a.recordingOffset("outra"); got >= 0 {
		t.Fatalf("gravando outra sessão, esperava negativo, veio %v", got)
	}
}

// As capturas chegam à tela pelo servidor local do app, em /capturas/.
func TestScreenshotsAreServedToTheScreen(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")
	if _, err := a.saveScreenshot(sess.ID, 5, fakeCapture(800, 500)); err != nil {
		t.Fatal(err)
	}
	a.startup(context.Background())
	base := a.GetAudioServerURL()
	if base == "" {
		t.Fatal("o servidor local não subiu")
	}
	shots, _ := a.GetScreenshots(sess.ID)
	for _, c := range []struct{ path, contentType string }{
		{shots[0].URL, "image/png"},
		{shots[0].Thumb, "image/jpeg"},
	} {
		resp, err := http.Get(base + c.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != c.contentType || len(body) == 0 {
			t.Errorf("%s: status %d, tipo %q, %d bytes", c.path, resp.StatusCode, resp.Header.Get("Content-Type"), len(body))
		}
	}
}

func TestNotesMarkdownListsScreenshotsWithTheirMoment(t *testing.T) {
	taken := time.Date(2026, 11, 11, 14, 32, 0, 0, time.Local)
	got := notesMarkdown("Reunião", "\nPerguntar do prazo\n- migração\n\n", []storage.Screenshot{
		{File: "20261111-143205.png", Offset: 754},
		{File: "20261111-150000.png", Offset: -1, CreatedAt: taken},
	}, filepath.Join("pasta", "s1"))
	want := "# Reunião\n\n## Notas\n" +
		"\nPerguntar do prazo\n- migração\n" +
		"\n## Capturas de tela\n\n" +
		"- [12:34] " + filepath.Join("pasta", "s1", "20261111-143205.png") + "\n" +
		"- [14:32] " + filepath.Join("pasta", "s1", "20261111-150000.png") + "\n"
	if got != want {
		t.Fatalf("veio:\n%s\nesperava:\n%s", got, want)
	}
}

func TestExportNotesNeedsNotesOrScreenshots(t *testing.T) {
	a, store := testApp(t)
	sess, _ := store.CreateSession("", "Reunião")
	if _, _, err := a.exportText(sess, "notes"); err == nil {
		t.Fatal("sem notas nem capturas deveria dar erro")
	}

	// Só com uma captura já tem o que exportar
	shot, err := a.saveScreenshot(sess.ID, 61, fakeCapture(200, 100))
	if err != nil {
		t.Fatal(err)
	}
	title, text, err := a.exportText(sess, "notes")
	if err != nil || title != "Notas" || !strings.Contains(text, "- [01:01] "+filepath.Join(screenshotsDir(sess.ID), shot.File)) {
		t.Fatalf("título %q, texto %q, erro %v", title, text, err)
	}

	a.SetNotes(sess.ID, "Perguntar do prazo")
	sess, _ = store.GetSession(sess.ID)
	if _, text, err = a.exportText(sess, "notes"); err != nil || !strings.Contains(text, "## Notas\n\nPerguntar do prazo\n") {
		t.Fatalf("texto %q, erro %v", text, err)
	}
}
