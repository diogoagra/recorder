package diarization

import "testing"

func voice(seconds float64, emb ...float32) Voice {
	return Voice{Embedding: emb, Seconds: seconds}
}

func TestRegistryKeepsSpeakersAcrossBlocks(t *testing.T) {
	var r Registry
	got := r.MatchBlock([]Voice{voice(5, 1, 0, 0), voice(5, 0, 1, 0)})
	if got[0] != "Pessoa 1" || got[1] != "Pessoa 2" {
		t.Fatalf("primeiro bloco: esperava Pessoa 1 e 2, veio %q", got)
	}
	// Bloco seguinte: as mesmas vozes com um pouco de variação, em outra ordem
	got = r.MatchBlock([]Voice{voice(5, 0.1, 0.9, 0.1), voice(5, 0.9, 0.2, 0.1)})
	if got[0] != "Pessoa 2" || got[1] != "Pessoa 1" {
		t.Fatalf("segundo bloco: esperava Pessoa 2 e 1, veio %q", got)
	}
	if len(r.Speakers) != 2 {
		t.Fatalf("esperava 2 pessoas, veio %d", len(r.Speakers))
	}
}

func TestRegistryKeepsDifferentVoicesOfTheSameBlockApart(t *testing.T) {
	var r Registry
	r.MatchBlock([]Voice{voice(5, 1, 0, 0)})

	// As duas vozes passam do limiar para a Pessoa 1, mas são longas e nada
	// parecidas entre si: a mais parecida fica com a Pessoa 1 e a outra, que
	// não é claramente dela, vira uma pessoa nova
	got := r.MatchBlock([]Voice{voice(5, 0.45, 0, 0.89), voice(5, 0.9, 0, -0.44)})
	if got[0] != "Pessoa 2" || got[1] != "Pessoa 1" || len(r.Merged) != 0 {
		t.Fatalf("esperava Pessoa 2 e Pessoa 1, separadas, veio %q (juntadas: %v)", got, r.Merged)
	}
}

func TestRegistryKeepsPersonSplitByTheBlockGroupingTogether(t *testing.T) {
	var r Registry
	r.MatchBlock([]Voice{voice(10, 1, 0, 0), voice(10, 0, 0, 1)})

	// O agrupamento separou a Pessoa 1 em várias vozes no mesmo bloco, uma
	// delas curta e pouco parecida: todas ficam com a Pessoa 1, sem criar
	// pessoas que não existem
	got := r.MatchBlock([]Voice{voice(5, 0.98, 0.2, 0), voice(5, 0.9, 0.44, 0), voice(2, 0.3, 0.9, 0)})
	if got[0] != "Pessoa 1" || got[1] != "Pessoa 1" || got[2] != "Pessoa 1" {
		t.Fatalf("esperava as três vozes na Pessoa 1, veio %q", got)
	}
	if len(r.Speakers) != 2 {
		t.Fatalf("esperava 2 pessoas, veio %+v", r.Speakers)
	}
}

func TestRegistryMergesSameSpeaker(t *testing.T) {
	r := Registry{Speakers: []Speaker{
		{Label: "Pessoa 1", Embedding: []float32{1, 0}, Seconds: 10},
		{Label: "Pessoa 2", Embedding: []float32{0, 1}, Seconds: 10},
		{Label: "Pessoa 3", Embedding: normalized([]float32{1, 0.3}), Seconds: 10},
	}}
	// Com mais áudio, a Pessoa 3 se revelou a Pessoa 1
	r.MatchBlock(nil)
	if len(r.Speakers) != 2 || len(r.Merged) != 1 || r.Merged[0] != (Merge{From: "Pessoa 3", To: "Pessoa 1"}) {
		t.Fatalf("esperava a Pessoa 3 juntada à Pessoa 1, veio %d vozes e %v", len(r.Speakers), r.Merged)
	}
	// Falas de um bloco anterior com o rótulo antigo passam para o novo
	if got := r.Resolve("Pessoa 3"); got != "Pessoa 1" {
		t.Fatalf("Resolve(Pessoa 3) = %q", got)
	}
	if got := r.Resolve("Pessoa 2"); got != "Pessoa 2" {
		t.Fatalf("Resolve(Pessoa 2) = %q", got)
	}
}

