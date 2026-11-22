package assistant

import (
	"fmt"
	"recorder/internal/roster"
	"recorder/internal/storage"
	"strings"
)

// SummaryPrompt pede o resumo da sessão em Markdown.
const SummaryPrompt = `Atue como um assistente executivo avançado. Baseado na transcrição a seguir, identifique o contexto do áudio (reunião, aula, vídeo de YouTube, etc) e gere um documento estruturado em Markdown.
IMPORTANTE: NÃO inclua NENHUMA frase introdutória, justificativa ou texto conversacional (como "Aqui está o resumo", "Como assistente, eu...", "Segue o documento"). Retorne DIRETO e EXCLUSIVAMENTE o conteúdo Markdown solicitado.
FORMATAÇÃO: NÃO utilize formatação matemática em LaTeX, símbolos matemáticos, ou blocos complexos (como $\rightarrow$). Use apenas texto simples. Para setas, use '->'.
IDIOMA: escreva sempre em português do Brasil, mesmo que a conversa (ou parte dela) seja em outro idioma.
MOMENTOS: cada fala da transcrição começa com o momento do áudio entre colchetes, como "[12:34] Pessoa 3: ...". Sempre que citar uma decisão, tarefa ou ponto importante, coloque no fim da frase o momento da fala correspondente, entre colchetes, por exemplo: "Ficou decidido migrar os dados ano a ano [12:34]." Use colchetes apenas para momentos.

Se for uma **reunião**:
1. **Ata da Reunião**: Resumo detalhado.
2. **Decisões Tomadas**: Lista de decisões.
3. **Action Items (Tarefas)**: Tabela com Tarefa, Responsável e Prazo, seguindo as regras de TAREFAS abaixo.
4. **E-mail de Follow-up**: Rascunho para os participantes.

Se for **outro tipo de áudio** (aula, podcast, vídeo):
1. **Resumo Principal**: Síntese do assunto.
2. **Pontos-Chave**: Principais ideias discutidas.
3. **Aprendizados/Conclusões**: O que se tira de lição do áudio.

TAREFAS: a lista deve ser curta e certa; quem lê vai cobrar cada linha de alguém.
- É tarefa o que alguém disse que vai fazer ("vou revisar", "eu vejo isso com o fulano"), o que foi pedido diretamente a alguém, que aceitou ou não recusou ("Ana, coloca isso no documento." "Ok."), e a pendência concreta que ficou com alguém para o trabalho andar ("preciso da lista de variações" - "veja com o Pablo": quem precisa da lista é o responsável por consegui-la).
- NÃO são tarefas: o que foi apenas discutido ou explicado; requisitos e o funcionamento esperado de um sistema; ideias, possibilidades e o que ficou para "depois", "talvez" ou "mais para frente" sem alguém assumir agora (isso vai na ata ou nas decisões); o que já está feito.
- Uma linha por entrega: o que resulta no mesmo documento, na mesma lista ou na mesma mudança é UMA tarefa, com todos os responsáveis, mesmo que tenha sido combinado em momentos diferentes da reunião. Não repita a mesma tarefa com outras palavras.
- Escreva cada tarefa como "**Título curto**: o que fazer, em uma frase", com o momento no fim.
- Responsável: quem vai FAZER, não quem pediu, pelo nome. Quando o pedido é feito a pessoas chamadas pelo nome ("Ana, Pedro, amanhã vocês colocam tudo no documento"), elas são as responsáveis, mesmo que quase não tenham falado. Se forem várias, separe os nomes com vírgula. Nunca escreva "equipe", "todos" ou um papel no lugar do nome se o nome é conhecido.
- Prazo: só se foi dito; senão, deixe a célula vazia.
- Na dúvida se algo é uma tarefa, deixe de fora.

NOMES: use o nome de quem disse, decidiu ou ficou responsável por cada coisa. Quando as falas vierem identificadas como Pessoa 1, Pessoa 2 etc., use esses rótulos, a não ser que a própria conversa revele o nome. Se houver uma lista de participantes conhecidos, escreva os nomes como estão nela (sem os apelidos entre parênteses), inclusive os de quem é citado ou recebe uma tarefa; a transcrição costuma errar a grafia dos nomes.`

