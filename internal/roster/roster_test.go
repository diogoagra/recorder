package roster

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	got := Parse(`Oleksandr Petrenko (Alexander, Alex / Sashka)
  Caio   Ramos
Luiz Prado <luiz@empresa.com>; Diogo Agra, Antoni Kowalski
caio ramos
alguem@empresa.com

Dominik Nowak (Dominic)`)
	want := []Person{
		{Name: "Oleksandr Petrenko", Aliases: []string{"Alexander", "Alex", "Sashka"}},
		{Name: "Caio Ramos"},
		{Name: "Luiz Prado"},
		{Name: "Diogo Agra"},
		{Name: "Antoni Kowalski"},
		{Name: "Dominik Nowak", Aliases: []string{"Dominic"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("esperava %+v, veio %+v", want, got)
	}
	if got := Parse("  \n "); len(got) != 0 {
		t.Fatalf("lista vazia: veio %+v", got)
	}
	if got := Text(want[:2]); got != "Oleksandr Petrenko (Alexander, Alex, Sashka)\nCaio Ramos\n" {
		t.Fatalf("Text: %q", got)
	}
}

func TestSame(t *testing.T) {
	same := [][2]string{
		{"Antoni", "Anthony"}, {"Antoni", "Antony"}, {"Antoni Kowalski", "Anthony"},
		{"Oleksandr", "Alexander"}, {"Alex", "Alexander"}, {"Luiz", "Luis"},
		{"Dominik", "Dominic"}, {"Dominik", "Dominique"}, {"Caio", "Caiu"},
		{"Diogo", "Diego"}, {"João", "Joao"}, {"Caio Ramos", "caio"},
		{"Antoni Kowalski", "Antony Kowalski"},
	}
	for _, c := range same {
		if !Same(c[0], c[1]) || !Same(c[1], c[0]) {
			t.Errorf("%q e %q deveriam ser o mesmo nome", c[0], c[1])
		}
	}
	different := [][2]string{
		{"Mario", "Maria"}, {"Daniel", "Daniela"}, {"Pablo", "Pavel"},
		{"Vladimir", "Wladislav"}, {"Caio", "Diogo"}, {"Davi", "Dani"},
		{"Ana Paula", "Ana Maria"}, {"Luiz", "Luiza"}, {"", "Ana"}, {"Li", "Lia"},
	}
	for _, c := range different {
		if Same(c[0], c[1]) || Same(c[1], c[0]) {
			t.Errorf("%q e %q não deveriam ser o mesmo nome", c[0], c[1])
		}
	}
}

func TestFind(t *testing.T) {
	people := Parse("Oleksandr Petrenko (Alex, Sashka)\nAntoni Kowalski\nAna Paula\nAna Maria")
	for said, want := range map[string]string{
		"Alexander": "Oleksandr Petrenko", "Sashka": "Oleksandr Petrenko", "Anthony": "Antoni Kowalski",
		"Antoni Kowalski": "Antoni Kowalski", "Ana Maria": "Ana Maria",
	} {
		if got := Find(people, said); got == nil || got.Name != want {
			t.Errorf("Find(%q) = %v, esperava %s", said, got, want)
		}
	}
	// Ninguém, ou mais de uma pessoa
	for _, said := range []string{"Pablo", "Ana", ""} {
		if got := Find(people, said); got != nil {
			t.Errorf("Find(%q) = %v, esperava ninguém", said, got)
		}
		if got := Guess(people, said); got != nil {
			t.Errorf("Guess(%q) = %v, esperava ninguém", said, got)
		}
	}
	// Um apelido que só pode ser de uma pessoa da lista
	plain := Parse("Oleksandr Petrenko\nAntoni Kowalski")
	if got := Find(plain, "Alex"); got != nil {
		t.Errorf("Find(Alex) = %v, esperava ninguém", got)
	}
	if got := Guess(plain, "Alex"); got == nil || got.Name != "Oleksandr Petrenko" {
		t.Errorf("Guess(Alex) = %v, esperava o Oleksandr", got)
	}
}

func TestAlike(t *testing.T) {
	for _, c := range [][2]string{{"Alex", "Aleksandr"}, {"Alex", "Oleksandr Petrenko"}, {"Antoni", "Anthony"}, {"Fran", "Francisco"}} {
		if !Alike(c[0], c[1]) || !Alike(c[1], c[0]) {
			t.Errorf("%q e %q deveriam poder ser a mesma pessoa", c[0], c[1])
		}
	}
	for _, c := range [][2]string{{"Alex", "Alexandra"}, {"Caio", "Carlos"}, {"Dan", "Dominik"}, {"Antoni", "Alexander"}, {"", "Ana"}} {
		if Alike(c[0], c[1]) || Alike(c[1], c[0]) {
			t.Errorf("%q e %q não deveriam poder ser a mesma pessoa", c[0], c[1])
		}
	}
}