func TestRegistryMergesTooShortSpeaker(t *testing.T) {
	var r Registry
	r.MatchBlock([]Voice{voice(10, 1, 0, 0), voice(10, 0, 1, 0)})

	// As duas pessoas já falaram neste bloco e sobra um trecho curto, diferente
	// das duas: curto demais para ser outra pessoa
	got := r.MatchBlock([]Voice{voice(5, 1, 0, 0), voice(5, 0, 1, 0), voice(1, 0.3, 0, 1)})
	if got[2] != "Pessoa 1" || len(r.Speakers) != 2 {
		t.Fatalf("esperava o trecho curto na Pessoa 1 e 2 pessoas, veio %q e %d", got, len(r.Speakers))
	}
}

func TestRegistryConsolidate(t *testing.T) {
	r := Registry{Speakers: []Speaker{
		{Label: "Pessoa 1", Embedding: []float32{1, 0, 0}, Seconds: 300},
		{Label: "Pessoa 2", Embedding: []float32{0, 1, 0}, Seconds: 120},
		// Pouco áudio e pouco parecidas com todo mundo: ruído e fala sobreposta
		{Label: "Pessoa 3", Embedding: normalized([]float32{0.2, 0.3, 1}), Seconds: 9},
		{Label: "Pessoa 4", Embedding: normalized([]float32{0.4, 0.1, 1}), Seconds: 3},
		// Pouco áudio, mas com nome: o usuário disse quem é
		{Label: "Ana", Embedding: []float32{0, 0, 1}, Seconds: 5},
	}}
	r.Consolidate()
	if len(r.Speakers) != 3 || r.index("Ana") < 0 {
		t.Fatalf("esperava Pessoa 1, Pessoa 2 e Ana, veio %+v", r.Speakers)
	}
	// Cada uma vai para a mais parecida entre as que falaram bastante; a Ana conta
	if got := r.Resolve("Pessoa 3"); got != "Ana" {
		t.Fatalf("Resolve(Pessoa 3) = %q", got)
	}

	// Ninguém falou bastante: não há com quem juntar
	short := Registry{Speakers: []Speaker{
		{Label: "Pessoa 1", Embedding: []float32{1, 0}, Seconds: 8},
		{Label: "Pessoa 2", Embedding: []float32{0, 1}, Seconds: 6},
	}}
	short.Consolidate()
	if len(short.Speakers) != 2 || len(short.Merged) != 0 {
		t.Fatalf("esperava as 2 pessoas intactas, veio %+v", short.Speakers)
	}
}

func TestRegistryNeverMergesTwoNamedPeople(t *testing.T) {
	r := Registry{Speakers: []Speaker{
		{Label: "Ana", Embedding: []float32{1, 0}, Seconds: 10},
		{Label: "Bia", Embedding: normalized([]float32{1, 0.2}), Seconds: 10},
		{Label: "Pessoa 3", Embedding: normalized([]float32{1, 0.1}), Seconds: 10},
	}}
	r.MatchBlock(nil)
	// A Pessoa 3 é a Ana (ou a Bia), mas as duas com nome continuam separadas
	if len(r.Speakers) != 2 || r.index("Ana") < 0 || r.index("Bia") < 0 {
		t.Fatalf("esperava Ana e Bia, veio %+v", r.Speakers)
	}
}

func TestRegistryShortUnknownVoiceDoesNotCreateSpeaker(t *testing.T) {
	var r Registry
	r.MatchBlock([]Voice{voice(5, 1, 0, 0)})

	if got := r.MatchBlock([]Voice{voice(minNewSpeakerSeconds/2, 0, 0, 1)}); got[0] != "Pessoa 1" {
		t.Fatalf("trecho curto: esperava a voz conhecida mais parecida, veio %q", got)
	}
	if got := r.MatchBlock([]Voice{voice(minNewSpeakerSeconds, 0, 0, 1)}); got[0] != "Pessoa 2" {
		t.Fatalf("trecho longo: esperava Pessoa 2, veio %q", got)
	}
}

func TestRegistryWithoutEmbedding(t *testing.T) {
	var r Registry
	if got := r.MatchBlock([]Voice{{Seconds: 5}}); got[0] != "" || len(r.Speakers) != 0 {
		t.Fatalf("sem embedding não deveria identificar ninguém, veio %q", got)
	}
}

func TestSpeakerAt(t *testing.T) {
	segs := []Segment{
		{Start: 0, End: 4, Speaker: "Pessoa 1"},
		{Start: 4, End: 10, Speaker: "Pessoa 2"},
		{Start: 20, End: 25, Speaker: "Pessoa 1"},
	}
	cases := []struct {
		start, end float64
		want       string
	}{
		{0, 3, "Pessoa 1"},
		{3, 9, "Pessoa 2"},   // maior sobreposição
		{12, 14, "Pessoa 2"}, // fora de qualquer trecho: o mais próximo
		{18, 19, "Pessoa 1"},
	}
	for _, c := range cases {
		if got := SpeakerAt(segs, c.start, c.end); got != c.want {
			t.Errorf("SpeakerAt(%v, %v): esperava %q, veio %q", c.start, c.end, c.want, got)
		}
	}
	if got := SpeakerAt(nil, 0, 1); got != "" {
		t.Errorf("sem trechos: esperava vazio, veio %q", got)
	}
}