const partNotesPrompt = `Você está ajudando a resumir uma reunião longa, uma parte de cada vez. Abaixo está uma parte da transcrição, com cada fala marcada pelo momento do áudio e por quem falou, como "[12:34] Pessoa 3: ...".
Escreva anotações detalhadas, em tópicos, de tudo o que for relevante nesta parte: assuntos discutidos, decisões, tarefas (o que alguém disse que vai fazer ou aceitou fazer, com responsável e prazo, quando houver), números, datas, nomes e quem disse o quê. Se houver uma lista de participantes conhecidos, escreva os nomes como estão nela. No fim de cada tópico, coloque entre colchetes o momento da fala correspondente, como [12:34].
Escreva em português do Brasil, mesmo que a conversa seja em outro idioma. Não escreva introdução nem conclusão e não invente nada que não esteja no texto.
FORMATAÇÃO: NÃO utilize formatação matemática em LaTeX ou símbolos matemáticos. Use apenas texto simples.`

const condenseNotesPrompt = `Abaixo estão anotações de partes seguidas de uma reunião longa. Junte-as em anotações mais curtas, em tópicos, sem perder assuntos, decisões, tarefas, responsáveis, prazos, números, quem disse o quê e os momentos [mm:ss].
Escreva em português do Brasil. Não escreva introdução nem conclusão e não invente nada que não esteja no texto.
FORMATAÇÃO: NÃO utilize formatação matemática em LaTeX ou símbolos matemáticos. Use apenas texto simples.`

// Summarize gera o resumo da sessão inteira, seguindo instructions (ex: "foque
// nas decisões técnicas") se não for vazio.
// Quando a transcrição não cabe numa chamada (ContextChars da IA; no agy cabe
// quase sempre), cada parte vira anotações e o resumo final é feito sobre as
// anotações, para o começo da reunião não ficar de fora. onText recebe o resumo final enquanto ele é escrito.
// people é a lista de participantes do workspace, para os nomes saírem
// escritos como nela.
func (a *Assistant) Summarize(instructions string, transcripts []storage.Transcript, people []roster.Person, onText func(string)) (string, error) {
	prompt := SummaryPrompt
	if instructions != "" {
		prompt += fmt.Sprintf("\n\nINSTRUÇÕES ADICIONAIS DO USUÁRIO (Você DEVE manter rigorosamente a estrutura de tópicos solicitada acima - Ata, Decisões, Action Items e E-mail - mas aplique esta modificação ao formato interno deles):\n%s\n", instructions)
	}

	budget := a.LLM.ContextChars()
	known := participantsBlock(people)
	parts := chunkLines(transcriptLines(transcripts), budget)
	if len(parts) == 1 {
		return a.LLM.StreamRaw(prompt, known+"--- TRANSCRIÇÕES ---\n"+parts[0]+"--------------------\n\nPor favor, gere o resumo.", onText)
	}

	notesPrompt := partNotesPrompt
	if instructions != "" {
		notesPrompt += "\nDê atenção especial a: " + instructions
	}
	notes := make([]string, len(parts))
	for i, part := range parts {
		a.status(fmt.Sprintf("Resumindo a reunião (parte %d de %d)...", i+1, len(parts)))
		note, err := a.LLM.AskRaw(notesPrompt, known+fmt.Sprintf("--- TRANSCRIÇÃO (parte %d de %d) ---\n%s", i+1, len(parts), part))
		if err != nil {
			return "", err
		}
		notes[i] = fmt.Sprintf("--- Parte %d de %d ---\n%s\n", i+1, len(parts), strings.TrimSpace(note))
	}

	// Em reuniões muito longas nem as anotações cabem: condensa em rodadas
	for round := 0; round < 3; round++ {
		groups := chunkLines(notes, budget)
		if len(groups) == 1 {
			break
		}
		a.status("Juntando as anotações da reunião...")
		notes = notes[:0]
		for _, group := range groups {
			note, err := a.LLM.AskRaw(condenseNotesPrompt, group)
			if err != nil {
				return "", err
			}
			notes = append(notes, strings.TrimSpace(note)+"\n")
		}
	}

	a.status("Gerando resumo da sessão...")
	return a.LLM.StreamRaw(prompt, known+"--- ANOTAÇÕES DA REUNIÃO, EM ORDEM ---\n"+strings.Join(notes, "\n")+"--------------------\n\nPor favor, gere o resumo.", onText)
}
