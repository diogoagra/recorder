#!/bin/bash

# Gera o pacote do Windows (compilação cruzada a partir do macOS) em build/bin.
# Uso: scripts/build-windows.sh (ou make windows)

# Abort on errors
set -e

# Os caminhos abaixo são a partir da raiz do projeto, de onde quer que o
# script seja chamado
cd "$(dirname "$0")/.."

APP_NAME="Recorder"
OUTPUT_DIR="build/bin/windows"
ZIP_NAME="${APP_NAME}_Windows.zip"

# whisper-server oficial do whisper.cpp para Windows (CPU), mesma versão do
# whisper-cpp 1.9.4 do Homebrew usado no macOS
WHISPER_URL="https://github.com/ggml-org/whisper.cpp/releases/download/b5130/whisper-bin-x64.zip"
WHISPER_SHA256="f9ec6c52a2e949b62ab51fa21d0d497958f9e41c3010c157c4e42932d5316f3c"

# O wails instalado pelo "go install" fica na pasta de binários do Go, que nem
# sempre está no PATH
WAILS="$(command -v wails || true)"
[ -n "${WAILS}" ] || WAILS="$(go env GOPATH)/bin/wails"

echo "Verificando dependências de cross-compilação..."
if ! command -v x86_64-w64-mingw32-gcc &> /dev/null; then
    echo "Erro: Mingw-w64 não encontrado!"
    echo "Para compilar para Windows a partir do macOS, você precisa instalar o compilador C do Windows."
    echo "Por favor, rode o comando abaixo no terminal:"
    echo "brew install mingw-w64"
    exit 1
fi

echo "Limpando build anterior..."
# Só o que este script gera: o app do macOS, na mesma pasta, fica
rm -rf "${OUTPUT_DIR}" "build/bin/${ZIP_NAME}" "build/bin/${APP_NAME}.exe"
mkdir -p "${OUTPUT_DIR}"

echo "Compilando aplicativo Wails para Windows (isso pode demorar na primeira vez)..."
CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ "${WAILS}" build -platform windows/amd64 -m

echo "Organizando arquivos na pasta de destino..."
mkdir -p "${OUTPUT_DIR}"
# Move o executável gerado pelo Wails para a pasta windows
mv "build/bin/${APP_NAME}.exe" "${OUTPUT_DIR}/"

echo "Copiando bibliotecas do sherpa-onnx (identificação de locutores)..."
SHERPA_LIB_DIR="$(go list -m -f '{{.Dir}}' github.com/k2-fsa/sherpa-onnx-go-windows)/lib/x86_64-pc-windows-gnu"
cp "${SHERPA_LIB_DIR}/sherpa-onnx-c-api.dll" "${SHERPA_LIB_DIR}/onnxruntime.dll" "${OUTPUT_DIR}/"
chmod u+w "${OUTPUT_DIR}"/*.dll

echo "Baixando o whisper-server para Windows..."
WHISPER_TMP="$(mktemp -d)"
trap 'rm -rf "${WHISPER_TMP}"' EXIT
curl -fsSL -o "${WHISPER_TMP}/whisper.zip" "${WHISPER_URL}"
echo "${WHISPER_SHA256}  ${WHISPER_TMP}/whisper.zip" | shasum -a 256 -c -
# Só o servidor e as DLLs que ele usa (ggml-cpu-* são as variantes por CPU,
# escolhidas na hora conforme o processador)
unzip -q -j "${WHISPER_TMP}/whisper.zip" \
    "Release/whisper-server.exe" "Release/whisper.dll" "Release/ggml.dll" \
    "Release/ggml-base.dll" "Release/ggml-cpu-*.dll" \
    -d "${OUTPUT_DIR}/whisper"

# O modelo do Whisper (539 MB) não vai no pacote: o app o baixa para a pasta de
# dados na primeira vez que abre (internal/transcriber/model.go)

echo "Copiando os avisos de licença..."
# O pacote leva junto código e modelos de terceiros: os avisos acompanham o binário
scripts/licencas.sh "${OUTPUT_DIR}/licencas"

echo "Criando arquivo ZIP..."
(cd build/bin && zip -q -r "${ZIP_NAME}" windows/)

echo "Build completo! O aplicativo Windows está em ${OUTPUT_DIR} e compactado no arquivo build/bin/${ZIP_NAME}"
