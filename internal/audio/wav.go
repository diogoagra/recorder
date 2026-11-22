package audio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// Os WAV do app são sempre PCM de 16 bits, mono. Eles são escritos direto, em
// pedaços: o codificador do go-audio pede as amostras como []int (8 bytes
// cada), e salvar 1h de áudio por ele alocava 780 MB.

const wavHeaderSize = 44

// wavHeader monta o cabeçalho de um WAV PCM de 16 bits, mono, com dataSize
// bytes de áudio.
func wavHeader(dataSize int64, rate int) ([]byte, error) {
	if dataSize < 0 || dataSize > 0xFFFFFFFF-36 {
		return nil, fmt.Errorf("áudio grande demais para um WAV: %d bytes", dataSize)
	}
	h := make([]byte, 0, wavHeaderSize)
	h = append(h, "RIFF"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(36+dataSize))
	h = append(h, "WAVEfmt "...)
	h = binary.LittleEndian.AppendUint32(h, 16)             // tamanho do bloco fmt
	h = binary.LittleEndian.AppendUint16(h, 1)              // PCM
	h = binary.LittleEndian.AppendUint16(h, 1)              // mono
	h = binary.LittleEndian.AppendUint32(h, uint32(rate))   // amostras por segundo
	h = binary.LittleEndian.AppendUint32(h, uint32(rate*2)) // bytes por segundo
	h = binary.LittleEndian.AppendUint16(h, 2)              // bytes por amostra
	h = binary.LittleEndian.AppendUint16(h, 16)             // bits por amostra
	h = append(h, "data"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(dataSize))
	return h, nil
}

// SaveWav grava data em path como WAV PCM de 16 bits, mono.
func SaveWav(path string, data []int16, sampleRate int) error {
	header, err := wavHeader(int64(len(data))*2, sampleRate)
	if err != nil {
		return err
	}
	return writeFile(path, func(w io.Writer) error {
		if _, err := w.Write(header); err != nil {
			return err
		}
		return writePCM(w, data)
	})
}

// writePCM escreve as amostras como bytes (little-endian), em pedaços.
func writePCM(w io.Writer, samples []int16) error {
	buf := make([]byte, 0, 32*1024)
	for len(samples) > 0 {
		n := min(len(samples), cap(buf)/2)
		buf = buf[:n*2]
		for i, s := range samples[:n] {
			buf[i*2] = byte(s)
			buf[i*2+1] = byte(s >> 8)
		}
		if _, err := w.Write(buf); err != nil {
			return err
		}
		samples = samples[n:]
	}
	return nil
}

// writeFile cria path e chama write com um escritor com buffer. Se algo
// falhar, o arquivo pela metade é apagado.
func writeFile(path string, write func(w io.Writer) error) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(out, 256*1024)
	err = write(w)
	if err == nil {
		err = w.Flush()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

// rawToWav grava em wavPath o áudio bruto de rawPath (AppendRaw), sem
// carregá-lo na memória: o arquivo bruto já é o conteúdo do WAV, só falta o
// cabeçalho.
func rawToWav(rawPath, wavPath string) error {
	in, err := os.Open(rawPath)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	size := info.Size() &^ 1 // uma amostra pela metade no fim (escrita interrompida) fica de fora
	header, err := wavHeader(size, sampleRate)
	if err != nil {
		return err
	}
	return writeFile(wavPath, func(w io.Writer) error {
		if _, err := w.Write(header); err != nil {
			return err
		}
		_, err := io.CopyN(w, in, size)
		return err
	})
}

// wavData abre um WAV gravado por SaveWav e devolve o arquivo posicionado no
// começo do áudio, com quantos bytes de áudio ele tem.
func wavData(path string) (*os.File, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	size, err := seekWavData(f)
	if err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}
	return f, size, nil
}

// seekWavData confere que f é um WAV PCM de 16 bits, mono, a 16kHz e o deixa
// no começo do áudio. Devolve o tamanho do áudio em bytes.
func seekWavData(f *os.File) (int64, error) {
	var riff [12]byte
	if _, err := io.ReadFull(f, riff[:]); err != nil || string(riff[:4]) != "RIFF" || string(riff[8:]) != "WAVE" {
		return 0, errors.New("arquivo WAV inválido")
	}
	okFormat := false
	for {
		var chunk [8]byte
		if _, err := io.ReadFull(f, chunk[:]); err != nil {
			return 0, errors.New("arquivo WAV sem áudio")
		}
		size := int64(binary.LittleEndian.Uint32(chunk[4:]))
		switch string(chunk[:4]) {
		case "fmt ":
			if size < 16 {
				return 0, errors.New("arquivo WAV inválido")
			}
			format := make([]byte, size+size%2)
			if _, err := io.ReadFull(f, format); err != nil {
				return 0, errors.New("arquivo WAV inválido")
			}
			okFormat = binary.LittleEndian.Uint16(format[0:]) == 1 && // PCM
				binary.LittleEndian.Uint16(format[2:]) == 1 && // mono
				binary.LittleEndian.Uint32(format[4:]) == sampleRate &&
				binary.LittleEndian.Uint16(format[14:]) == 16
		case "data":
			if !okFormat {
				return 0, errors.New("formato de WAV não suportado (esperava 16 bits, mono, 16kHz)")
			}
			// O que há de fato no arquivo vale mais que o tamanho anotado, que
			// fica errado se a escrita foi interrompida
			pos, err := f.Seek(0, io.SeekCurrent)
			if err != nil {
				return 0, err
			}
			info, err := f.Stat()
			if err != nil {
				return 0, err
			}
			return min(size, info.Size()-pos) &^ 1, nil
		default:
			if _, err := f.Seek(size+size%2, io.SeekCurrent); err != nil {
				return 0, err
			}
		}
	}
}

// concatWav grava em out o áudio dos WAV de parts, um depois do outro, sem
// carregá-los na memória.
func concatWav(parts []string, out string) error {
	var files []*os.File
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	var sizes []int64
	var total int64
	for _, part := range parts {
		f, size, err := wavData(part)
		if err != nil {
			return err
		}
		files, sizes, total = append(files, f), append(sizes, size), total+size
	}
	header, err := wavHeader(total, sampleRate)
	if err != nil {
		return err
	}
	return writeFile(out, func(w io.Writer) error {
		if _, err := w.Write(header); err != nil {
			return err
		}
		for i, f := range files {
			if _, err := io.CopyN(w, f, sizes[i]); err != nil {
				return err
			}
		}
		return nil
	})
}
