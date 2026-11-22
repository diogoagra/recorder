# Segurança e privacidade

O Recorder guarda o que há de mais sensível num trabalho: o áudio e o texto
das reuniões. Este guia diz o que fica no computador, de quem o app se
defende, como, e o que fica de fora.

- [O que fica no computador e o que sai](#o-que-fica-no-computador-e-o-que-sai)
- [De quem o app se defende](#de-quem-o-app-se-defende)
- [A reunião não dá ordens à IA](#a-reunião-não-dá-ordens-à-ia)
- [O que a IA escreve é tratado como texto](#o-que-a-ia-escreve-é-tratado-como-texto)
- [Serviços locais](#serviços-locais)
- [Arquivos e pasta de dados](#arquivos-e-pasta-de-dados)
- [Downloads](#downloads)
- [O que vai para o log](#o-que-vai-para-o-log)
- [Limites conhecidos](#limites-conhecidos)
- [Como foi conferido](#como-foi-conferido)

Para relatar uma falha, veja o [SECURITY.md](../SECURITY.md).

## O que fica no computador e o que sai

O app não tem servidor próprio, conta nem telemetria.

| O quê | Para onde vai |
|---|---|
| Áudio das reuniões | Fica em `~/.recorder`. Nunca é enviado. |
| Transcrição (Whisper) e identificação de quem fala | Feitas no computador. |
| Resumo, tarefas, nomes e chat com o **Ollama** | Feitos no computador. |
| Resumo, tarefas, nomes e chat com o **agy** ou o **Claude** | O texto das reuniões é enviado ao serviço do modelo escolhido, pelo programa e pelo login que você já usa. Com o `agy`, cada chamada fica no histórico de conversas dele. |
| Intérprete, com a correção pelo Claude Haiku | O texto de cada frase (em português e a tradução) é enviado à Anthropic. Com a correção pelo Ollama ou sem correção, nada sai. |
| Voz do Intérprete | A amostra da sua voz e a voz gerada ficam no computador. |

Os únicos acessos à internet feitos pelo próprio app são downloads: o modelo
de transcrição do Whisper (Hugging Face ou, se ele falhar, a cópia no GitHub
do Recorder) e os modelos de identificação de locutores (GitHub do
sherpa-onnx), na primeira abertura; o modelo de busca do chat (pelo Ollama);
e, para quem instala o Intérprete, o ambiente Python e o modelo de voz (PyPI e
Hugging Face). Nesses pedidos vai só o endereço do arquivo: nada das reuniões.

## De quem o app se defende

1. **Do que é dito numa reunião**, ou vem num áudio importado. Qualquer
   participante pode dizer qualquer coisa, e isso vira transcrição, entra nos
   pedidos à IA e aparece na tela. Esse texto não pode dar ordens à IA,
   executar código na janela do app nem fazer o conteúdo da reunião sair do
   computador.
2. **De páginas abertas no navegador e de outros programas**, que alcançam as
   portas locais do app.
3. **De arquivos de áudio malformados** na importação.
4. **De outras contas do mesmo computador.**
5. **Da rede**, nos downloads.

Quem já controla a sua conta no computador está fora do alcance de qualquer
defesa do app: lê a pasta de dados e usa o microfone como você.

## A reunião não dá ordens à IA

O `agy` e o `claude` são agentes: sabem rodar comandos, ler arquivos e usar a
internet. O texto que recebem do Recorder é o de uma reunião.

- **claude:** roda sempre sem nenhuma ferramenta (`--tools ""`), com
  `--safe-mode` (não carrega o seu `CLAUDE.md`, hooks, plugins, skills nem
  servidores MCP), `--strict-mcp-config`, sem comandos de barra, sem guardar a
  conversa, numa pasta vazia criada para a chamada, e com as instruções do
  Recorder no lugar das de agente de programação. Isso vale também para o
  processo que fica aberto para a correção do Intérprete.
- **Menções a arquivos.** Mesmo assim, o `claude` anexa ao pedido o conteúdo de
  todo arquivo citado como `@caminho` no texto que recebe. Medido em
  21/11/2026 (claude 2.1.325), com arquivos de teste: um `@/pasta/arquivo` no
  começo de uma linha ou depois de um espaço levou o conteúdo do arquivo
  junto; no meio de uma palavra (um e-mail), não. Um `@/Users/.../arquivo`
  numa fala levaria um arquivo do computador para a nuvem e para o resumo.
  Por isso todo `@` que não está no meio de uma palavra vai cercado por
  espaços de largura zero: quem lê não vê diferença, e o `claude` deixa de
  enxergar ali um arquivo (conferido com os mesmos arquivos de teste).
- **agy:** não tem opção para desligar as ferramentas. Roda numa pasta vazia,
  com `--sandbox`, sem comandos de barra e sem aprovar nada; no modo não
  interativo ele nega as ferramentas que pedem permissão, e a resposta que
  ficou vazia por causa de uma ferramenta negada vira erro. No teste com
  arquivos, o `agy` não anexou os citados com `@`. **Isso depende da
  configuração do seu `agy`**: veja os [limites](#limites-conhecidos).
- **Ollama:** recebe texto e devolve texto; não há ferramentas.
- O texto das reuniões vai para os programas pela entrada padrão, nunca na
  linha de comando.
- Os nomes tirados da conversa só valem com pistas conferidas no código: a
  fala citada precisa existir e ter o nome escrito, e um nome dado sozinho
  pede três falas de outras pessoas.

## O que a IA escreve é tratado como texto

A IA pode devolver HTML de verdade, por engano ou induzida pelo que foi dito.
Um script na janela alcançaria o app inteiro.

- Falas, nomes, tarefas, títulos, notas e avisos entram na tela como texto
  (`textContent`), nunca como HTML.
- O Markdown do resumo e do chat passa por um lugar só (`renderMarkdown`), que
  o converte com o [marked](https://marked.js.org) e limpa com o
  [DOMPurify](https://github.com/cure53/DOMPurify) usando uma **lista do que
  pode ficar**: títulos, listas, tabelas, links, ênfase e código. Imagens,
  áudio, vídeo, formulários, estilos, `id` e `class` ficam de fora. Sem as
  bibliotecas, o texto aparece como veio.
- Links do resumo abrem no navegador do sistema, e só os de `http(s)` e
  `mailto`; a janela do app nunca navega. Soltar um arquivo ou um link na
  janela também não a faz navegar.
- A janela tem uma **política de conteúdo** (CSP, em
  `internal/backend/csp.go`), que é a
  segunda barreira: só rodam os arquivos `.js` do próprio app (nada de script
  escrito no HTML, `onclick` ou `eval`), imagens e áudio só vêm do app e do
  servidor local dele, e não há conexões para fora. Mesmo que um trecho de
  HTML escapasse do filtro, ele não executaria nem buscaria nada na internet.
- Nada é carregado de CDN: as bibliotecas e a fonte ficam em
  `frontend/vendor/`, em cópias exatas das publicadas, com o SHA-256 de cada
  uma.

Antes desta revisão o filtro era uma lista do que **sai**, e passava o que
ninguém lembrou de proibir: um `<table background="…">` no resumo fazia a
janela buscar um endereço de fora ao ser mostrado, sem clique — e o endereço
podia levar o texto da reunião, mesmo com o Ollama. Um `<label for="…">`
fazia um clique no resumo acionar um botão do app.

## Serviços locais

Tudo o que o app abre escuta só em `127.0.0.1`. Uma porta local, porém, é
alcançada por qualquer programa do computador, e uma página aberta no
navegador também consegue mandar pedidos a ela.

| Serviço | Proteção |
|---|---|
| Servidor de arquivos (áudio, capturas de tela e opções da calibração, para a janela) | Porta sorteada a cada abertura; o endereço leva um segredo de 128 bits que só a janela recebe; só atende pedidos feitos a `127.0.0.1` (um site que aponte o próprio nome para `127.0.0.1` chega com outro `Host`); entrega arquivos, nunca a lista de uma pasta. |
| Servidor de voz do Intérprete (porta 47824) | Só fala com quem manda, num cabeçalho, o segredo sorteado pelo app ao subir o servidor (que vai pelo ambiente, não pela linha de comando); o pedido precisa ser JSON e feito a `127.0.0.1`, o que uma página do navegador não consegue sem pedir permissão; tamanho do pedido limitado. O trecho de referência da voz só pode estar na pasta da voz. |
| `whisper-server` (portas 47823 e 47825) | Só `127.0.0.1`. **Sem autenticação**: veja os limites. |

## Arquivos e pasta de dados

- A pasta de dados é só do dono (`0700`), inclusive a que já existia aberta:
  a permissão é acertada a cada abertura do app. Com a permissão padrão, num
  computador com mais de uma conta os outros usuários liam o banco, o áudio,
  o log e a amostra da voz.
- A janela não escolhe caminhos: importar e exportar passam pelo seletor de
  arquivos do sistema.
- O que a janela manda como identificador é conferido antes de virar caminho:
  só se apaga uma sessão que existe (o id entra no nome de arquivos
  temporários), e a pasta e os arquivos das capturas não aceitam barras nem
  `..`.
- Todo o SQL usa parâmetros.
- Os programas externos (`afconvert`, `ffmpeg`, `screencapture`, `uv`, os
  CLIs) são chamados sem shell, com cada argumento separado. No Windows, o app
  não procura executáveis em pastas que qualquer usuário consegue criar.
- Um WAV importado com a taxa ou o número de canais absurdos no cabeçalho é
  recusado antes de ser lido (com 1 Hz, cada amostra viraria 16 mil na
  memória), e um m4a com os metadados cortados dá erro, não uma pane.

## Downloads

| O quê | Conferência |
|---|---|
| Modelos de identificação de locutores | HTTPS e SHA-256 fixo no código; gravados num arquivo temporário e só renomeados depois de conferidos |
| Modelo do Whisper (baixado pelo app na primeira abertura; pelo `make model`, para desenvolver) | Só por HTTPS, de uma revisão fixa do repositório no Hugging Face ou da cópia no Release do Recorder; tamanho e SHA-256 fixos no código (e no `Makefile`); gravado num arquivo `.part` e só renomeado depois de conferido |
| Código do whisper.cpp (`scripts/build-macos.sh`) e `whisper-server` do Windows (`scripts/build-windows.sh`) | SHA-256 fixo nos scripts |
| Modelo de voz do Intérprete | Revisão fixa do repositório no Hugging Face: o conteúdo baixado é sempre o mesmo |
| Bibliotecas da tela (`frontend/vendor/`) | No repositório, com origem e SHA-256 |
| Ambiente Python da voz | Versão fixa do `mlx-audio`; as dependências dele, não (veja os limites) |

## O que vai para o log

O log (`~/.recorder/recorder.log`) serve para entender o que deu errado depois.

- **Não vai:** o texto das falas das reuniões, os resumos, as respostas do
  chat e os pedidos feitos à IA.
- **Vai:** os nomes das pessoas e das reuniões (uma voz que recebeu nome, uma
  reunião apagada), o caminho de um áudio importado, erros dos serviços e os
  tempos de cada etapa.
- **Intérprete:** o que você disse com o atalho apertado vai para o log, em
  português e em inglês, com a tradução antes da correção. É só a sua fala, e
  é o que permite descobrir por que uma tradução saiu errada.

## Limites conhecidos

- **O `whisper-server` não tem autenticação.** Outro programa do computador,
  ou uma página do navegador, consegue mandar áudio para ele transcrever
  (disputando a GPU com a reunião) e fazê-lo cair. O das reuniões o app sobe
  de novo sozinho; o do Intérprete volta ao desligar e ligar o Intérprete. O
  áudio das suas reuniões não fica exposto por aí: o servidor não o guarda.
- **Os serviços são reconhecidos pela porta.** Se outro programa já estiver
  respondendo como um `whisper-server` na porta 47823 quando o app abre, o app
  o usa, e esse programa recebe o áudio de cada bloco. O mesmo vale para a
  porta do Ollama e para as do Intérprete. Isso pede um programa hostil
  rodando no computador antes do app: num computador de uso pessoal, quem
  consegue isso já consegue ler a pasta de dados.
- **O isolamento do `agy` depende da configuração dele.** O Recorder não
  aprova ferramentas, mas não tem como proibi-las: se o seu `agy` estiver
  configurado para aprovar ferramentas sozinho (regras de permissão,
  servidores MCP, plugins), uma instrução dita numa reunião pode ser
  executada. Com o `agy` como veio instalado, as ferramentas foram negadas
  nos testes. Quem usa o `agy` com permissões automáticas deve escolher o
  Claude ou o Ollama no Recorder.
- **As dependências Python da voz não são travadas por hash.** A versão do
  `mlx-audio` é fixa, mas as bibliotecas de que ele depende são as do dia da
  instalação.
- **Nada é cifrado em disco.** O banco, o áudio e a amostra da voz ficam como
  arquivos comuns na pasta de dados; a proteção é a da sua conta e a do disco
  (FileVault, BitLocker).
- **A amostra da voz** fica em `~/.recorder/voz`. Quem a copia tem o
  necessário para imitar a sua voz com o mesmo modelo.
- **O app não é assinado pela Apple nem pela Microsoft.** No macOS a
  assinatura é local, sem o *hardened runtime*: outro programa da sua conta
  consegue abrir o Recorder com uma biblioteca injetada e herdar as permissões
  de microfone e de gravação de tela dadas a ele.
- **Arquivos importados muito longos** são lidos inteiros na memória: um
  áudio de muitas horas (ou feito para isso) pode esgotar a memória e a
  importação falha.
- **Gravar uma conversa** pode exigir o consentimento de quem participa dela.
  O app não avisa os participantes: isso é com você.

## Como foi conferido

A revisão de 21/11/2026, antes da publicação do código, juntou a leitura de
todo o código que não é de teste, duas auditorias independentes (a da
interface e a do backend) e testes:

- **Filtro e política de conteúdo:** uma reunião com HTML hostil no resumo
  (imagem de fundo, `<label for>`, `<dialog open>`, `<img>`, `<script>`,
  `javascript:`, `onclick`, `<iframe>`, `<form>`…) foi aberta na interface de
  verdade, ligada ao app de verdade, num Chromium e num WKWebView (o motor do
  app no macOS). Nada sobrou no HTML e nenhum pedido chegou ao servidor de
  teste. O mesmo HTML posto direto na página, sem passar pelo filtro, foi
  barrado pela política de conteúdo.
- **Menções a arquivos:** chamadas reais ao `claude` e ao `agy`, com arquivos
  de teste citados no texto, antes e depois da proteção.
- **Testes automáticos** (rodam no `make check`): o servidor de arquivos sem
  o segredo, com o segredo errado, pedindo listas e por outro nome
  (`TestFileServerNeedsTheSecretAndListsNothing`); o servidor de voz de
  verdade, em Python, com os mesmos casos (`TestVoiceServerScript`); apagar
  uma sessão que não existe (`TestDeleteUnknownSessionTouchesNoFiles`); a
  interface dentro do que a política de conteúdo permite
  (`TestFrontendFitsThePageCSP`); as menções a arquivos
  (`TestClaudeGetsNoFileMentions`); os argumentos dos CLIs
  (`TestClaudeStreamsTextWithoutTools` e os do `agy`); os cabeçalhos de WAV
  (`TestDecodeRejectsAbsurdWavHeaders`); a permissão da pasta de dados
  (`TestEnsureClosesTheDataDirToOtherUsers`).
