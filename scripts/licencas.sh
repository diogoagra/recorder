#!/bin/bash

# Copia para uma pasta os avisos de licença do que vai dentro de um pacote do
# Recorder. As licenças do que é distribuído pedem que o aviso acompanhe o
# binário, e não só o código-fonte.
# Uso: scripts/licencas.sh PASTA (chamado pelo build-macos.sh e pelo build-windows.sh)

set -e
cd "$(dirname "$0")/.."

DEST="$1"
if [ -z "${DEST}" ]; then
    echo "Uso: $0 PASTA" >&2
    exit 1
fi
mkdir -p "${DEST}/modulos-go"

# O Recorder e a lista de tudo o que ele usa
cp LICENSE "${DEST}/Recorder.txt"
cp THIRD_PARTY_NOTICES.md "${DEST}/"

# O que vai no pacote ao lado do app: o whisper-server (whisper.cpp), o modelo
# do Whisper (OpenAI) e o ONNX Runtime, que vem com o sherpa-onnx
cp build/licencas/*.txt "${DEST}/"

# O que vai embutido na interface
cp frontend/vendor/fonts/OFL.txt "${DEST}/Inter-OFL.txt"
cp frontend/vendor/marked-LICENSE.md "${DEST}/marked.md"
cp frontend/vendor/purify-LICENSE.txt "${DEST}/DOMPurify.txt"

# Os módulos Go compilados dentro do executável (entre eles o sherpa-onnx e o
# Wails): a licença de cada um, tirada da cópia que a compilação usou
go list -m -f '{{if .Dir}}{{.Path}}	{{.Dir}}{{end}}' all | while IFS=$'\t' read -r path dir; do
    for file in "${dir}"/LICENSE* "${dir}"/LICENCE* "${dir}"/COPYING* "${dir}"/UNLICENSE* "${dir}"/NOTICE*; do
        [ -f "${file}" ] || continue
        cp "${file}" "${DEST}/modulos-go/$(echo "${path}" | tr '/' '_')-$(basename "${file}").txt"
    done
done

# Os arquivos dos módulos vêm só para leitura
chmod -R u+w "${DEST}"