func TestRegistryRecognizesKnownPeople(t *testing.T) {
	r := Registry{People: []Speaker{
		{Label: "Ana", Embedding: []float32{1, 0, 0}},
		{Label: "João", Embedding: []float32{0, 1, 0}},
	}}
	// Com pouco áudio ainda não dá para dizer quem é
	got := r.MatchBlock([]Voice{voice(5, 0.95, 0.1, 0), voice(5, 0, 0, 1)})
	if got[0] != "Pessoa 1" || got[1] != "Pessoa 2" {
		t.Fatalf("esperava Pessoa 1 e Pessoa 2, veio %q", got)
	}
	// Com o áudio acumulado de mais um bloco, a Pessoa 1 é a Ana: as falas já
	// gravadas com o rótulo antigo mudam junto
	if got := r.MatchBlock([]Voice{voice(6, 0.9, 0.2, 0)}); got[0] != "Ana" {
		t.Fatalf("esperava Ana, veio %q", got)
	}
	if got := r.Resolve("Pessoa 1"); got != "Ana" {
		t.Fatalf("Resolve(Pessoa 1) = %q", got)
	}
	// Uma voz longa já chega com o nome
	if got := r.MatchBlock([]Voice{voice(12, 0.1, 0.99, 0)}); got[0] != "João" {
		t.Fatalf("esperava João, veio %q", got)
	}
}

// Em 18/11/2026 a reunião começou com um trecho de duas pessoas misturadas,
// reconhecido como o Diogo; a voz seguinte, de outra pessoa, parecia com a
// média da sessão e passou a reunião inteira com o nome dele.
func TestRegistryKeepsRecognizedNameForThePersonsVoice(t *testing.T) {
	ana := []float32{1, 0, 0}
	r := Registry{People: []Speaker{{Label: "Ana", Embedding: ana, Seconds: 300}}}
	// O começo mistura a Ana com outra pessoa, mas ainda é mais Ana
	mixed := normalized([]float32{0.8, 0.6, 0})
	if got := r.MatchBlock([]Voice{voice(12, mixed...)}); got[0] != "Ana" {
		t.Fatalf("esperava Ana, veio %q", got)
	}
	// A outra pessoa parece com a média da sessão (0.87), mas não com a voz
	// guardada da Ana (0.4): é uma pessoa nova
	other := normalized([]float32{0.4, 0.92, 0})
	if got := r.MatchBlock([]Voice{voice(12, other...)}); got[0] != "Pessoa 2" {
		t.Fatalf("esperava Pessoa 2, veio %q", got)
	}
	// E as duas não viram uma só quando ela acumula mais áudio
	r.MatchBlock([]Voice{voice(30, other...)})
	if len(r.Speakers) != 2 || len(r.Merged) != 0 {
		t.Fatalf("esperava Ana e Pessoa 2 separadas, veio %+v (juntadas: %v)", r.Speakers, r.Merged)
	}
	// A Ana de verdade continua sendo a Ana
	if got := r.MatchBlock([]Voice{voice(8, 0.97, 0.24, 0)}); got[0] != "Ana" {
		t.Fatalf("esperava Ana, veio %q", got)
	}
}

func TestRegistryBorderlineVoiceDoesNotMoveTheAverage(t *testing.T) {
	var r Registry
	r.MatchBlock([]Voice{voice(10, 1, 0, 0)})
	// Parecida o bastante para ficar com a Pessoa 1 (0.55), mas não para
	// entrar na média dela
	got := r.MatchBlock([]Voice{voice(10, normalized([]float32{0.55, 0.835, 0})...)})
	if got[0] != "Pessoa 1" {
		t.Fatalf("esperava Pessoa 1, veio %q", got)
	}
	if e := r.Speakers[0].Embedding; e[0] < 0.999 || r.Speakers[0].Seconds != 10 {
		t.Fatalf("a média da Pessoa 1 mudou: %v (%.0fs)", e, r.Speakers[0].Seconds)
	}
}

