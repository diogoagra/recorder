package backend

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"recorder/internal/datadir"
	"recorder/internal/mac"
	"recorder/internal/screen"
	"recorder/internal/storage"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// As notas da reunião: o que o usuário escreve durante ela e as capturas de
// tela (ex: um slide mostrado na chamada), guardadas com a sessão. Cada
// captura sabe em que momento da gravação foi feita, para ouvir o que se
// falava ali.

// screenshotsPath é o endereço das capturas no servidor local do app (startup).
const screenshotsPath = "/capturas/"

// screenshotsRoot é a pasta das capturas de tela, com uma pasta por sessão.
func screenshotsRoot() string {
	return datadir.Screenshots()
}

// screenshotsDir devolve a pasta das capturas da sessão, ou "" se sessionID
// não servir como nome de pasta (vazio apagaria as capturas de todas).
func screenshotsDir(sessionID string) string {
	if sessionID == "" || sessionID == "." || sessionID == ".." || strings.ContainsAny(sessionID, `/\`) {
		return ""
	}
	return filepath.Join(screenshotsRoot(), sessionID)
}

// thumbName é o arquivo da miniatura de uma captura, dentro da pasta da sessão.
func thumbName(file string) string {
	return filepath.Join("thumbs", strings.TrimSuffix(file, filepath.Ext(file))+".jpg")
}

// GetNotes devolve as anotações do usuário sobre a reunião.
func (a *App) GetNotes(sessionID string) (string, error) {
	sess, err := a.store.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	return sess.Notes, nil
}

// SetNotes guarda as anotações; a tela chama enquanto o usuário escreve.
func (a *App) SetNotes(sessionID, notes string) error {
	return a.store.UpdateSessionNotes(sessionID, notes)
}

// ScreenCaptureInfo diz à tela se dá para capturar a tela neste sistema.
type ScreenCaptureInfo struct {
	Supported bool   `json:"supported"`
	Shortcut  string `json:"shortcut"` // atalho global, com uma reunião aberta
}

func (a *App) GetScreenCapture() ScreenCaptureInfo {
	return ScreenCaptureInfo{Supported: screen.Supported, Shortcut: mac.CaptureShortcut}
}

// SetCaptureShortcut liga o atalho global da captura de tela enquanto há uma
// reunião aberta na tela, e o devolve aos outros apps quando não há. O atalho
// chega à tela pelo evento "capture-shortcut", e ela chama CaptureScreen para
// a reunião aberta.
func (a *App) SetCaptureShortcut(on bool) {
	mac.EnableCaptureShortcut(on && screen.Supported, func() {
		a.emit("capture-shortcut")
	})
}

// ScreenshotInfo é uma captura de tela da sessão, para a tela mostrar.
type ScreenshotInfo struct {
	ID        int       `json:"id"`
	SessionID string    `json:"session_id"`
	URL       string    `json:"url"`   // caminho no servidor local do app (GetAudioServerURL)
	Thumb     string    `json:"thumb"` // o da miniatura; sem ela, o da própria captura
	CreatedAt time.Time `json:"created_at"`
	Offset    float64   `json:"offset"` // momento no áudio da sessão; negativo se não estava gravando
}

func screenshotInfo(shot storage.Screenshot) ScreenshotInfo {
	base := screenshotsPath + shot.SessionID + "/"
	info := ScreenshotInfo{ID: shot.ID, SessionID: shot.SessionID, URL: base + shot.File, Thumb: base + shot.File, CreatedAt: shot.CreatedAt, Offset: shot.Offset}
	thumb := thumbName(shot.File)
	if _, err := os.Stat(filepath.Join(screenshotsDir(shot.SessionID), thumb)); err == nil {
		info.Thumb = base + filepath.ToSlash(thumb)
	}
	return info
}

// GetScreenshots lista as capturas de tela da sessão, da mais antiga para a mais nova.
func (a *App) GetScreenshots(sessionID string) ([]ScreenshotInfo, error) {
	shots, err := a.store.GetScreenshots(sessionID)
	if err != nil {
		return nil, err
	}
	infos := make([]ScreenshotInfo, len(shots))
	for i, shot := range shots {
		infos[i] = screenshotInfo(shot)
	}
	return infos, nil
}

// CaptureScreen deixa o usuário escolher uma área ou uma janela da tela e
// guarda a imagem com a sessão. Devolve nil se ele desistir (Esc) ou se já
// houver uma captura em andamento.
func (a *App) CaptureScreen(sessionID string) (*ScreenshotInfo, error) {
	if !screen.Supported {
		return nil, screen.ErrUnsupported
	}
	if _, err := a.store.GetSession(sessionID); err != nil {
		return nil, fmt.Errorf("reunião não encontrada")
	}
	if !a.capturing.CompareAndSwap(false, true) {
		return nil, nil
	}
	defer a.capturing.Store(false)
	if !screen.RequestAccess() {
		return nil, screen.ErrNotAllowed
	}

	// O momento da reunião é o do pedido, e não o do fim da seleção
	offset := a.recordingOffset(sessionID)

	// Com o Recorder na frente, a janela dele sai do caminho durante a
	// seleção. Pelo atalho, com outro app na frente (ex: o Meet), nada muda de
	// lugar: mostrar a janela depois tiraria o foco da reunião.
	if ctx := a.wailsCtx(); ctx != nil && mac.IsActive() {
		runtime.WindowHide(ctx)
		defer runtime.WindowShow(ctx)
		time.Sleep(250 * time.Millisecond)
	}

	// Seleção esquecida aberta: desiste sozinha
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	shot, err := a.saveScreenshot(sessionID, offset, func(path string) error {
		return screen.Capture(ctx, path)
	})
	if errors.Is(err, screen.ErrCanceled) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info := screenshotInfo(*shot)
	return &info, nil
}

// saveScreenshot guarda na sessão a imagem que capture gravar em path, com a
// miniatura dela. offset é o momento do áudio da sessão (negativo se não
// estava gravando).
func (a *App) saveScreenshot(sessionID string, offset float64, capture func(path string) error) (*storage.Screenshot, error) {
	dir := screenshotsDir(sessionID)
	if dir == "" {
		return nil, fmt.Errorf("reunião não encontrada")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	file := screenshotFile(dir, time.Now())
	path := filepath.Join(dir, file)
	if err := capture(path); err != nil {
		os.Remove(dir) // só sai se estiver vazia
		return nil, err
	}
	thumb := filepath.Join(dir, thumbName(file))
	if err := screen.Thumbnail(path, thumb); err != nil {
		// Sem a miniatura, a lista mostra a própria captura
		log.Printf("Aviso: não foi possível fazer a miniatura de %s: %v", path, err)
	}
	shot, err := a.store.AddScreenshot(sessionID, file, offset)
	if err != nil {
		os.Remove(path)
		os.Remove(thumb)
		return nil, err
	}
	log.Printf("Captura de tela guardada na sessão %s: %s", sessionID, file)
	return shot, nil
}

// screenshotFile devolve um nome livre em dir para a captura feita em now,
// como "20261111-143205.png".
func screenshotFile(dir string, now time.Time) string {
	stamp := now.Format("20060102-150405")
	file := stamp + ".png"
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			return file
		}
		file = fmt.Sprintf("%s-%d.png", stamp, n)
	}
}

// recordingOffset devolve em que momento do áudio da sessão a gravação está
// agora, em segundos; negativo se a sessão não estiver sendo gravada.
func (a *App) recordingOffset(sessionID string) float64 {
	return a.recorder().Offset(sessionID)
}

// screenshotFiles devolve onde estão a captura e a miniatura dela.
func (a *App) screenshotFiles(id int) (path, thumb string, err error) {
	shot, err := a.store.GetScreenshot(id)
	if err != nil {
		return "", "", fmt.Errorf("captura não encontrada")
	}
	dir := screenshotsDir(shot.SessionID)
	if dir == "" || filepath.Base(shot.File) != shot.File {
		return "", "", fmt.Errorf("captura não encontrada")
	}
	return filepath.Join(dir, shot.File), filepath.Join(dir, thumbName(shot.File)), nil
}

// DeleteScreenshot apaga uma captura de tela da sessão, com o arquivo.
func (a *App) DeleteScreenshot(id int) error {
	path, thumb, err := a.screenshotFiles(id)
	if err != nil {
		return err
	}
	if err := a.store.DeleteScreenshot(id); err != nil {
		return err
	}
	os.Remove(path)
	os.Remove(thumb)
	return nil
}

// RevealScreenshot mostra o arquivo da captura no Finder (para copiar, anexar
// num e-mail, abrir em outro app).
func (a *App) RevealScreenshot(id int) error {
	path, _, err := a.screenshotFiles(id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("o arquivo da captura não existe mais")
	}
	return exec.Command("open", "-R", path).Start()
}
