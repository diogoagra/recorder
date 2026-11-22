// O Recorder grava reuniões, transcreve, identifica quem fala e escreve
// resumos, tarefas e respostas de chat. Este arquivo é só a entrada: embute a
// interface no executável e escolhe o modo. O aplicativo está em
// internal/backend.
//
//	recorder                    abre o app
//	recorder daemon             grava e transcreve sem janela, até ser encerrado
//	recorder query "pergunta"   responde com as falas mais recentes
package main

import (
	"embed"
	"fmt"
	"os"
	"strings"

	"recorder/internal/backend"
)

//go:embed all:frontend
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

func main() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "daemon":
			backend.RunDaemon()
			return
		case "query":
			if len(os.Args) < 3 {
				fmt.Println("Uso: recorder query \"sua pergunta\"")
				os.Exit(1)
			}
			backend.RunQuery(strings.Join(os.Args[2:], " "))
			return
		}
	}
	backend.RunWindow(assets, appIcon)
}
