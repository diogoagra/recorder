package interpreter

import "testing"

// Com dois BlackHole instalados, a tela e o roteador precisam escolher o
// mesmo: o 2ch, que é o que as instruções mandam escolher no Meet.
func TestChooseVirtualMic(t *testing.T) {
	cases := []struct {
		names []string
		want  int
	}{
		{[]string{"MacBook Pro Speakers", "BlackHole 2ch"}, 1},
		{[]string{"BlackHole 16ch", "MacBook Pro Speakers", "BlackHole 2ch"}, 2},
		{[]string{"BlackHole 2ch", "BlackHole 16ch"}, 0},
		{[]string{"BlackHole 16ch", "BlackHole 64ch"}, 0}, // sem o 2ch, o primeiro
		{[]string{"MacBook Pro Speakers", "AirPods"}, -1},
		{nil, -1},
	}
	for _, c := range cases {
		if got := chooseVirtualMic(c.names); got != c.want {
			t.Errorf("chooseVirtualMic(%q) = %d, quero %d", c.names, got, c.want)
		}
	}
}

// Com o BlackHole como saída de som padrão, o inglês não vai também para
// ela: entraria duas vezes na reunião.
func TestMonitorAllowed(t *testing.T) {
	if !monitorAllowed("MacBook Pro Speakers") || !monitorAllowed("") {
		t.Fatal("o fone ou o alto-falante recebem o inglês")
	}
	if monitorAllowed("BlackHole 2ch") || monitorAllowed("BlackHole 16ch") {
		t.Fatal("o BlackHole como saída padrão não recebe o inglês de novo")
	}
}
