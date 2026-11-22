// Package roster cuida da lista de participantes de um workspace (as pessoas
// que costumam estar nas reuniões) e de reconhecer o mesmo nome escrito de
// jeitos diferentes pela transcrição ("Antoni", "Anthony", "Antony").
package roster

import (
	"strings"
	"unicode"
)

// Person é alguém da lista de participantes.
type Person struct {
	Name    string   // como deve aparecer: "Oleksandr Petrenko"
	Aliases []string // outros jeitos de chamar: "Alexander", "Alex", "Sashka"
}

// Parse lê a lista escrita pelo usuário: uma pessoa por linha (ou separadas
// por vírgula ou ponto e vírgula), com os apelidos entre parênteses:
//
//	Oleksandr Petrenko (Alexander, Alex)
//	Caio Ramos
//
// Um e-mail colado junto ("Nome <a@b.c>") é ignorado.
func Parse(text string) []Person {
	var people []Person
	seen := map[string]bool{}
	for _, entry := range splitOutside(text, "\n;,") {
		name, aliases := entry, ""
		if open := strings.IndexByte(entry, '('); open >= 0 {
			name = entry[:open]
			aliases = strings.TrimSuffix(strings.TrimSpace(entry[open+1:]), ")")
		}
		if lt := strings.IndexByte(name, '<'); lt >= 0 {
			name = name[:lt]
		}
		name = strings.Join(strings.Fields(name), " ")
		if name == "" || strings.Contains(name, "@") || seen[fold(name)] {
			continue
		}
		seen[fold(name)] = true
		p := Person{Name: name}
		for _, alias := range strings.FieldsFunc(aliases, func(r rune) bool { return r == ',' || r == ';' || r == '/' }) {
			if alias = strings.Join(strings.Fields(alias), " "); alias != "" {
				p.Aliases = append(p.Aliases, alias)
			}
		}
		people = append(people, p)
	}
	return people
}

// splitOutside divide text nos separadores que estão fora de parênteses.
func splitOutside(text, separators string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range text {
		switch {
		case r == '(':
			depth++
		case r == ')' && depth > 0:
			depth--
		case r == '\n' || (depth == 0 && strings.ContainsRune(separators, r)):
			parts = append(parts, text[start:i])
			start = i + 1
			depth = 0
		}
	}
	return append(parts, text[start:])
}

// Names devolve os nomes da lista, sem os apelidos.
func Names(people []Person) []string {
	names := make([]string, len(people))
	for i, p := range people {
		names[i] = p.Name
	}
	return names
}

