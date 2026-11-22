# Desenvolvimento

- [Rodar sem tocar nos seus dados](#rodar-sem-tocar-nos-seus-dados)
- [Estrutura do projeto](#estrutura-do-projeto)
- [Testes](#testes)
- [Portas e processos](#portas-e-processos)
- [Banco de dados e migrações](#banco-de-dados-e-migrações)
- [Bindings do Wails](#bindings-do-wails)
- [Interface](#interface)
- [Empacotamento](#empacotamento)
- [Commits, ramos e versões](#commits-ramos-e-versões)

## Rodar sem tocar nos seus dados

```sh
brew install whisper-cpp                    # o whisper-server usado fora do pacote
make model                                  # o modelo do Whisper, na pasta do projeto (opcional)
RECORDER_HOME=~/.recorder-teste make dev  # wails dev numa pasta de dados à parte
```

A variável `RECORDER_HOME` troca a pasta de dados, que por padrão é
`~/.recorder`. Sem ela, o app aberto pelo `make dev` usa as suas reuniões de
verdade — e aplica nelas qualquer migração nova do banco. Use uma pasta sua,
e não uma de nome previsível em `/tmp`: o app executa o Python do ambiente de
voz que encontrar lá dentro.

Fora do pacote, o app procura o `whisper-server` do Homebrew
(`/opt/homebrew/bin` ou `/usr/local/bin`). O modelo do Whisper ele procura
primeiro na pasta em que foi aberto, que é onde o `make model` o deixa; se
não estiver lá, baixa para `models/` dentro da pasta de dados em uso, uma vez
para cada pasta. Com o `make model`, uma pasta de dados nova não baixa 539 MB
de novo.

## Estrutura do projeto

```
recorder/
├── main.go        a entrada: embute a interface e escolhe o modo (janela ou terminal)
├── frontend/      a interface: HTML, CSS e JavaScript puros
├── internal/      o código do app
│   ├── backend/   o aplicativo: liga as peças e atende a interface
│   └── …          uma pasta por assunto (áudio, transcrição, vozes, IA…)
├── build/         ícone e arquivos de pacote do macOS e do Windows
├── scripts/       geram os pacotes para distribuir
└── docs/          estes guias
```

Na raiz ficam só a entrada e os arquivos do projeto (`go.mod`, `wails.json`,
`Makefile`, licença). O que os builds geram sai em `build/bin/`, fora do git.

**`internal/backend/`**, o aplicativo:

| Arquivo | O que é |
|---|---|
| `run.go` | Os três modos de execução (a janela, pelo Wails, e os dois de terminal) e a montagem das peças |
| `app.go` | Métodos chamados pela interface: sessões, gravação, resumo, chat, configurações |
| `recorder.go` | A gravação: o pedido da tela (gravar/parar), o laço que grava em blocos e a fila que os transcreve, com novas tentativas se o Whisper cair |
| `workspaces.go` | Workspaces: criar, renomear, apagar, mover reuniões entre eles |
| `notes.go` | Notas da reunião e capturas de tela guardadas com ela |
| `export.go` | Exportação de cada aba em Markdown |
| `llm.go` | Escolha da IA do assistente (Ollama, agy ou Claude) |
| `interpreter.go` | Liga o Intérprete (microfone virtual, Whisper próprio, voz e atalho) e grava a amostra da voz |
| `status.go` | O aviso de trabalho em segundo plano que a tela mostra |
| `fileserver.go` | Servidor local que entrega o áudio e as capturas à janela |
| `csp.go` | Política de conteúdo da janela |
| `logfile.go` | O log em arquivo |
| `version.go` | A versão do app |
| `bindings_on.go`, `bindings_off.go` | Dizem ao app quando ele está sendo executado só para gerar os bindings |

**Os outros pacotes de `internal/`**, que não dependem da janela:

| Pacote | O que é |
|---|---|
| `datadir` | Onde ficam os dados do app (`~/.recorder`, ou a pasta de `RECORDER_HOME`) |
| `storage` | Persistência em SQLite e as migrações do esquema |
| `audio` | Captura do microfone e do áudio do computador, cancelamento de eco, cortes nas pausas da fala e o arquivo de áudio de cada sessão |
| `transcriber` | Cliente HTTP do `whisper-server` e o processo dele: subir, subir de novo se cair e encerrar servidores órfãos; e o modelo do Whisper, baixado na primeira abertura (`model.go`) |
| `diarization` | Identificação de quem fala com o sherpa-onnx, e o download dos modelos |
| `roster` | Lista de participantes do workspace e comparação de nomes escritos de jeitos diferentes |
| `speakers` | Vozes de cada sessão, nomes dados a elas e vozes conhecidas de cada workspace |
| `transcript` | Transcrição de um bloco de áudio: Whisper + quem fala, filtros e vocabulário do Whisper |
| `chat` | As IAs: cliente do Ollama (respostas em tempo real, formato fixo, busca e download de modelos) e dos CLIs na nuvem (`cli.go`, `agy.go`, `claude.go`); `claude_session.go` mantém um `claude` aberto para a correção do Intérprete |
| `assistant` | Resumo, tarefas, nomes tirados da conversa e chat com busca |
| `interpreter` | O Intérprete: roteador do microfone (`router.go`, `mixer.go`), frases pelas pausas (`segment.go`), Whisper, correção da tradução (`corrector.go`) e o servidor de voz em Python (`voice_server.py`, embutido no app) |
| `mac` | Badge do Dock e os atalhos globais (⌃⌥Espaço do Intérprete e ⌃⌥S da captura de tela) |
| `screen` | Captura de tela (o `screencapture` do macOS) e miniaturas das capturas |

## Testes

```sh
make check   # gofmt, go vet e os testes
make race    # os testes com o detector de corrida de dados
make lint    # staticcheck
```

Os testes não usam microfone, Whisper, Ollama nem a sua pasta de dados: a
gravação recebe um microfone de mentira, o Whisper e as IAs são servidores e
programas falsos, e cada teste abre o banco numa pasta temporária.

Alguns só rodam a pedido, porque mexem em dispositivos de verdade ou usam a
internet:

| Variável | O que libera |
|---|---|
| `RECORDER_DEVICE_TESTS=1` | Testes do roteador do Intérprete, que abrem o microfone e precisam do BlackHole |
| `RECORDER_SCREEN_TEST=1` | `go test ./internal/screen -run TestCapturesTheScreen`: fotografa a tela (pede a permissão de gravação de tela para o terminal) |
| `RECORDER_MODEL_TEST=1` | `go test ./internal/transcriber -run TestModelAddressesAnswer -v`: os dois endereços do modelo do Whisper respondem com um arquivo do tamanho certo e aceitam continuar um download |
| `RECORDER_MODEL_DIR=<pasta>` | `go test ./internal/transcriber -run TestDownloadsTheRealModel -v -timeout 30m`: baixa o modelo de verdade (539 MB) para a pasta, conferido, como na primeira abertura |

## Portas e processos

Tudo escuta só em `127.0.0.1`.

| Porta | Quem | Quando |
|---|---|---|
| 47823 | `whisper-server` das reuniões | Sempre que o app está aberto |
| 47824 | Servidor de voz do Intérprete (Python) | Com o Intérprete ligado |
| 47825 | `whisper-server` do Intérprete | Com o Intérprete ligado |
| aleatória | Servidor de arquivos do app (`internal/backend/fileserver.go`): áudio das reuniões e capturas de tela, para a interface; o endereço leva um segredo sorteado a cada abertura | Sempre que o app está aberto |
| 11434 | Ollama (não é do Recorder) | Se instalado |

As portas ficam longe das comuns de desenvolvimento de propósito: na 8080, um
servidor de outro programa recebia as transcrições e respondia 404. O endereço
é `127.0.0.1`, e não `localhost`: o `whisper-server` só escuta em IPv4, e
`localhost` vai primeiro para o IPv6, onde outro programa pode estar na mesma
porta.

## Banco de dados e migrações

O banco é um SQLite em `~/.recorder/transcripts.db`. O esquema tem versão
(`PRAGMA user_version`), e as mudanças são passos numerados em
`internal/storage/migrate.go`.

- Ao abrir um banco de versão anterior, o app copia o banco para
  `~/.recorder/backups/` (ficam as 3 últimas cópias) e só então aplica os
  passos que faltam.
- Mudar o esquema é acrescentar um passo no fim de `migrations`. O passo
  precisa poder rodar de novo sem estragar nada.
- Um banco criado por uma versão mais nova do app abre com um aviso no log, e
  a versão dele não volta atrás.

Depois de mexer no esquema, teste a migração numa cópia e rode o app com
`RECORDER_HOME`.

## Bindings do Wails

A interface chama os métodos públicos do `App` por `window.go.backend.App`,
que o Wails põe na janela. Para saber quais métodos existem, o `wails dev`, o
`wails build` e o `wails generate module` **compilam e executam o app** com a
etiqueta `bindings`. Nessa execução o app não abre o banco, não sobe o
`whisper-server` e não mexe no microfone (`generatingBindings`, em
`internal/backend/bindings_on.go`): só lista os métodos.

O Wails também escreve esses métodos em arquivos JavaScript e TypeScript, em
`build/wailsjs/`. A interface não os usa, e a pasta fica fora do git.

## Interface

`frontend/` não tem dependências a instalar nem etapa de build: `index.html`,
`style.css` e `app.js`. O que a tela usa de terceiros — o
[marked](https://marked.js.org), o
[DOMPurify](https://github.com/cure53/DOMPurify) e a fonte Inter — fica em
`frontend/vendor/`, em cópias exatas das publicadas, com a origem e o SHA-256
de cada arquivo em [`frontend/vendor/README.md`](../frontend/vendor/README.md).
Nada é carregado de CDN: o app funciona sem internet e as versões não mudam
sozinhas.

O visual é o de um aplicativo de mesa clássico: claro ou escuro, denso, com
painéis de borda fina. As cores dos dois temas são variáveis no começo do
`style.css` (o `theme.js` escolhe o tema antes de a tela ser desenhada). Os
ícones são desenhos do próprio projeto, guardados uma vez só num `<svg>` de
símbolos no começo do `index.html` e usados com `<use href="#i-nome">`. O
player do áudio da reunião tem controles do app, e não os do navegador, que
mudam de cara do WKWebView para o WebView2; quem toca é o `<audio>` de sempre.

O Markdown que vem da IA (resumo e chat) passa por um lugar só,
`renderMarkdown`, que o converte e limpa o HTML antes de ir para a tela.

A janela é servida com uma política de conteúdo (`internal/backend/csp.go`)
que só deixa rodar os arquivos `.js` do app: **nada de `<script>` com código
dentro, de `onclick` no HTML nem de `eval`**. O que ela proíbe para de
funcionar sem erro na compilação; o teste `TestFrontendFitsThePageCSP` avisa.
Imagens e áudio só podem vir do próprio app e do servidor local de arquivos.
O porquê está em [seguranca.md](seguranca.md).

## Empacotamento

O `wails build` sozinho gera um app que só abre na máquina em que foi
compilado. Os scripts fazem o resto:

- **`scripts/build-macos.sh`** (`make mac`): compila o app; copia as
  bibliotecas do sherpa-onnx para
  `Contents/Frameworks`; baixa o código do whisper.cpp (conferido por SHA-256)
  e compila o `whisper-server` estático, com Metal embutido e sem instruções
  exclusivas do processador da máquina; copia os avisos de licença; assina o
  pacote localmente (ad hoc) e gera o `build/bin/Recorder.dmg`.
- **`scripts/build-windows.sh`** (`make windows`): veja [windows.md](windows.md).

Cada script apaga só o que ele mesmo gera, então os pacotes das duas
plataformas convivem em `build/bin/`. Os dois chamam o `scripts/licencas.sh`,
que põe dentro do pacote os avisos de licença de tudo o que vai nele (veja o
[THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md)).

### O modelo do Whisper fica fora do pacote

O modelo (`ggml-medium-q5_0.bin`, 539 MB) era quase todo o tamanho do pacote.
Desde a 1.2.0 ele não vai mais junto: na primeira abertura o app o baixa para
`models/`, na pasta de dados, e as versões seguintes usam o mesmo arquivo.

- **O quê e de onde:** `DefaultModel`, em `internal/transcriber/model.go`, tem
  o nome, o tamanho, o SHA-256 e os endereços, em ordem: o arquivo do projeto
  whisper.cpp no Hugging Face, numa revisão fixa do repositório, e uma cópia
  anexada ao Release 1.2.0 do Recorder, usada se o primeiro falhar. O
  `Makefile` (`make model`) repete nome, endereço e SHA-256;
  `TestDefaultModelMatchesTheMakefile` confere.
- **Como:** o download vai para um arquivo `.part`, continua de onde parou se
  a conexão cair ou o app for fechado, e só ganha o nome final depois de
  conferido pelo SHA-256. Sem conexão, o app tenta de novo sozinho. Duas
  instâncias abertas não baixam ao mesmo tempo: a segunda espera a primeira.
- **Enquanto baixa:** o `whisper-server` não sobe (o supervisor espera o
  modelo), a tela abre e mostra o andamento (`CheckServices` não acusa a falta
  do Whisper, e o evento `whisper-model` leva o progresso), e o que depende da
  transcrição é recusado com um aviso: gravar, importar, reprocessar e o
  Intérprete (`internal/backend/model.go`).
- **Onde o app procura antes de baixar:** a pasta em que foi aberto, o pacote
  (`Contents/Resources` no macOS, ao lado do `Recorder.exe` no Windows) e a
  pasta de dados. Um arquivo só serve com o tamanho exato. Para montar um
  pacote que funcione sem internet, basta copiar o modelo para dentro dele
  antes de assinar.
- **Ao trocar de modelo:** mudar `DefaultModel` e o `Makefile` juntos e anexar
  o arquivo novo ao Release cujo endereço for para o código. A cópia do
  Release 1.2.0 precisa continuar lá enquanto houver versões apontando para
  ela.

Dois testes conferem os endereços e o download de verdade, pela internet, e
só rodam a pedido: veja [Testes](#testes). Vale rodar o primeiro depois de
publicar um Release com o modelo anexado.

## Commits, ramos e versões

As mensagens seguem "área: descrição", em português, com um corpo que explica
o porquê. Cada commit deve compilar e passar no `make check` sozinho.

- **Ramos:** `main` é a última versão publicada; o trabalho do dia a dia
  entra em `develop`. A correção de algo já publicado sai de `main`, num ramo
  `hotfix/<versão>`, e volta para os dois.
- **Versão:** fica em `internal/backend/version.go` (o que o "Sobre" mostra)
  e no `productVersion` do `wails.json` (o que vai no pacote); o teste
  `TestVersionMatchesThePackage` confere que os dois são iguais.
- **Tags:** o número da versão, sem prefixo (`1.0.0`).
