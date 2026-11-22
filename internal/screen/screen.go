// Package screen captura a tela para guardar com a reunião (ex: um slide
// mostrado na chamada) e faz as miniaturas das capturas.
package screen

import (
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
)

var (
	// ErrCanceled: o usuário desistiu da captura (Esc) sem escolher nada.
	ErrCanceled = errors.New("captura cancelada")
	// ErrNotAllowed: o macOS ainda não deixa o Recorder ver a tela.
	ErrNotAllowed = errors.New("o macOS ainda não deixa o Recorder capturar a tela. Em Ajustes do Sistema > Privacidade e Segurança > Gravação do Áudio do Sistema e da Tela, ative o Recorder e abra o app de novo")
	// ErrUnsupported: a captura de tela só existe no macOS.
	ErrUnsupported = errors.New("a captura de tela só funciona no macOS")
)

// Tamanho máximo das miniaturas, em pixels: o dobro do que a tela mostra, para
// ficarem nítidas em telas Retina.
const (
	thumbWidth  = 480
	thumbHeight = 360
)

// Thumbnail grava em dst uma miniatura JPEG da captura src (PNG). A lista de
// capturas mostra as miniaturas: uma captura de tela Retina inteira ocupa
// mais de 20 MB de memória depois de aberta.
func Thumbnail(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	img, err := png.Decode(in)
	if err != nil {
		return err
	}

	// Sobre branco: o JPEG não tem transparência (os cantos de uma janela)
	bounds := img.Bounds()
	full := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(full, full.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(full, full.Bounds(), img, bounds.Min, draw.Over)

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	w, h := thumbSize(bounds.Dx(), bounds.Dy())
	if err := jpeg.Encode(out, shrink(full, w, h), &jpeg.Options{Quality: 82}); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// thumbSize devolve o tamanho da miniatura de uma imagem w x h: cabe em
// thumbWidth x thumbHeight mantendo a proporção, sem ampliar.
func thumbSize(w, h int) (int, int) {
	if w <= 0 || h <= 0 {
		return 1, 1
	}
	tw, th := w, h
	if tw > thumbWidth {
		tw, th = thumbWidth, h*thumbWidth/w
	}
	if th > thumbHeight {
		tw, th = w*thumbHeight/h, thumbHeight
	}
	return max(tw, 1), max(th, 1)
}

// shrink reduz src para w x h (nunca maiores que a imagem), com cada pixel
// sendo a média da área que ele cobre: o texto de um slide continua legível.
func shrink(src *image.RGBA, w, h int) *image.RGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := y*sh/h, max((y+1)*sh/h, y*sh/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := x*sw/w, max((x+1)*sw/w, x*sw/w+1)
			var r, g, b uint32
			for sy := y0; sy < y1; sy++ {
				row := src.Pix[sy*src.Stride+x0*4 : sy*src.Stride+x1*4]
				for i := 0; i < len(row); i += 4 {
					r += uint32(row[i])
					g += uint32(row[i+1])
					b += uint32(row[i+2])
				}
			}
			n := uint32((x1 - x0) * (y1 - y0))
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(r/n), uint8(g/n), uint8(b/n), 255
		}
	}
	return dst
}