func TestRegistryDoesNotGuessAmbiguousPeople(t *testing.T) {
	// A voz é parecida com as duas pessoas na mesma medida
	r := Registry{People: []Speaker{
		{Label: "Ana", Embedding: normalized([]float32{1, 1, 0})},
		{Label: "Bia", Embedding: normalized([]float32{1, 0.95, 0.1})},
	}}
	if got := r.MatchBlock([]Voice{voice(15, 1, 0.97, 0.05)}); got[0] != "Pessoa 1" {
		t.Fatalf("sem uma pessoa claramente mais parecida, esperava Pessoa 1, veio %q", got)
	}
	// Trecho curto demais para reconhecer alguém
	r = Registry{People: []Speaker{{Label: "Ana", Embedding: []float32{1, 0, 0}}}}
	if got := r.MatchBlock([]Voice{voice(5, 1, 0, 0)}); got[0] != "Pessoa 1" {
		t.Fatalf("trecho curto: esperava Pessoa 1, veio %q", got)
	}
}

func TestRegistryRenameAndMerge(t *testing.T) {
	var r Registry
	r.MatchBlock([]Voice{voice(5, 1, 0, 0), voice(5, 0, 1, 0), voice(5, 0, 0, 1)})

	if v, ok := r.Rename("Pessoa 2", "Ana"); !ok || v.Label != "Ana" {
		t.Fatalf("renomear: veio %+v %v", v, ok)
	}
	// A Pessoa 3 era a Ana também: as duas vozes viram uma só
	if _, ok := r.Rename("Pessoa 3", "Ana"); !ok || len(r.Speakers) != 2 {
		t.Fatalf("juntar: esperava 2 vozes, veio %d", len(r.Speakers))
	}
	// A próxima voz nova não pode repetir um rótulo em uso
	if got := r.MatchBlock([]Voice{voice(5, -1, 0, 0)}); got[0] != "Pessoa 3" {
		t.Fatalf("esperava Pessoa 3 para a voz nova, veio %q", got[0])
	}
	collide := Registry{Speakers: []Speaker{{Label: "Pessoa 2", Embedding: []float32{1, 0}}, {Label: "Pessoa 3", Embedding: []float32{0, 1}}}}
	if got := collide.MatchBlock([]Voice{voice(5, -1, -1)}); got[0] != "Pessoa 4" {
		t.Fatalf("esperava Pessoa 4 (a 3 já existe), veio %q", got[0])
	}
	if _, ok := r.Rename("Pessoa 9", "Bia"); ok {
		t.Fatal("renomear uma voz que não existe deveria falhar")
	}
}

func TestIsAutoLabel(t *testing.T) {
	for label, want := range map[string]bool{"Pessoa 1": true, "Pessoa 12": true, "Ana": false, "Pessoa": false, "Pessoa 1 Ana": false} {
		if got := IsAutoLabel(label); got != want {
			t.Errorf("IsAutoLabel(%q) = %v", label, got)
		}
	}
}

func TestCombine(t *testing.T) {
	if _, ok := Combine(nil); ok {
		t.Fatal("sem vozes não há o que combinar")
	}
	// Uma reunião longa pesa no máximo 300s: a média fica entre as duas
	got, ok := Combine([]Speaker{
		{Embedding: []float32{1, 0}, Seconds: 5000},
		{Embedding: normalized([]float32{0.8, 0.6}), Seconds: 300},
	})
	if !ok || got.Seconds != 300 {
		t.Fatalf("veio %+v %v", got, ok)
	}
	if d := dot(got.Embedding, normalized([]float32{1.8, 0.6})); d < 0.999 {
		t.Fatalf("esperava a média das duas vozes, veio %v", got.Embedding)
	}

	// O nome foi dado à voz de outra pessoa numa reunião: ela fica de fora,
	// mesmo sendo a mais longa
	got, _ = Combine([]Speaker{
		{Embedding: []float32{1, 0, 0}, Seconds: 60},
		{Embedding: []float32{0, 1, 0}, Seconds: 2000},
		{Embedding: normalized([]float32{0.9, 0.1, 0.3}), Seconds: 200},
		{Embedding: normalized([]float32{0.95, 0, 0.2}), Seconds: 100},
	})
	if got.Embedding[1] > 0.1 || got.Embedding[0] < 0.9 {
		t.Fatalf("a voz errada entrou na média: %v", got.Embedding)
	}

	// Só duas, e diferentes: fica a que tem mais áudio
	got, _ = Combine([]Speaker{{Embedding: []float32{1, 0}, Seconds: 20}, {Embedding: []float32{0, 1}, Seconds: 90}})
	if got.Embedding[1] < 0.999 {
		t.Fatalf("esperava a voz com mais áudio, veio %v", got.Embedding)
	}
}
