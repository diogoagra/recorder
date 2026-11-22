package transcript

import (
	"recorder/internal/diarization"
	"recorder/internal/transcriber"
	"reflect"
	"testing"
)

func TestGroupBySpeaker(t *testing.T) {
	segs := []transcriber.Segment{
		{Start: 0, End: 3, Text: "Bom dia a todos."},
		{Start: 3, End: 6, Text: "Vamos começar."},
		{Start: 6, End: 7, Text: "[Música]"},
		{Start: 7, End: 10, Text: "Bom dia!"},
		{Start: 10, End: 14, Text: "Primeiro item da pauta."},
	}
	speakers := []diarization.Segment{
		{Start: 0, End: 6.2, Speaker: "Pessoa 1"},
		{Start: 7.1, End: 9.8, Speaker: "Pessoa 2"},
		{Start: 10.1, End: 14, Speaker: "Pessoa 1"},
	}
	// Cada fala começa na sua primeira palavra, depois da marcação [Música]
	want := []Utterance{
		{Speaker: "Pessoa 1", Text: "Bom dia a todos. Vamos começar.", Start: 0},
		{Speaker: "Pessoa 2", Text: "Bom dia!", Start: 7},
		{Speaker: "Pessoa 1", Text: "Primeiro item da pauta.", Start: 10},
	}
	if got := groupBySpeaker(segs, speakers, true); !reflect.DeepEqual(got, want) {
		t.Fatalf("esperava %+v, veio %+v", want, got)
	}
}

// Trocas de pessoa dentro de um mesmo trecho do Whisper, como numa pergunta
// respondida na hora
var turnTakers = []diarization.Segment{
	{Start: 24.8, End: 32.2, Speaker: "Pessoa 1"},
	{Start: 32.7, End: 38, Speaker: "Pessoa 2"},
}

func TestGroupBySpeakerSplitsSentencesInsideSegment(t *testing.T) {
	segs := []transcriber.Segment{{Start: 30, End: 34, Text: " Quem pode cuidar disso? Eu assumo."}}
	// O início da segunda frase é estimado pelo tamanho do texto
	want := []Utterance{
		{Speaker: "Pessoa 1", Text: "Quem pode cuidar disso?", Start: 30},
		{Speaker: "Pessoa 2", Text: "Eu assumo.", Start: 30 + 4*23.0/34},
	}
	if got := groupBySpeaker(segs, turnTakers, true); !reflect.DeepEqual(got, want) {
		t.Fatalf("esperava %+v, veio %+v", want, got)
	}
}

func TestGroupBySpeakerWordLevelSegments(t *testing.T) {
	// Palavra por palavra (max_len=1): o "Eu" vem com o tempo adiantado, mas a
	// frase "Eu assumo." como um todo é da Pessoa 2
	segs := []transcriber.Segment{
		{Start: 30, End: 30.4, Text: " Quem"},
		{Start: 30.4, End: 30.8, Text: " pode"},
		{Start: 30.8, End: 31.4, Text: " cuidar"},
		{Start: 31.4, End: 32, Text: " disso?"},
		{Start: 32, End: 32.4, Text: " Eu"},
		{Start: 32.4, End: 34, Text: " assumo."},
		{Start: 34, End: 34.1, Text: " [BLANK_AUDIO]"},
	}
	want := []Utterance{
		{Speaker: "Pessoa 1", Text: "Quem pode cuidar disso?", Start: 30},
		{Speaker: "Pessoa 2", Text: "Eu assumo.", Start: 32},
	}
	if got := groupBySpeaker(segs, turnTakers, true); !reflect.DeepEqual(got, want) {
		t.Fatalf("esperava %+v, veio %+v", want, got)
	}
}

func TestGroupBySpeakerWithoutDiarization(t *testing.T) {
	segs := []transcriber.Segment{
		{Start: 0, End: 3, Text: "Bom dia a todos."},
		{Start: 3, End: 6, Text: "Vamos começar."},
	}
	want := []Utterance{{Text: "Bom dia a todos. Vamos começar."}}
	if got := groupBySpeaker(segs, nil, false); !reflect.DeepEqual(got, want) {
		t.Fatalf("esperava %+v, veio %+v", want, got)
	}
}

