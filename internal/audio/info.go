package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-audio/wav"
)

// Info devolve quando a gravação do arquivo começou e quanto ela dura, pelos
// metadados do arquivo. No m4a, o afconvert grava a data de criação (o fim da
// gravação, quando o arquivo é gerado); o início é essa data menos a duração.
// No WAV, que não tem data, vale a data de modificação do arquivo.
func Info(path string) (start time.Time, duration time.Duration, err error) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, 0, err
	}
	defer f.Close()

	if strings.EqualFold(path[max(len(path)-4, 0):], ".wav") {
		d := wav.NewDecoder(f)
		if !d.IsValidFile() {
			return time.Time{}, 0, fmt.Errorf("arquivo WAV inválido: %s", path)
		}
		if err := d.FwdToPCM(); err != nil {
			return time.Time{}, 0, err
		}
		bytesPerSecond := int64(d.SampleRate) * int64(d.NumChans) * int64(d.BitDepth/8)
		if bytesPerSecond == 0 {
			return time.Time{}, 0, fmt.Errorf("formato WAV inválido: %s", path)
		}
		duration = time.Duration(int64(d.PCMLen()) * int64(time.Second) / bytesPerSecond)
		stat, err := f.Stat()
		if err != nil {
			return time.Time{}, 0, err
		}
		return stat.ModTime().Add(-duration), duration, nil
	}

	created, duration, err := mp4Times(f)
	if err != nil {
		return time.Time{}, 0, err
	}
	return created.Add(-duration).Local(), duration, nil
}

// mp4Times lê a data de criação e a duração da caixa mvhd de um arquivo MP4/M4A.
func mp4Times(f *os.File) (time.Time, time.Duration, error) {
	stat, err := f.Stat()
	if err != nil {
		return time.Time{}, 0, err
	}
	moovStart, moovEnd, err := findBox(f, 0, stat.Size(), "moov")
	if err != nil {
		return time.Time{}, 0, err
	}
	mvhdStart, mvhdEnd, err := findBox(f, moovStart, moovEnd, "mvhd")
	if err != nil {
		return time.Time{}, 0, err
	}

	buf := make([]byte, min(mvhdEnd-mvhdStart, 32))
	if _, err := f.ReadAt(buf, mvhdStart); err != nil {
		return time.Time{}, 0, err
	}
	var created, timescale, units uint64
	switch {
	case len(buf) >= 20 && buf[0] == 0:
		created = uint64(binary.BigEndian.Uint32(buf[4:8]))
		timescale = uint64(binary.BigEndian.Uint32(buf[12:16]))
		units = uint64(binary.BigEndian.Uint32(buf[16:20]))
	case len(buf) >= 32 && buf[0] == 1:
		created = binary.BigEndian.Uint64(buf[4:12])
		timescale = uint64(binary.BigEndian.Uint32(buf[20:24]))
		units = binary.BigEndian.Uint64(buf[24:32])
	default:
		return time.Time{}, 0, errors.New("caixa mvhd inválida")
	}
	if created == 0 || timescale == 0 {
		return time.Time{}, 0, errors.New("arquivo sem data de criação")
	}
	duration := time.Duration(units * uint64(time.Second) / timescale)
	return mp4Epoch.Add(time.Duration(created) * time.Second), duration, nil
}

// Datas do MP4 contam segundos desde 1904-01-01 UTC
var mp4Epoch = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)

// SetRecordedAt grava end como o fim da gravação do arquivo, de onde Info
// tira quando ela começou. Serve para o áudio convertido muito depois de
// gravado, como o de uma gravação interrompida recuperada.
func SetRecordedAt(path string, end time.Time) error {
	if !strings.EqualFold(filepath.Ext(path), ".wav") {
		if err := setMP4Created(path, end); err != nil {
			return err
		}
	}
	return os.Chtimes(path, end, end)
}

// setMP4Created troca as datas de criação e de modificação da caixa mvhd.
func setMP4Created(path string, t time.Time) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	moovStart, moovEnd, err := findBox(f, 0, stat.Size(), "moov")
	if err != nil {
		return err
	}
	mvhdStart, mvhdEnd, err := findBox(f, moovStart, moovEnd, "mvhd")
	if err != nil {
		return err
	}
	var version [1]byte
	if _, err := f.ReadAt(version[:], mvhdStart); err != nil {
		return err
	}

	secs := uint64(t.Sub(mp4Epoch) / time.Second)
	var buf []byte
	switch {
	case version[0] == 0 && mvhdEnd-mvhdStart >= 12:
		buf = binary.BigEndian.AppendUint32(buf, uint32(secs))
		buf = binary.BigEndian.AppendUint32(buf, uint32(secs))
	case version[0] == 1 && mvhdEnd-mvhdStart >= 20:
		buf = binary.BigEndian.AppendUint64(buf, secs)
		buf = binary.BigEndian.AppendUint64(buf, secs)
	default:
		return errors.New("caixa mvhd inválida")
	}
	if _, err := f.WriteAt(buf, mvhdStart+4); err != nil {
		return err
	}
	return f.Close()
}

// findBox procura a caixa name entre start e end e devolve onde está o
// conteúdo dela (sem o cabeçalho).
func findBox(r io.ReaderAt, start, end int64, name string) (int64, int64, error) {
	var hdr [16]byte
	for pos := start; pos+8 <= end; {
		if _, err := r.ReadAt(hdr[:8], pos); err != nil {
			return 0, 0, err
		}
		size, headerLen := int64(binary.BigEndian.Uint32(hdr[:4])), int64(8)
		switch size {
		case 0: // vai até o fim do arquivo
			size = end - pos
		case 1: // tamanho de 64 bits logo depois do tipo
			if _, err := r.ReadAt(hdr[8:16], pos+8); err != nil {
				return 0, 0, err
			}
			size, headerLen = int64(binary.BigEndian.Uint64(hdr[8:16])), 16
		}
		if size < headerLen || pos+size > end {
			return 0, 0, errors.New("arquivo MP4 inválido")
		}
		if string(hdr[4:8]) == name {
			return pos + headerLen, pos + size, nil
		}
		pos += size
	}
	return 0, 0, fmt.Errorf("caixa %s não encontrada", name)
}
