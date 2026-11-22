# Windows

O Recorder nasceu no macOS, que é a plataforma principal. No Windows ele
grava, transcreve, identifica quem fala, resume e conversa, mas com menos
recursos.

## O que muda

| | macOS | Windows |
|---|---|---|
| Áudio do computador | Core Audio Process Taps (macOS 14.2+) | Loopback do WASAPI |
| Transcrição | GPU (Metal) | Processador: mais lenta |
| `whisper-server` | Compilado junto com o app | O oficial do whisper.cpp, baixado na compilação |
| Áudio da reunião | m4a | wav (arquivos maiores) |
| Importar áudio | Vários formatos, pelo `afconvert` do sistema | Só WAV direto; os outros precisam do [ffmpeg](https://ffmpeg.org/) no PATH |
| Captura de tela e atalho ⌃⌥S | Sim | Não |
| Intérprete | Sim, em Apple Silicon | Não |
| Menu "Sobre" | No menu do app | Só na tela do app |

## Compilar

O caminho testado é a compilação cruzada a partir do macOS:

```sh
brew install mingw-w64   # compilador C para Windows
make windows             # gera build/bin/Recorder_Windows.zip
```

O `scripts/build-windows.sh`:

1. compila o `Recorder.exe` com o Wails e o mingw-w64;
2. copia as bibliotecas do sherpa-onnx (`sherpa-onnx-c-api.dll` e
   `onnxruntime.dll`) para junto do executável;
3. baixa o `whisper-server` oficial do whisper.cpp para Windows (CPU),
   conferido por SHA-256, e o coloca na pasta `whisper`, com as DLLs que ele
   usa;
4. copia os avisos de licença (o modelo do Whisper não vai no pacote: o app
   o baixa na primeira abertura);
5. compacta tudo em `build/bin/Recorder_Windows.zip`.

## Instalar e rodar

Descompacte o zip e abra o `Recorder.exe`: o app sobe o `whisper-server`
sozinho, como no Mac, e o encerra ao fechar.

- Na primeira abertura o app baixa o modelo de transcrição (539 MB) para a
  pasta `.recorder\models` da sua pasta de usuário e mostra o andamento; a
  gravação é liberada quando ele termina
  ([primeira abertura](uso.md#primeira-abertura)).

- O `whisper-server` do Windows precisa do
  [Visual C++ Redistributable](https://aka.ms/vs/17/release/vc_redist.x64.exe)
  (x64), que a maioria das máquinas já tem. Sem ele o Whisper aparece como
  "Indisponível" ao abrir o app.
- A interface usa o WebView2, que já vem no Windows 10 e 11.
- O [Ollama](https://ollama.com) também precisa estar instalado e aberto, a
  não ser que a IA escolhida seja o `agy` ou o `claude`.
- O executável não é assinado: o SmartScreen pode avisar na primeira abertura.

## Limites de hoje

- A transcrição não usa a GPU.
- O áudio das reuniões não é comprimido.
- Não há instalador: os arquivos do NSIS gerados pelo Wails estão em
  `build/windows/installer`, mas o pacote é o zip.