func TestGroupBySpeakerUntimedTranscription(t *testing.T) {
	// Servidores que não informam os tempos: só dá para rotular se uma pessoa falou
	segs := []transcriber.Segment{{Text: "Bom dia a todos."}, {Text: "Vamos começar."}}

	one := []diarization.Segment{{Start: 0, End: 2, Speaker: "Pessoa 1"}, {Start: 3, End: 6, Speaker: "Pessoa 1"}}
	if got := groupBySpeaker(segs, one, true); len(got) != 1 || got[0].Speaker != "Pessoa 1" {
		t.Fatalf("uma pessoa só: esperava Pessoa 1, veio %+v", got)
	}

	two := []diarization.Segment{{Start: 0, End: 2, Speaker: "Pessoa 1"}, {Start: 3, End: 6, Speaker: "Pessoa 2"}}
	if got := groupBySpeaker(segs, two, true); len(got) != 1 || got[0].Speaker != "" {
		t.Fatalf("duas pessoas: esperava fala sem rótulo, veio %+v", got)
	}
}

func TestGroupBySpeakerDropsTextWithoutSpeech(t *testing.T) {
	// O Whisper inventa frases sobre ruído; o detector não ouviu ninguém ali
	segs := []transcriber.Segment{
		{Start: 0, End: 3, Text: " Bom dia a todos."},
		{Start: 9, End: 15, Text: " Thanks"},
		{Start: 15, End: 20, Text: " for watching!"},
	}
	speakers := []diarization.Segment{{Start: 0.2, End: 3.1, Speaker: "Pessoa 1"}}
	want := []Utterance{{Speaker: "Pessoa 1", Text: "Bom dia a todos.", Start: 0}}
	if got := groupBySpeaker(segs, speakers, true); !reflect.DeepEqual(got, want) {
		t.Fatalf("esperava %+v, veio %+v", want, got)
	}
	// Bloco inteiro sem fala e sem os tempos da transcrição
	if got := groupBySpeaker([]transcriber.Segment{{Text: "Obrigado."}}, nil, true); got != nil {
		t.Fatalf("sem fala detectada não deveria sobrar nada, veio %+v", got)
	}
}

func TestGroupBySpeakerStripsSplitMarkers(t *testing.T) {
	// Palavra por palavra, a marcação chega quebrada em pedaços
	segs := []transcriber.Segment{
		{Start: 0, End: 0.5, Text: " ["},
		{Start: 0.5, End: 2, Text: " Música"},
		{Start: 2, End: 2.2, Text: " ]"},
		{Start: 2.2, End: 2.6, Text: " Vamos"},
		{Start: 2.6, End: 3, Text: " lá."},
		{Start: 3, End: 3.5, Text: " (risos)"},
	}
	speakers := []diarization.Segment{{Start: 0, End: 3.5, Speaker: "Pessoa 1"}}
	// A fala começa em "Vamos" (2,2s), não na marcação quebrada antes dela
	want := []Utterance{{Speaker: "Pessoa 1", Text: "Vamos lá.", Start: 2.2}}
	if got := groupBySpeaker(segs, speakers, true); !reflect.DeepEqual(got, want) {
		t.Fatalf("esperava %+v, veio %+v", want, got)
	}
}

func TestGroupBySpeakerDropsRepeatedSentences(t *testing.T) {
	// O Whisper em loop repete a mesma frase; respostas curtas podem se repetir de verdade
	segs := []transcriber.Segment{
		{Start: 0, End: 3, Text: " Et aussi, l'increase de la performance de la personne."},
		{Start: 3, End: 6, Text: " Et aussi, l'increase de la performance de la personne."},
		{Start: 6, End: 7, Text: " Sim."},
		{Start: 7, End: 8, Text: " Sim."},
		{Start: 8, End: 11, Text: " et aussi l'increase de la performance de la personne"},
	}
	speakers := []diarization.Segment{{Start: 0, End: 11, Speaker: "Pessoa 1"}}
	want := []Utterance{{Speaker: "Pessoa 1", Text: "Et aussi, l'increase de la performance de la personne. Sim. Sim."}}
	if got := groupBySpeaker(segs, speakers, true); !reflect.DeepEqual(got, want) {
		t.Fatalf("esperava %+v, veio %+v", want, got)
	}
}
