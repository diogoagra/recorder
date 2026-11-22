package backend

import "net/http"

// pageCSP é a política de conteúdo da janela do app. A tela mostra texto que
// vem de fora (o que foi dito na reunião e o que a IA escreveu a partir disso)
// e o trata como texto; o Markdown da IA passa por um filtro de HTML
// (renderMarkdown, no app.js). Esta política é a segunda barreira, para o caso
// de um erro nesse tratamento. Mesmo que um trecho de HTML de fora chegasse à
// página, ele não teria como:
//
//   - executar código: só rodam os arquivos .js do próprio app (nada de script
//     escrito no HTML, de atributo onclick ou de eval);
//   - buscar nada na internet: imagens e áudio só vêm do próprio app e do
//     servidor local de arquivos (fileserver.go), e conexões, só com o app.
//     Era por aí que o texto de uma reunião poderia sair do computador;
//   - embutir outras páginas ou enviar formulários.
//
// Estilos escritos no HTML continuam valendo: o index.html os usa.
const pageCSP = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' http://127.0.0.1:*; media-src http://127.0.0.1:*; font-src 'self'; connect-src 'self'; " +
	"object-src 'none'; frame-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// withPageCSP põe a política de conteúdo nas respostas que entregam a
// interface à janela (vai como Middleware do servidor de arquivos do Wails).
func withPageCSP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", pageCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
