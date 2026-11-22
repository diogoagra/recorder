# Áudio

Como o Recorder capta o microfone e o som do computador, o que faz com o eco e
o que acontece com a gravação quando alguma coisa cai.

- [Microfone e áudio do computador](#microfone-e-áudio-do-computador)
- [Eco com alto-falantes](#eco-com-alto-falantes)
- [SoundSource e Audio Hijack](#soundsource-e-audio-hijack)
- [Blocos, transcrição e recuperação](#blocos-transcrição-e-recuperação)
- [O arquivo de áudio da reunião](#o-arquivo-de-áudio-da-reunião)

## Microfone e áudio do computador

Por padrão o Recorder grava o microfone **e** o áudio do computador
(reuniões, vídeos etc.), mixados em um único áudio. Isso pode ser desligado em
⚙️ Configurações → "Gravar também o áudio do computador".

- **macOS**: usa os Core Audio Process Taps (macOS 14.2 ou mais novo), sem
  instalar drivers como o BlackHole. Na primeira gravação o sistema pede a
  permissão de gravação de áudio do sistema; se o áudio do computador não
  aparecer na transcrição, verifique em *Ajustes do Sistema → Privacidade e
  Segurança → Gravação de Tela e Áudio do Sistema*.
- **Windows**: usa o loopback do WASAPI, sem configuração extra.

O microfone é captado pelo [miniaudio](https://miniaud.io). O áudio é guardado
em 16 kHz, mono, que é o que o Whisper usa.

## Eco com alto-falantes

Sem fone, o microfone capta de novo o som que sai dos alto-falantes — as vozes
da reunião, que já entram limpas pela captura do sistema. Somadas, as duas
cópias viram a mesma voz repetida com 50 a 120 ms de diferença: um eco, que
atrapalha a transcrição e a identificação de quem fala.

Antes de juntar os dois áudios, o Recorder estima como o som do sistema chega
ao microfone (o atraso e a resposta acústica do alto-falante e da sala) e
subtrai essa estimativa do microfone. Como a gravação é processada em blocos
de 30 segundos, a estimativa usa o bloco inteiro de uma vez: ela não se perde
quando alguém fala ao mesmo tempo que o som do computador, que é o ponto fraco
dos canceladores em tempo real.

Com fones de ouvido o resultado ainda é o melhor: não há eco para tirar.

## SoundSource e Audio Hijack

Esses apps (da Rogue Amoeba) interceptam o som de cada app e o tocam de novo,
~75 ms depois, pelo motor de áudio deles (ARK). Uma captura do áudio do
sistema inteiro pegaria as duas cópias, e cada voz seria gravada duas vezes,
com eco — o que também impede o modelo de voz de distinguir as pessoas.

A captura do Recorder ignora esse motor. Quem usa outro programa que retoca o
áudio do sistema e percebe as vozes duplicadas pode conferir gravando só o
áudio do computador e ouvindo o arquivo da reunião.

## Blocos, transcrição e recuperação

A gravação anda em **blocos de 30 segundos**, cortados nas pausas da fala
quando possível. Cada bloco vai para uma fila e é transcrito pelo
`whisper-server`, um processo à parte que o app sobe ao abrir (a janela não
espera por ele) e vigia.

- O `whisper-server` usa a porta própria do Recorder, **47823**, e só aceita
  conexões do próprio computador (127.0.0.1). Se outro programa estiver nessa
  porta, o Whisper aparece como "Indisponível" ao abrir o app.
- **Se o Whisper cair** ou travar no meio de uma gravação, o áudio continua
  sendo gravado, o app sobe o servidor de novo e os blocos esperam ele voltar.
  Se ele não voltar, os blocos ficam guardados e a transcrição é retomada na
  próxima vez que o app abrir.
- **Se o app fechar** (ou travar) no meio de uma gravação, o que já foi
  gravado está no disco: na próxima abertura o áudio é juntado à reunião, o
  que faltou é transcrito e o resumo é feito.
- Um `whisper-server` que ficou órfão de uma execução anterior (o app foi
  fechado à força) é encerrado na abertura; sem isso, dois servidores na mesma
  porta dividiriam as transcrições.
- Trechos em que o detector de voz não ouviu ninguém são descartados: é o
  Whisper inventando frases sobre silêncio.

## O arquivo de áudio da reunião

Cada reunião tem um arquivo em `~/.recorder/sessions_audio/` (m4a no macOS,
wav no Windows).
Ao retomar uma gravação, o trecho novo é juntado sem recomprimir o que já
estava gravado. É desse arquivo que saem o player da reunião, os momentos
clicáveis do resumo e o **Reprocessar**, que refaz a transcrição do zero.
