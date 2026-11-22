// Package datadir diz onde ficam os dados do Recorder: o banco, o áudio das
// sessões, as capturas de tela, os modelos e a voz do Intérprete.
package datadir

import (
	"os"
	"path/filepath"
)

// EnvVar troca a pasta de dados, que por padrão é ~/.recorder. Serve para
// rodar o app (ou o `wails generate`, que executa o app para gerar os
// bindings) sem tocar nas reuniões de verdade:
//
//	RECORDER_HOME=~/.recorder-teste wails dev
const EnvVar = "RECORDER_HOME"

// Dir é a pasta de dados: a de EnvVar, se definida, ou ~/.recorder.
func Dir() string {
	if dir := os.Getenv(EnvVar); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".recorder")
}

// Ensure cria a pasta de dados, se faltar, e a deixa acessível só ao dono
// (0700). Ali ficam o áudio e o texto das reuniões, o log e a amostra da voz;
// com a permissão padrão, num computador com mais de uma conta os outros
// usuários conseguiam ler tudo. Fechada a pasta, o que está dentro dela fica
// fora de alcance, seja qual for a permissão de cada arquivo.
func Ensure() error {
	dir := Dir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.Chmod(dir, 0700)
}

// Path devolve um caminho dentro da pasta de dados.
func Path(elem ...string) string {
	return filepath.Join(append([]string{Dir()}, elem...)...)
}

// DB é o banco com as sessões, as transcrições e as configurações.
func DB() string { return Path("transcripts.db") }

// SessionsAudio é a pasta com o áudio de cada sessão.
func SessionsAudio() string { return Path("sessions_audio") }

// TmpAudio é a pasta do que está sendo gravado: o áudio bruto da gravação em
// andamento e os blocos esperando transcrição.
func TmpAudio() string { return Path("tmp_audio") }

// Models é a pasta dos modelos de identificação de locutores.
func Models() string { return Path("models") }

// Screenshots é a pasta das capturas de tela, com uma pasta por sessão.
func Screenshots() string { return Path("screenshots") }

// Voice é a pasta da voz do Intérprete.
func Voice() string { return Path("voz") }
