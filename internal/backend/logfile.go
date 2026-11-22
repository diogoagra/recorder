package backend

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

// O app aberto pelo Finder não tem terminal: sem um arquivo, o que o log diz
// (erros em segundo plano, como os do Intérprete) se perde. O log fica em
// ~/.recorder/recorder.log; acima de maxLogSize, ao abrir o app, o anterior
// vira recorder.log.1.
const maxLogSize = 5 << 20

// logToFile manda o log também para dir/recorder.log.
func logToFile(dir string) {
	path := filepath.Join(dir, "recorder.log")
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogSize {
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.Printf("Aviso: não foi possível abrir o log %s: %v", path, err)
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}
