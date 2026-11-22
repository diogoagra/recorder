//go:build darwin

package audio

/*
#cgo LDFLAGS: -framework AVFoundation -framework CoreMedia -framework Foundation
#include <stdlib.h>
#include "concat_darwin.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

// concatCompressed junta os arquivos de áudio comprimido de parts (m4a/mp4
// com AAC), em ordem, num MPEG-4 em out, copiando os pacotes de áudio como
// estão. É o que deixa retomar uma sessão sem decodificar e comprimir de novo
// o que já estava gravado, o que tirava um pouco da qualidade a cada retomada.
// out precisa terminar em ".mp4".
func concatCompressed(parts []string, out string) error {
	paths := make([]*C.char, len(parts))
	for i, p := range parts {
		paths[i] = C.CString(p)
		defer C.free(unsafe.Pointer(paths[i]))
	}
	cOut := C.CString(out)
	defer C.free(unsafe.Pointer(cOut))

	var errBuf [512]C.char
	if C.concat_audio(&paths[0], C.int(len(paths)), cOut, &errBuf[0], C.int(len(errBuf))) != 0 {
		return errors.New(C.GoString(&errBuf[0]))
	}
	return nil
}
