# Limitações conhecidas

O que ainda não funciona, ou funciona com ressalvas. As de segurança e de
privacidade estão em [seguranca.md](seguranca.md#limites-conhecidos).

## Plataformas

- **macOS é a plataforma principal.** A captura do áudio do computador pede
  macOS 14.2 ou mais novo.
- **O Intérprete só funciona em Mac com Apple Silicon**, e precisa do
  BlackHole instalado.
- **Windows:** a transcrição roda no processador (mais lenta), não há
  Intérprete, captura de tela nem atalhos globais, e importar áudio que não
  seja WAV pede o ffmpeg. Detalhes em [windows.md](windows.md).
- **O app não é assinado** pela Apple nem pela Microsoft: a primeira abertura
  em outro computador pede uma liberação manual, e no macOS a permissão de
  gravação de tela pode ser pedida de novo depois de cada build.

## Transcrição

- **A primeira abertura precisa de internet:** o modelo de transcrição (539
  MB) é baixado uma vez, e até terminar não dá para gravar. Num computador
  sem conexão, o arquivo pode ser copiado à mão
  ([primeira abertura](uso.md#primeira-abertura)).
- O Whisper inventa frases sobre silêncio e ruído ("Obrigado por assistir!").
  O Recorder descarta os trechos em que o detector de voz não ouviu ninguém,
  mas uma ou outra frase inventada ainda pode passar.
- O idioma é detectado a cada bloco de 30 segundos: um bloco em que se fala
  em dois idiomas pode sair todo num só.
- Nomes próprios e siglas saem errados sem a ajuda da lista de participantes
  e do glossário, e o Whisper só aproveita uma lista curta.
- Enquanto o Intérprete entende uma frase, a transcrição da reunião espera a
  vez (até 15 s): as duas usam a GPU.

## Quem está falando

- Falas sobrepostas, trechos muito curtos e vozes parecidas podem ser
  atribuídos à pessoa errada.
- Quem só diz uma frase curta na reunião aparece junto com outra pessoa: um
  trecho com menos de 4 segundos de voz nunca vira uma pessoa nova, e quem
  falou menos de 15 segundos na reunião inteira é juntado à voz mais parecida.
- A mesma pessoa com outro microfone fica menos parecida com a voz guardada e
  pode aparecer como "Pessoa N".
- Uma voz conhecida vale só no workspace em que recebeu o nome: a sua própria
  voz precisa ser nomeada uma vez em cada um.
- Corrigir o nome de uma voz conhecida muda as falas e as tarefas, mas os
  resumos já feitos ficam com o nome antigo até serem refeitos.

## Resumo, tarefas e chat

- Com o Ollama, o contexto é pequeno (~16 mil tokens): reuniões longas são
  resumidas em partes, o que perde detalhes em relação a ler tudo de uma vez.
- A qualidade do resumo e das tarefas é a do modelo escolhido. A IA erra: os
  momentos do áudio ao lado de cada ponto existem para conferir.
- Os nomes tirados da conversa também erram. Por isso um nome só é dado
  sozinho com três pistas; com menos, fica como sugestão.
- A busca do chat em todas as reuniões do workspace precisa do Ollama rodando,
  mesmo com outra IA escolhida.

## Intérprete

- O inglês começa de 2 a 5 segundos depois de cada pausa.
- A tradução do Whisper erra, e a correção não pega tudo.
- Nomes em português podem sair com a pronúncia estranha no meio do inglês.
- A voz imita uma amostra de 9 a 20 segundos: parece com você, mas não é
  idêntica, e depende de uma boa calibração.
- Com o Intérprete desligado, quem deixou o BlackHole como microfone da
  reunião não é ouvido.
- O modelo de voz ocupa ~4 GB em disco.

## Dados

- Não há sincronização entre computadores nem cópia de segurança automática
  das reuniões (só do banco, antes de cada mudança de esquema).
- No Windows o áudio das reuniões fica em WAV, sem compressão.
