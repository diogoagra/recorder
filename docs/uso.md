# Uso

Como o Recorder se organiza no dia a dia: workspaces, gravação, importação de
áudio, resumo, tarefas, chat, notas e onde tudo fica guardado.

- [Workspaces](#workspaces)
- [Gravar](#gravar)
- [Importar áudio](#importar-áudio)
- [Resumo, tarefas e chat](#resumo-tarefas-e-chat)
- [Notas e capturas de tela](#notas-e-capturas-de-tela)
- [Exportar](#exportar)
- [Configurações](#configurações)
- [Onde os dados ficam](#onde-os-dados-ficam)
- [Modo terminal](#modo-terminal)

## Workspaces

As reuniões ficam separadas em **workspaces**, por exemplo um para cada
emprego ou cliente, para não misturar umas com as outras. O workspace aberto
aparece no topo da barra lateral; a lista mostra só as reuniões dele.

- **Criar:** no seletor de workspace, escolha "＋ Novo workspace…". Ao lado
  ficam os botões de renomear e de apagar.
- **O que é de cada workspace:** as reuniões, a lista de participantes, as
  vozes conhecidas e o glossário. Uma pessoa nomeada num workspace só é
  reconhecida nas reuniões dele (a sua própria voz precisa ser nomeada uma vez
  em cada um). Já a IA escolhida e a gravação do áudio do computador valem
  para o app todo.
- **Chat:** "Pesquisar em todas as reuniões deste workspace" busca só nas
  reuniões do workspace aberto.
- **Mover uma reunião:** com mais de um workspace, o botão de pasta ao lado de
  renomear a sessão move a reunião aberta para outro workspace. As pessoas que
  têm nome nela passam a ser reconhecidas também lá.
- **Apagar** um workspace apaga as reuniões dele (áudio, transcrição, resumo e
  chat), os participantes, as vozes conhecidas e o glossário. Sempre fica pelo
  menos um. O que é apagado (reunião, workspace, voz esquecida) fica
  registrado no log do app.
- As reuniões de antes dos workspaces ficam no workspace **Principal**, que
  pode ser renomeado.

## Gravar

Crie uma sessão (o **+** da barra lateral) e clique em **Gravar**. O áudio é
gravado em blocos de 30 segundos; cada bloco é transcrito em seguida e as
falas vão aparecendo na aba Transcrição. Ao parar, o Recorder procura os nomes
de quem falou, escreve o resumo e a lista de tarefas e sugere um nome para a
reunião.

- Por padrão entram o microfone **e** o áudio do computador. Isso se desliga
  em ⚙️ Configurações → "Gravar também o áudio do computador". Os detalhes da
  captura estão em [audio.md](audio.md).
- Pausar e retomar continua a mesma reunião: o trecho novo é juntado ao áudio
  já gravado.
- Fechar o app no meio de uma gravação não perde o que já foi gravado: o que
  faltou transcrever e o resumo são feitos na próxima abertura.
- **Reprocessar** (o botão de seta circular) refaz a transcrição e a
  identificação de quem fala do zero, a partir do áudio salvo da reunião.

## Importar áudio

Uma reunião gravada em outro lugar (no celular, por exemplo) vira uma sessão
como as gravadas no app: clique no botão de importar (a seta para cima ao lado
do **+**, na barra lateral, ou **Importar Áudio** na tela inicial) e escolha o
arquivo.

- A transcrição, a identificação de quem fala e o resumo são feitos em segundo
  plano; as falas vão aparecendo na aba Transcrição.
- A sessão fica com a data em que o áudio foi gravado, tirada do próprio
  arquivo (ou da data de modificação dele, quando o formato não guarda isso).
- Formatos: m4a (Gravador do iPhone), mp3, wav, aac, ogg/opus (áudios do
  WhatsApp), flac, amr, 3gp e vídeos mp4/mov. Áudios estéreo têm os dois
  canais juntados.
- **Windows:** sem o `afconvert` do macOS, só WAV é lido direto; os outros
  formatos precisam do [ffmpeg](https://ffmpeg.org/) instalado e no PATH.

## Resumo, tarefas e chat

- **Resumo ao vivo:** ao parar a gravação, o resumo aparece enquanto é
  escrito, citando o momento do áudio de cada ponto (ex: `12:34`). Clicar no
  momento toca o áudio dali, no player que fica embaixo das abas (com
  velocidade de 0,75× a 2× e volume). O botão **Refazer** gera o resumo de
  novo, com instruções suas se quiser ("foque nas decisões técnicas").
- **Tarefas:** as tarefas combinadas viram uma lista com título, responsável,
  prazo e momento, com caixa para marcar como feita. Só entra o que alguém
  disse que vai fazer, o que foi pedido a alguém pelo nome e a pendência que
  ficou com alguém; o que foi apenas discutido, os requisitos e o que ficou
  "para depois" vão para a ata ou para as decisões. O que resulta na mesma
  entrega é uma tarefa só, com todos os responsáveis.
- **Quem é quem:** no fim da gravação, antes do resumo, o Recorder procura na
  conversa o nome de cada "Pessoa N". Como isso funciona está em
  [locutores.md](locutores.md).
- **Chat com busca:** cada pergunta procura os trechos mais relacionados em
  toda a reunião (ou em todas as reuniões do workspace, marcando a opção
  abaixo do campo), além das falas mais recentes. Na primeira pergunta o
  Recorder baixa no Ollama o modelo de busca `embeddinggemma` (~600 MB). O
  chat responde só com o que está na transcrição e no resumo; sem a
  informação, diz que não tem dados suficientes.

A IA que escreve tudo isso é escolhida nas configurações: veja
[assistente.md](assistente.md).

## Notas e capturas de tela

- **Notas:** a aba **Notas** da reunião tem um campo livre para anotar durante
  a conversa. O texto é salvo sozinho enquanto você escreve e fica guardado
  com a reunião. A busca da lista de reuniões também procura nas notas.
- **Capturar a tela** (macOS): o botão **Capturar tela** da aba Notas funciona
  como o ⌘⇧4: arraste para escolher uma área ou aperte Espaço e clique numa
  janela (Esc desiste). A janela do Recorder sai da frente durante a escolha.
  A imagem fica guardada com a reunião, numa fileira embaixo das notas; clicar
  nela abre em tamanho grande, com as opções de mostrar o arquivo no Finder e
  de apagar.
- **Atalho ⌃⌥S:** com uma reunião aberta no Recorder, o atalho captura a tela
  em qualquer app, sem sair da chamada. Sem reunião aberta, a combinação
  continua sendo dos outros apps.
- **Momento da reunião:** uma captura feita durante a gravação guarda o
  momento do áudio (ex: 12:34); clicar nele toca o que se falava ali. Fora da
  gravação, ela mostra o dia e a hora.
- **Permissão:** na primeira captura o macOS pede a permissão de gravação de
  tela. Ative o Recorder em *Ajustes do Sistema → Privacidade e Segurança →
  Gravação do Áudio do Sistema e da Tela* e abra o app de novo. Como o app é
  assinado localmente, o macOS pode pedir de novo depois de cada build.
- A captura não faz o som do obturador, que entraria na gravação do áudio do
  computador.

## Exportar

O botão de exportar salva num arquivo a aba que estiver aberta: o resumo com
as tarefas, a transcrição, o chat ou as notas (com a indicação de onde estão
as capturas de tela). O texto sai em Markdown, que também se lê bem como texto
puro.

## Configurações

As configurações são uma tela própria, aberta pelo ⚙️ no rodapé da barra
lateral; cada mudança é salva na hora.

| Seção | O que tem |
|---|---|
| **IA do assistente** | Ollama, agy ou Claude, e o modelo de cada um ([assistente.md](assistente.md)) |
| **Gravação** | Gravar ou não o áudio do computador junto com o microfone |
| **Intérprete** | O que falta instalar, a calibração da voz e a correção da tradução ([interprete.md](interprete.md)) |
| **Workspace** | Participantes, glossário e vozes conhecidas do workspace aberto ([locutores.md](locutores.md)) |

## Onde os dados ficam

Tudo o que o app guarda fica numa pasta oculta na pasta do usuário:
`~/.recorder/` (ou a pasta indicada na variável de ambiente `RECORDER_HOME`).
O botão de pasta, no rodapé da barra lateral, a abre. A pasta é acessível só
à sua conta do computador.

| Caminho | Conteúdo |
|---|---|
| `transcripts.db` | Banco SQLite: workspaces, sessões, transcrições, resumos, tarefas, chat, notas, vozes e configurações |
| `backups/` | Cópias do banco feitas antes de cada atualização do esquema (ficam as 3 últimas) |
| `sessions_audio/` | O áudio das reuniões, um arquivo por reunião; ao retomar uma gravação, o trecho novo é juntado sem recomprimir o que já estava gravado |
| `tmp_audio/` | Áudio temporário da gravação em andamento e dos blocos esperando transcrição |
| `screenshots/` | Capturas de tela, uma pasta por reunião (PNG, com as miniaturas em `thumbs/`); são apagadas junto com a reunião |
| `models/` | Modelos de identificação de locutores |
| `voz/` | Intérprete: ambiente Python, a amostra da sua voz e a última calibração |
| `interprete/` | Áudio das últimas 20 frases do Intérprete, para conferir o que o microfone captou |
| `recorder.log` | Log do app (o anterior vira `recorder.log.1` quando passa de 5 MB). O texto das falas das reuniões não vai para o log; as frases ditas pelo Intérprete, sim ([o que vai para o log](seguranca.md#o-que-vai-para-o-log)) |

## Modo terminal

O executável também funciona sem janela (compilado com `go build -o recorder .`):

- **Escuta:** `./recorder daemon` grava e transcreve até ser encerrado, numa
  sessão criada para a execução ("dd/mm/aaaa - Gravação pelo terminal"), que
  depois aparece no app.
- **Pergunta:** `./recorder query "Resuma sobre o que eu estava falando"`
  consulta o Ollama com as falas mais recentes.

É um modo antigo (legado): os recursos novos estão só no app.
