# Atalhos do projeto.
#
#   make model     baixa o modelo do Whisper (539 MB) para a pasta do projeto, conferido por SHA-256
#   make dev       abre o app em modo desenvolvimento (recarrega a tela ao salvar)
#   make mac       gera build/bin/Recorder.app e o build/bin/Recorder.dmg
#   make windows   gera o build/bin/Recorder_Windows.zip (compilação cruzada a partir do macOS)
#   make check     formatação, go vet e testes (o que precisa passar sempre)
#   make race      testes com o detector de corrida de dados
#   make lint      staticcheck (go install honnef.co/go/tools/cmd/staticcheck@latest)
#   make fmt       formata o código Go

GOFILES := $(shell git ls-files '*.go')

# Modelo do Whisper: o arquivo publicado pelo projeto whisper.cpp no Hugging
# Face, numa revisão fixa do repositório. O app não leva o modelo no pacote:
# ele o baixa sozinho, para a pasta de dados, na primeira vez que abre
# (DefaultModel, em internal/transcriber/model.go, com estes mesmos valores;
# um teste confere). O `make model` é para quem desenvolve: com o arquivo na
# pasta do projeto, o `make dev` e os testes com o Whisper de verdade o usam
# dali, qualquer que seja a pasta de dados.
MODEL        := ggml-medium-q5_0.bin
MODEL_URL    := https://huggingface.co/ggerganov/whisper.cpp/resolve/5359861c739e955e79d9a303bcbc70fb988958b1/$(MODEL)
MODEL_SHA256 := 19fea4b380c3a618ec4723c3eef2eb785ffba0d0538cf43f8f235e7b3b34220f

# O wails instalado pelo "go install" fica na pasta de binários do Go, que nem
# sempre está no PATH
WAILS := $(shell command -v wails || echo "$$(go env GOPATH)/bin/wails")

.PHONY: model dev mac windows check fmt fmt-check vet test race lint

model: $(MODEL)

# Baixa para um arquivo temporário e só renomeia depois de conferir: um
# download interrompido não deixa um modelo pela metade
$(MODEL):
	curl -fL --progress-bar -o $(MODEL).tmp $(MODEL_URL)
	echo "$(MODEL_SHA256)  $(MODEL).tmp" | shasum -a 256 -c -
	mv $(MODEL).tmp $(MODEL)

# O app aberto assim usa as suas reuniões de verdade (~/.recorder). Para
# testar sem tocar nelas: RECORDER_HOME=~/.recorder-teste make dev
dev: $(MODEL)
	$(WAILS) dev

mac: $(MODEL)
	scripts/build-macos.sh

windows: $(MODEL)
	scripts/build-windows.sh

check: fmt-check vet test

fmt:
	gofmt -w $(GOFILES)

fmt-check:
	@unformatted="$$(gofmt -l $(GOFILES))"; \
	if [ -n "$$unformatted" ]; then echo "Arquivos fora do gofmt (rode 'make fmt'):"; echo "$$unformatted"; exit 1; fi

vet:
	go vet ./...

test:
	go test ./...

race:
	go test -race ./...

lint:
	staticcheck ./...
