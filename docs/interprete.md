# Intérprete: fale português, a reunião ouve inglês com a sua voz

Segurando **⌃⌥Espaço** (ou o botão "Segure para falar em inglês" na barra
lateral), a reunião deixa de ouvir você; o que você diz em português é
traduzido e entra na reunião em inglês, com a sua voz, frase a frase: ~3-5 s
depois de cada pausa com a correção pelo Claude Haiku, ~2 s só com o Whisper.
O atalho funciona com o app da reunião em primeiro plano.

A voz e o Whisper rodam no próprio Mac. **Só funciona em Mac com Apple
Silicon.**

- [Como funciona](#como-funciona)
- [Tradução e correção](#tradução-e-correção)
- [Configurar](#configurar)
- [Detalhes](#detalhes)
- [O Intérprete só fala o que você disse](#o-intérprete-só-fala-o-que-você-disse)
- [O que foi medido](#o-que-foi-medido)

## Como funciona

O app da reunião (Meet, Zoom…) usa como microfone o **BlackHole 2ch**, um
microfone virtual. Com o Intérprete ligado, o Recorder repassa o seu microfone
para ele com atraso mínimo; segurando o atalho, corta o repasse e manda só o
inglês. O seu microfone continua cortado até o inglês acabar de tocar, e você
ouve o inglês no seu fone.

```
seu microfone ──→ (atalho solto) ───────────────────────────────→ BlackHole 2ch → reunião
              └─→ (atalho apertado) → frases pelas pausas
                    → Whisper: transcreve em português e traduz para o inglês
                    → correção da tradução (opcional)
                    → voz clonada (Qwen3-TTS) ──────────────────→ BlackHole 2ch → reunião
                                                              └→ o seu fone
```

Cada frase passa por:

1. um **Whisper só do Intérprete** (porta 47825, para não esperar a
   transcrição da reunião), que a transcreve em português e a traduz para o
   inglês;
2. a **correção** da tradução (veja abaixo);
3. o **servidor de voz** (Qwen3-TTS 1.7B via MLX, porta 47824), que imita a sua
   voz a partir de uma amostra.

## Tradução e correção

O inglês é a tradução do próprio Whisper, que traduz do áudio e fica fiel ao
que você disse (traduzindo livre, um modelo de linguagem mudava frases e
tirava palavras). Mas o Whisper erra: troca palavras, às vezes inverte o
sentido ("acho que quem está acompanhando é o Mauro" virou "I think it's
following Mauro") e às vezes perde metade da frase. Por isso uma IA compara a
tradução com a transcrição em português e corrige só o que está errado, sem
resumir nem reescrever o resto.

A escolha fica em ⚙️ Configurações → Intérprete → Tradução:

| Correção | Onde roda | Atraso a mais por frase | Observações |
|---|---|---|---|
| **Claude Haiku** (padrão, se o `claude` estiver instalado) | Nuvem | ~1,5-3 s | Quase não usa memória. Um processo do `claude` fica aberto enquanto o Intérprete está ligado, para cada frase não esperar o CLI abrir (~4 s). **O texto de cada frase é enviado à Anthropic**, pelo login do Claude Code. |
| **Ollama** | Neste computador | ~0,3 s | O modelo ocupa vários GB de memória. Com o Ollama desligado, o Claude Haiku corrige. |
| **Só o Whisper** | Neste computador | nenhum | A mais rápida, sem correção. |

Se a correção falhar, vai a tradução do Whisper, com um aviso. Passando o
mouse sobre o último inglês na barra lateral, aparecem o português e a
tradução do Whisper antes da correção.

## Configurar

⚙️ Configurações → Intérprete mostra o que falta.

1. **Instale o BlackHole** no Terminal (pede a sua senha):
   `brew install blackhole-2ch`. Depois, para o macOS carregar o driver sem
   reiniciar o Mac: `sudo killall coreaudiod` (o som some por um segundo). Até
   isso, as configurações avisam que o BlackHole foi instalado mas ainda não
   foi carregado.
2. Clique em **Instalar** a voz (precisa do `uv`: `brew install uv`). O
   ambiente fica em `~/.recorder/voz`; o modelo (~4 GB) é baixado na primeira
   vez que o Intérprete for ligado.
3. **Calibre a sua voz**: leia em inglês, por uns 2 minutos (mínimo 20 s), o
   texto que aparece, com o microfone das reuniões, num lugar silencioso. O
   Recorder separa os melhores trechos da gravação, gera a mesma frase em
   inglês imitando cada um (uns 20 s no total) e você escolhe, de ouvido, a
   que mais parece com você; a voz atual entra na lista para comparar.
4. Abra (ou crie) a reunião no Recorder, ligue o **Intérprete PT → EN** na
   barra lateral e, no app da reunião, escolha **BlackHole 2ch** como
   microfone. O Intérprete só liga e só traduz com uma reunião aberta no
   Recorder; sem ela, a reunião continua ouvindo a sua voz normal. Com o
   Intérprete desligado, quem usa o BlackHole como microfone não é ouvido:
   volte para o microfone normal.
5. Para a reunião receber o áudio com qualidade cheia, deixe o BlackHole em
   48 kHz: no app *Configuração de Áudio MIDI* do macOS, escolha **BlackHole
   2ch** e o formato **48.000 Hz** (se algum app o deixar em 16 kHz, a voz
   chega com qualidade de telefone).

**Sobre a calibração.** O modelo não é retreinado: ele imita, na hora, um
trecho de referência de 9 a 20 s, e o que muda o resultado é qual trecho é
usado. Os trechos preferidos são frases inteiras, com fala contínua e que o
Whisper entendeu exatamente como no texto lido (o texto errado atrapalha a
imitação). A última calibração fica salva em `~/.recorder/voz/calibracao` para
ouvir e escolher de novo.

## Detalhes

- Enquanto você segura o atalho, a fala é cortada nas pausas: o inglês da
  primeira frase começa sem esperar você soltar. Frases seguidas esperam a
  anterior terminar de tocar.
- **Atraso** (do fim da sua fala ao começo do inglês), sem mudar o que é dito:
  - o Whisper começa com 0,3 s de pausa, sem esperar a pausa de 0,6 s que
    fecha a frase (o áudio é o mesmo; se você volta a falar, o que foi
    adiantado é descartado, antes de gastar a correção);
  - a frase seguinte é entendida (Whisper e correção) enquanto a anterior é
    falada, e não depois;
  - conferir se o que chegou é inglês roda junto com a correção, que está na
    rede, em vez de antes de tudo;
  - o `claude` é aquecido com uma frase de exemplo ao ligar o Intérprete e a
    cada renovação do processo: a primeira resposta de um processo leva ~2,7
    s, as seguintes ~1 s;
  - com uma frase em andamento, a transcrição da gravação da reunião espera a
    vez (até 15 s): as duas usam a GPU, e um bloco de 30 s no meio atrasava a
    frase em meio segundo.
- O log diz quanto levou cada etapa de cada frase ("Intérprete: etapas:
  transcrição …, tradução …, correção …, voz …, esperou …"), além do atraso
  total.
- O inglês voltando pelo microfone (sem fone, pelo alto-falante) não é
  traduzido de novo: o Whisper o reconhece como inglês com ~93% de certeza.
  Abaixo de 70%, a frase segue como português (um pedaço curto de ruído no fim
  da fala também sai "inglês", com ~40%).
- Todo aperto que não vira inglês diz por quê na barra lateral: não chegou som
  do microfone, não houve fala, era inglês (você falando inglês, ou o som do
  computador captado pelo microfone), o Whisper não entendeu nada ou a voz
  falhou. A fala dentro do aperto é medida pelo próprio aperto: medindo o
  ruído com o atalho solto, com a reunião tocando no alto-falante ou você
  falando logo antes, uma fala mais baixa era descartada sem aviso.
- O inglês que vai para o seu fone não entra na gravação da reunião (a captura
  do áudio do computador ignora o próprio Recorder); a sua fala fica gravada
  em português, pelo microfone.
- Nomes em português podem sair com a pronúncia estranha no meio do inglês.
- O modelo imita a amostra por inteiro, inclusive o ruído de fundo da
  gravação. Por isso o servidor de voz tira o ruído da amostra (e das opções
  da calibração) e acerta o volume antes de imitá-la. As cópias limpas ficam
  em `~/.recorder/voz/.sem-ruido`.
- O que acontece fica no log do app, `~/.recorder/recorder.log` (inclusive a
  tradução do Whisper antes da correção).

## O Intérprete só fala o que você disse

O áudio que ele traduz é só o do microfone padrão do Mac, enquanto o atalho
está apertado: o som do computador (a voz dos outros na reunião) não entra, a
não ser pelo ar, com alto-falante ou fone aberto.

Duas situações faziam sair, na sua voz, algo que você não disse. As duas estão
fechadas:

- **Um aperto (ou o fim dele) sem fala**, só com o ruído do microfone e o
  estalo da tecla: o Whisper transcreve como `[silêncio]`, mas a tradução do
  mesmo áudio sai inventada ("Thank you for watching!"). Agora o que não tem
  som por 0,15 s nem vai para o Whisper, e o que ele marca como não-fala
  (`[silêncio]`, `(música)`) é descartado com o aviso "Não entendi nenhuma
  fala".
- **A correção repetindo a frase anterior**: a correção guarda a conversa e,
  numa frase cortada pela pausa, devolvia junto o começo que já tinha sido
  falado ("Renato was supposed to review the" e, em seguida, "Renato was
  supposed to review the integration with the bank until Friday."). Essa
  resposta agora é descartada (vai a tradução literal da frase, com aviso),
  assim como o rótulo "EN:" e as aspas que às vezes vêm em volta.

O áudio das últimas 20 frases que foram para o Whisper fica em
`~/.recorder/interprete/` (um wav por frase, com a hora no nome; o log diz o
arquivo de cada uma): se uma tradução sair com o que você não disse, dá para
ouvir o que o microfone captou naquele aperto.

## O que foi medido

Os números deste guia vêm de testes num Mac com Apple Silicon, em novembro
de 2026:

- **Correção** (03/11): com 9 frases reais de reunião, o Haiku recuperou as
  partes que o Whisper perdeu e corrigiu os sentidos trocados, e deixou como
  estavam as frases certas. Ollama (gemma4): 0,2-0,3 s por frase; Claude Haiku
  ~1,5-3 s com o processo aberto (~3,5-4,5 s abrindo um por frase); Sonnet e o
  agy (Gemini) são mais lentos para frases curtas.
- **Isolamento do som do computador** (13/11): com um fone fechado e o
  computador tocando fala a -15 dB, nada no áudio do aperto (abaixo de -65 dB)
  nem no BlackHole.
- **Ruído da voz gerada**: com a amostra limpa, a voz gerada fica com o ruído
  ~85 dB abaixo do máximo (antes, ~45 dB: um chiado audível no retorno pelos
  alto-falantes) e mais parecida com a voz real.
