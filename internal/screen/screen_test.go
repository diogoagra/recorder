package screen

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func readJPEG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestThumbSizeFitsTheBoxWithoutEnlarging(t *testing.T) {
	for _, c := range []struct{ w, h, wantW, wantH int }{
		{3024, 1964, 480, 311}, // tela Retina inteira: limitada pela largura
		{600, 2400, 90, 360},   // área alta e estreita: limitada pela altura
		{200, 100, 200, 100},   // menor que a miniatura: fica como está
		{5000, 3, 480, 1},      // faixa fina: nunca zero
		{0, 0, 1, 1},
	} {
		if w, h := thumbSize(c.w, c.h); w != c.wantW || h != c.wantH {
			t.Errorf("thumbSize(%d, %d) = %d x %d, esperava %d x %d", c.w, c.h, w, h, c.wantW, c.wantH)
		}
	}
}

// A metade esquerda da captura é vermelha e a direita, azul, com um canto
// transparente (como o de uma janela): a miniatura mantém as cores no lugar e
// o canto fica branco.
func TestThumbnailShrinksAndKeepsThePicture(t *testing.T) {
	dir := t.TempDir()
	src := image.NewNRGBA(image.Rect(0, 0, 1920, 1080))
	for y := 0; y < 1080; y++ {
		for x := 0; x < 1920; x++ {
			c := color.NRGBA{R: 220, A: 255}
			if x >= 960 {
				c = color.NRGBA{B: 220, A: 255}
			}
			if x < 40 && y < 40 {
				c = color.NRGBA{}
			}
			src.SetNRGBA(x, y, c)
		}
	}
	writePNG(t, filepath.Join(dir, "captura.png"), src)

	thumb := filepath.Join(dir, "thumbs", "captura.jpg")
	if err := Thumbnail(filepath.Join(dir, "captura.png"), thumb); err != nil {
		t.Fatal(err)
	}
	img := readJPEG(t, thumb)
	if b := img.Bounds(); b.Dx() != 480 || b.Dy() != 270 {
		t.Fatalf("miniatura de %d x %d, esperava 480 x 270", b.Dx(), b.Dy())
	}
	near := func(x, y int, want color.RGBA) {
		t.Helper()
		r, g, b, _ := img.At(x, y).RGBA()
		diff := func(a uint32, b uint8) int {
			d := int(a>>8) - int(b)
			if d < 0 {
				d = -d
			}
			return d
		}
		if diff(r, want.R) > 12 || diff(g, want.G) > 12 || diff(b, want.B) > 12 {
			t.Errorf("pixel (%d, %d) = (%d, %d, %d), esperava perto de %v", x, y, r>>8, g>>8, b>>8, want)
		}
	}
	near(120, 135, color.RGBA{R: 220})
	near(360, 135, color.RGBA{B: 220})
	near(2, 2, color.RGBA{R: 255, G: 255, B: 255})
}

func TestThumbnailRejectsWhatIsNotAnImage(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "captura.png")
	if err := os.WriteFile(src, []byte("não é png"), 0644); err != nil {
		t.Fatal(err)
	}
	thumb := filepath.Join(dir, "thumbs", "captura.jpg")
	if err := Thumbnail(src, thumb); err == nil {
		t.Fatal("esperava erro")
	}
	if _, err := os.Stat(thumb); err == nil {
		t.Fatal("não deveria sobrar uma miniatura pela metade")
	}
}
