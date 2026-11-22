# Quem está falando

A transcrição identifica as diferentes vozes e marca cada fala como **Pessoa
1**, **Pessoa 2** etc. Isso é feito no próprio computador com o
[sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) (segmentação do pyannote
e "impressões digitais" de voz do 3D-Speaker), sem Python e sem enviar áudio
para fora.

- [Como as vozes são separadas](#como-as-vozes-são-separadas)
- [Dar nome às pessoas](#dar-nome-às-pessoas)
- [Reconhecer pela voz](#reconhecer-pela-voz)
- [Nomes tirados da conversa](#nomes-tirados-da-conversa)
- [Participantes e glossário](#participantes-e-glossário)
- [Limitações](#limitações)

## Como as vozes são separadas

- Na primeira vez que o app abre, ele baixa os modelos (~45 MB) para
  `~/.recorder/models/`, conferidos por SHA-256. Até terminar o download, as
  falas ficam sem identificação.
- Cada voz fica guardada na sessão, então a mesma pessoa mantém o rótulo ao
  longo da gravação, mesmo pausando e retomando.
- Trechos em que o detector de voz não ouviu ninguém são descartados: é o
  Whisper inventando frases sobre ruído ou silêncio ("Obrigado por
  assistir!").
- Trechos curtos (menos de 4 segundos de voz) nunca viram uma pessoa nova:
  ficam com a pessoa de voz mais parecida. A impressão digital de um trecho
  curto é pouco confiável, e sem essa regra uma conversa de 2 pessoas chegava
  a ter 9. Por isso quem só diz uma frase curta na reunião ("Ok, obrigado")
  aparece junto com outra pessoa.
- Uma voz só entra na média de alguém da sessão se for claramente dela; a que
  é apenas parecida fica com o rótulo, mas não muda a média. Trechos com duas
  pessoas misturadas iam aos poucos transformando a voz de uma na da outra.
- No fim da gravação, cada "Pessoa N" com menos de 15 segundos de fala na
  sessão inteira é juntada à pessoa mais parecida. Costumam ser fala
  sobreposta, murmúrio ou ruído; quem realmente falou só isso na reunião acaba
  junto com outra pessoa. Nomes que você deu nunca são juntados.
- **Reprocessar** a sessão refaz a identificação do zero, usando o áudio salvo
  da sessão.

## Dar nome às pessoas

Na aba Transcrição, clique em "Pessoa N" e diga quem é. Todas as falas daquela
voz passam a usar o nome, e a voz fica guardada para o Recorder reconhecer a
pessoa nas próximas reuniões do workspace. Se a mesma pessoa aparecer como
dois rótulos, dê o mesmo nome aos dois para juntá-los.

As vozes guardadas ficam em ⚙️ Configurações → Workspace → "Vozes conhecidas",
onde dá para:

- **Esquecer** uma pessoa (com confirmação);
- **Renomear**, para corrigir o nome de uma voz conhecida (a voz guardada como
  "Diego" é do Diogo). Nas reuniões do workspace, as falas e as tarefas com o
  nome antigo também mudam; os resumos já feitos ficam com o nome antigo até
  serem refeitos. Se já existir uma voz com o nome novo, as duas viram uma só.

**A voz guardada se corrige.** O Recorder guarda a voz que recebeu o nome em
cada reunião, e a voz conhecida da pessoa é a combinação das que concordam
entre si. Dar o nome à voz errada numa reunião não estraga as outras, e
corrigir (ou desfazer) o nome tira aquela reunião da conta. Ao dar um nome que
as reuniões anteriores do workspace já usavam para a mesma voz, elas entram
junto: quem teve a voz esquecida volta a ser reconhecido com o que já se sabia
dele.

## Reconhecer pela voz

Numa reunião nova, a voz ganha o nome de uma pessoa conhecida quando já
acumulou pelo menos 10 segundos de fala e é claramente a dela: parecida com a
voz guardada e bem mais do que com a de qualquer outra pessoa conhecida. Até
lá aparece como "Pessoa N", e as falas já transcritas mudam junto quando o
nome chega.

Depois disso, só fica com o nome a voz que continua lembrando a guardada.
Decidir o nome nos primeiros segundos não funciona: numa reunião que começa
com duas pessoas falando juntas, uma delas passaria a reunião inteira com o
nome da outra.

## Nomes tirados da conversa

No fim da gravação, antes do resumo, o Recorder procura na conversa o nome de
cada "Pessoa N": alguém é chamado e responde ("Ana, e você?"), é citado pelo
que acabou de dizer ("como o João falou") ou se apresenta.

- Com **pelo menos três falas de outras pessoas** apontando para a mesma voz,
  ela já recebe o nome, e a voz fica guardada para as próximas reuniões. O
  painel "Quem é quem?" mostra o trecho que comprova, com **Não é** para
  desfazer.
- Com menos pistas, o nome vira uma **sugestão** para você confirmar ou
  ignorar.
- O mesmo nome escrito de jeitos diferentes pela transcrição (Antoni, Anthony,
  Antony) conta como um só.
- Com uma IA da nuvem, a pergunta é feita três vezes ao mesmo tempo e vale o
  que a maioria concluir: nos testes, cerca de uma em cada seis respostas
  vinha confusa (duas pessoas trocadas, ou a voz com o nome de quem só foi
  chamado junto).

Errar o nome é pior do que deixar "Pessoa N": por isso o nome automático pede
três pistas, e não duas.

O resumo e o chat recebem as falas já identificadas, e os nomes tirados da
conversa são dados antes de o resumo ser escrito.

## Participantes e glossário

Os dois ficam em ⚙️ Configurações → Workspace e valem só para o workspace
aberto.

- **Participantes:** liste quem costuma estar nas reuniões, um por linha, com
  o nome como deve aparecer e, entre parênteses, outros jeitos de chamar a
  pessoa:

  ```
  Maria Souza
  Oleksandr Petrenko (Alexander, Alex)
  João Lima
  ```

  Os nomes tirados da conversa, o resumo e as tarefas passam a usar essa
  grafia, inclusive para quem é só citado ou recebe uma tarefa sem ter falado,
  e o Whisper recebe a lista para acertar os nomes na transcrição.
- **Glossário:** liste nomes de sistemas, clientes e siglas. Eles, os nomes dos
  participantes e os das vozes conhecidas ajudam o Whisper a escrever esses
  termos corretamente. O Whisper só aproveita uma lista curta (uns 220
  tokens): ponha o que ele costuma errar, não tudo.

## Limitações

- Falas sobrepostas, trechos muito curtos e vozes parecidas podem ser
  atribuídos à pessoa errada.
- A mesma pessoa com outro microfone fica menos parecida com a voz guardada, e
  pode aparecer como "Pessoa N" até você confirmar o nome naquela reunião.
- Áudio com a mesma voz duplicada com alguns milissegundos de atraso (o que
  alguns programas de áudio fazem com o som do computador; veja
  [audio.md](audio.md)) torna as vozes indistinguíveis para o modelo.