// Text escreve a lista para as instruções da IA, uma pessoa por linha, com os
// apelidos entre parênteses.
func Text(people []Person) string {
	var b strings.Builder
	for _, p := range people {
		b.WriteString(p.Name)
		if len(p.Aliases) > 0 {
			b.WriteString(" (" + strings.Join(p.Aliases, ", ") + ")")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Find devolve a pessoa da lista chamada de said na conversa ("Anthony" é o
// Antoni Kowalski): pelo nome completo, pelo primeiro nome ou por um apelido.
// Devolve nil se ninguém ou mais de uma pessoa servir.
func Find(people []Person, said string) *Person {
	var found *Person
	for i := range people {
		if !people[i].Is(said) {
			continue
		}
		if found != nil {
			return nil
		}
		found = &people[i]
	}
	return found
}

// Guess é como Find, mas, se ninguém da lista tiver esse nome, aceita a única
// pessoa de quem said pode ser apelido ("Alex" é o Oleksandr, se não houver
// outro nome parecido na lista).
func Guess(people []Person, said string) *Person {
	if found := Find(people, said); found != nil {
		return found
	}
	var guess *Person
	for i := range people {
		if !Alike(people[i].Name, said) {
			continue
		}
		if guess != nil {
			return nil
		}
		guess = &people[i]
	}
	return guess
}

// Is diz se said é um jeito de chamar a pessoa.
func (p Person) Is(said string) bool {
	if Same(p.Name, said) {
		return true
	}
	for _, alias := range p.Aliases {
		if Same(alias, said) {
			return true
		}
	}
	return false
}

// Same diz se a e b são o mesmo nome, talvez escrito de outro jeito pela
// transcrição ou com e sem sobrenome: "Antoni Kowalski", "Anthony" e "Antony"
// são o mesmo; "Mario" e "Maria" não. Um nome sozinho é comparado com o
// primeiro nome do outro; com sobrenome dos dois lados, o último também
// precisa bater.
func Same(a, b string) bool {
	x, y := strings.Fields(fold(a)), strings.Fields(fold(b))
	if len(x) == 0 || len(y) == 0 {
		return false
	}
	if !sameWord(x[0], y[0]) {
		return false
	}
	return len(x) == 1 || len(y) == 1 || sameWord(x[len(x)-1], y[len(y)-1])
}

// Alike diz se a e b podem ser a mesma pessoa: o mesmo nome (ver Same) ou um
// apelido que soa como o começo do nome ("Alex" e "Aleksandr"). É mais frouxo
// que Same ("André" e "Anderson" passam): serve para comparar os nomes dados
// a uma mesma voz, não para dizer que duas pessoas são uma só.
func Alike(a, b string) bool {
	if Same(a, b) {
		return true
	}
	x, y := strings.Fields(fold(a)), strings.Fields(fold(b))
	if len(x) == 0 || len(y) == 0 || strings.HasSuffix(x[0], "a") != strings.HasSuffix(y[0], "a") {
		return false
	}
	short, long := skeleton(x[0]), skeleton(y[0])
	if len(short) > len(long) {
		short, long = long, short
	}
	return len(short) >= 3 && strings.HasPrefix(long, short)
}

func sameWord(x, y string) bool {
	if x == y {
		return true
	}
	// "Mario" e "Maria", "Daniel" e "Daniela": pessoas diferentes
	if strings.HasSuffix(x, "a") != strings.HasSuffix(y, "a") {
		return false
	}
	short, long := x, y
	if len(short) > len(long) {
		short, long = long, short
	}
	// Apelido que é o começo do nome: "Alex" e "Alexander"
	if len(short) >= 3 && strings.HasPrefix(long, short) {
		return true
	}
	sx, sy := skeleton(x), skeleton(y)
	if sx != sy || sx == "" {
		return false
	}
	// Mesmas consoantes: "Anthony" e "Antoni", "Oleksandr" e "Alexander". Com
	// poucas consoantes ("Caio", "Luis") isso é pouco: só uma letra pode mudar
	return len(sx) >= 3 || distance(x, y) <= 1
}

// skeleton reduz o nome às consoantes, juntando as que soam igual.
func skeleton(word string) string {
	word = strings.NewReplacer("ph", "f", "ck", "k", "x", "ks", "q", "k", "c", "k", "z", "s", "w", "v", "y", "i").Replace(word)
	var b strings.Builder
	var last rune
	for _, r := range word {
		if strings.ContainsRune("aeiouh", r) || r == last {
			continue
		}
		b.WriteRune(r)
		last = r
	}
	return b.String()
}

// distance é quantas letras é preciso trocar, pôr ou tirar para ir de a até b.
func distance(a, b string) int {
	x, y := []rune(a), []rune(b)
	prev := make([]int, len(y)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(x); i++ {
		cur := make([]int, len(y)+1)
		cur[0] = i
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(y)]
}

var accents = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a", "å", "a", "ą", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e", "ę", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o", "ø", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ç", "c", "ć", "c", "č", "c", "ñ", "n", "ń", "n", "ł", "l",
	"ś", "s", "š", "s", "ß", "ss", "ź", "z", "ż", "z", "ž", "z", "ý", "y",
)

// fold deixa só as letras, minúsculas e sem acento, com um espaço entre as palavras.
func fold(text string) string {
	text = accents.Replace(strings.ToLower(text))
	return strings.Join(strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r)
	}), " ")
}
