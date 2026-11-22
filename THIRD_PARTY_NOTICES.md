# Código, modelos e bibliotecas de terceiros

O Recorder é distribuído sob a [licença MIT](LICENSE). Ele inclui ou usa o
trabalho abaixo, cada um sob a própria licença.

## Incluído neste repositório

| Componente | Onde | Licença |
|---|---|---|
| [marked](https://marked.js.org) 15.0.12 — Markdown para HTML (Christopher Jeffrey e colaboradores) | `frontend/vendor/marked.min.js` | MIT — [`marked-LICENSE.md`](frontend/vendor/marked-LICENSE.md) |
| [DOMPurify](https://github.com/cure53/DOMPurify) 3.4.17 — filtro de HTML (Cure53 e colaboradores) | `frontend/vendor/purify.min.js` | Apache 2.0 ou MPL 2.0 — [`purify-LICENSE.txt`](frontend/vendor/purify-LICENSE.txt) |
| [Inter](https://rsms.me/inter/) — fonte da interface, subconjuntos latin e latin-ext (The Inter Project Authors) | `frontend/vendor/fonts/` | SIL Open Font License 1.1 — [`OFL.txt`](frontend/vendor/fonts/OFL.txt) |
| Modelos do instalador do Windows, do [Wails](https://wails.io) | `build/windows/installer/` | MIT |
| Textos das licenças do whisper.cpp, do modelo do Whisper e do ONNX Runtime, que vão nos pacotes | `build/licencas/` | — |

Os três primeiros são cópias exatas das publicadas, sem alterações; a origem e
o SHA-256 de cada arquivo estão em
[`frontend/vendor/README.md`](frontend/vendor/README.md). Eles vão embutidos no
executável.

## Dependências (baixadas na compilação)

| Componente | Uso | Licença |
|---|---|---|
| [Wails](https://wails.io) v2 | Janela, menus e ponte entre o Go e a interface | MIT |
| [malgo](https://github.com/gen2brain/malgo) / [miniaudio](https://miniaud.io) | Captura e reprodução de áudio | Unlicense / domínio público ou MIT-0 |
| [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) (pelo `sherpa-onnx-go`) | Identificação de quem fala | Apache 2.0 |
| [ONNX Runtime](https://onnxruntime.ai) (vem com o sherpa-onnx) | Execução dos modelos de voz | MIT |
| [go-sqlite3](https://github.com/mattn/go-sqlite3) / [SQLite](https://sqlite.org) | Banco de dados | MIT / domínio público |
| [go-audio](https://github.com/go-audio) (`audio`, `wav`) | Leitura de arquivos WAV | Apache 2.0 |
| [google/uuid](https://github.com/google/uuid) | Identificadores | BSD de 3 cláusulas |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | Chamadas do sistema | BSD de 3 cláusulas |

As demais dependências indiretas estão em [`go.mod`](go.mod).

## Dentro dos pacotes gerados pelos scripts

O `scripts/build-macos.sh` e o `scripts/build-windows.sh` põem no pacote, além do app:

| Componente | Origem | Licença |
|---|---|---|
| `whisper-server`, do [whisper.cpp](https://github.com/ggml-org/whisper.cpp) 1.9.4 | macOS: compilado do código-fonte; Windows: o binário oficial | MIT |
| Bibliotecas do sherpa-onnx e do ONNX Runtime | Dependências do Go | Apache 2.0 / MIT |

Essas licenças pedem que o aviso de direitos autorais acompanhe as
distribuições em binário. O `scripts/licencas.sh`, chamado pelos dois scripts
de build, põe dentro do pacote (`Contents/Resources/licencas` no macOS,
`licencas` no Windows) este arquivo, a licença do Recorder e as do
whisper.cpp, do modelo do Whisper e do ONNX Runtime (guardadas em
`build/licencas/`), as do marked, do DOMPurify e da Inter (em
`frontend/vendor/`) e a de cada módulo Go compilado no executável, tirada da
cópia que a compilação usou.

## Baixado pelo app, no computador de quem usa

| Componente | Quando | Licença |
|---|---|---|
| Modelo do Whisper `ggml-medium-q5_0.bin` (pesos do [Whisper](https://github.com/openai/whisper), da OpenAI, convertidos pelo projeto whisper.cpp), do [Hugging Face](https://huggingface.co/ggerganov/whisper.cpp) ou da cópia anexada ao Release 1.2.0 do Recorder | Na primeira abertura | MIT |
| [pyannote segmentation 3.0](https://huggingface.co/pyannote/segmentation-3.0), convertido pelo sherpa-onnx | Na primeira abertura | MIT |
| [3D-Speaker](https://github.com/modelscope/3D-Speaker) ERes2Net (`speech_eres2net_base_sv_zh-cn_3dspeaker_16k`) | Na primeira abertura | Apache 2.0 |
| [EmbeddingGemma](https://huggingface.co/google/embeddinggemma-300m), pelo Ollama | Na primeira pergunta do chat | Termos de uso do Gemma (Google) |
| [mlx-audio](https://github.com/Blaizzy/mlx-audio) 0.6.4 e as dependências dele | Ao instalar a voz do Intérprete | MIT (o mlx-audio) |
| [Qwen3-TTS](https://huggingface.co/mlx-community/Qwen3-TTS-12Hz-1.7B-Base-bf16) 12Hz 1.7B Base (Alibaba, conversão para MLX da comunidade) | Na primeira vez que o Intérprete é ligado | Apache 2.0 |

## Programas que o app usa, se estiverem instalados

O Recorder não distribui nenhum deles: [Ollama](https://ollama.com), o `agy`
(Antigravity), o `claude` (Claude Code), o
[BlackHole](https://github.com/ExistentialAudio/BlackHole), o
[uv](https://docs.astral.sh/uv/), o [ffmpeg](https://ffmpeg.org) e os
utilitários do macOS (`afconvert`, `screencapture`).

## Marcas

Os nomes de produtos citados pertencem aos respectivos donos. Este projeto é
independente: não é afiliado, patrocinado nem endossado por nenhum deles.
