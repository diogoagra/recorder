#!/bin/bash

# Gera o app do macOS e o .dmg em build/bin. Uso: scripts/build-macos.sh (ou make mac)

# Abort on errors
set -e

# Os caminhos abaixo são a partir da raiz do projeto, de onde quer que o
# script seja chamado
cd "$(dirname "$0")/.."

APP_NAME="Recorder"
APP_DIR="build/bin/${APP_NAME}.app"
DMG="build/bin/${APP_NAME}.dmg"
RESOURCES_DIR="${APP_DIR}/Contents/Resources"

# Código do whisper.cpp na mesma versão do whisper-server do Windows (b5130)
WHISPER_VERSION="1.9.4"
WHISPER_URL="https://github.com/ggml-org/whisper.cpp/archive/refs/tags/v${WHISPER_VERSION}.tar.gz"
WHISPER_SHA256="57e280cee375ab02425b806ad5146b99f6eb9357e3c2b31357c8a6af2e2e44ae"

# O wails instalado pelo "go install" fica na pasta de binários do Go, que nem
# sempre está no PATH
WAILS="$(command -v wails || true)"
[ -n "${WAILS}" ] || WAILS="$(go env GOPATH)/bin/wails"

echo "Verificando dependências..."
if [ ! -f ggml-medium-q5_0.bin ]; then
    echo "Erro: o modelo do Whisper (ggml-medium-q5_0.bin) não está na pasta do projeto."
    echo "Baixe com: make model"
    exit 1
fi
if ! command -v cmake &> /dev/null; then
    echo "Erro: CMake não encontrado!"
    echo "Ele é usado para compilar o whisper-server que vai dentro do app."
    echo "Por favor, rode o comando abaixo no terminal:"
    echo "brew install cmake"
    exit 1
fi

echo "Limpando build anterior..."
# Só o que este script gera: o pacote do Windows, na mesma pasta, fica
rm -rf "${APP_DIR}" "${DMG}"

echo "Compilando aplicativo Wails (isso pode demorar na primeira vez)..."
# O executável procura as bibliotecas do sherpa-onnx em Contents/Frameworks
CGO_LDFLAGS="-Wl,-rpath,@executable_path/../Frameworks" "${WAILS}" build -m

echo "Copiando modelo de IA para o bundle do macOS..."
mkdir -p "${RESOURCES_DIR}"
cp ggml-medium-q5_0.bin "${RESOURCES_DIR}/"

echo "Copiando bibliotecas do sherpa-onnx (identificação de locutores) para o bundle do macOS..."
case "$(uname -m)" in
    arm64) SHERPA_ARCH="aarch64-apple-darwin" ;;
    *) SHERPA_ARCH="x86_64-apple-darwin" ;;
esac
SHERPA_LIB_DIR="$(go list -m -f '{{.Dir}}' github.com/k2-fsa/sherpa-onnx-go-macos)/lib/${SHERPA_ARCH}"
FRAMEWORKS_DIR="${APP_DIR}/Contents/Frameworks"
mkdir -p "${FRAMEWORKS_DIR}"
cp "${SHERPA_LIB_DIR}/libsherpa-onnx-c-api.dylib" "${SHERPA_LIB_DIR}/libonnxruntime.dylib" "${FRAMEWORKS_DIR}/"
chmod u+w "${FRAMEWORKS_DIR}"/*.dylib

echo "Compilando o whisper-server para o bundle do macOS..."
# Estático e com a biblioteca do Metal embutida: um único executável que só
# usa frameworks do sistema, então quem instala o app não precisa do Homebrew.
# O do Homebrew não serve: depende das dylibs do ggml e só roda na versão do
# macOS em que foi compilado. GGML_NATIVE=OFF evita instruções exclusivas do
# processador desta máquina (ex: um M4), que travariam num M1.
WHISPER_TMP="$(mktemp -d)"
trap 'rm -rf "${WHISPER_TMP}"' EXIT
curl -fsSL -o "${WHISPER_TMP}/whisper.tar.gz" "${WHISPER_URL}"
echo "${WHISPER_SHA256}  ${WHISPER_TMP}/whisper.tar.gz" | shasum -a 256 -c -
tar -xzf "${WHISPER_TMP}/whisper.tar.gz" -C "${WHISPER_TMP}"
WHISPER_SRC="${WHISPER_TMP}/whisper.cpp-${WHISPER_VERSION}"
WHISPER_LOG="${WHISPER_TMP}/build.log"
# O log (cheio de avisos do código do whisper.cpp) só aparece se der erro
trap 'cat "${WHISPER_LOG}" 2>/dev/null' ERR
cmake -S "${WHISPER_SRC}" -B "${WHISPER_SRC}/build" \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_OSX_DEPLOYMENT_TARGET=13.3 \
    -DBUILD_SHARED_LIBS=OFF \
    -DGGML_NATIVE=OFF \
    -DGGML_METAL=ON \
    -DGGML_METAL_EMBED_LIBRARY=ON \
    -DWHISPER_BUILD_TESTS=OFF \
    -DWHISPER_BUILD_SERVER=ON \
    -DWHISPER_SDL2=OFF \
    -DWHISPER_CURL=OFF > "${WHISPER_LOG}" 2>&1
cmake --build "${WHISPER_SRC}/build" --target whisper-server --config Release -j >> "${WHISPER_LOG}" 2>&1
trap - ERR
cp "${WHISPER_SRC}/build/bin/whisper-server" "${APP_DIR}/Contents/MacOS/"

echo "Copiando os avisos de licença para o bundle..."
# O app leva junto código e modelos de terceiros: os avisos acompanham o binário
scripts/licencas.sh "${RESOURCES_DIR}/licencas"
# A licença do whisper.cpp guardada no repositório é a da versão compilada aqui
cmp -s "${WHISPER_SRC}/LICENSE" build/licencas/whisper.cpp.txt || {
    echo "Erro: a licença do whisper.cpp ${WHISPER_VERSION} mudou; atualize build/licencas/whisper.cpp.txt"
    exit 1
}

echo "Assinando o bundle (ad-hoc) depois de incluir as bibliotecas..."
codesign --force --deep --sign - "${APP_DIR}"


echo "Criando arquivo DMG..."
hdiutil create -volname "${APP_NAME}" -srcfolder "${APP_DIR}" -ov -format UDZO "${DMG}"

echo "Build completo! O aplicativo está em ${APP_DIR} e no instalador ${DMG}"
