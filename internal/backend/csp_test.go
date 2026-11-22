package backend

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// A política de conteúdo da janela só deixa rodar os arquivos .js do app. A
// interface não pode depender do que ela proíbe: um <script> com código dentro
// ou um onclick no HTML deixariam de funcionar (e o botão, de responder) sem
// erro nenhum na compilação.
func TestFrontendFitsThePageCSP(t *testing.T) {
	page, err := os.ReadFile("../../frontend/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, tag := range regexp.MustCompile(`(?i)<script[^>]*>`).FindAllString(html, -1) {
		src := regexp.MustCompile(`(?i)\ssrc="([^"]*)"`).FindStringSubmatch(tag)
		if src == nil {
			t.Errorf("script escrito no HTML (a política da janela não o deixa rodar): %s", tag)
		} else if strings.Contains(src[1], ":") || strings.HasPrefix(src[1], "//") {
			t.Errorf("script de fora do app: %s", tag)
		}
	}
	if found := regexp.MustCompile(`(?i)\son[a-z]+\s*=\s*["']`).FindAllString(html, -1); len(found) > 0 {
		t.Errorf("atributos de evento no HTML (a política da janela não os deixa rodar): %q", found)
	}
	if found := regexp.MustCompile(`(?i)(href|src)\s*=\s*["']\s*(javascript:|https?://|//)[^"']*`).FindAllString(html, -1); len(found) > 0 {
		for _, f := range found {
			// Links da tela "Sobre": abrem no navegador do sistema, pelo app.js
			if !strings.Contains(f, "href=") {
				t.Errorf("recurso de fora do app no HTML: %s", f)
			}
		}
	}

	for _, file := range []string{"../../frontend/app.js", "../../frontend/theme.js"} {
		code, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if found := regexp.MustCompile(`\beval\s*\(|new\s+Function\s*\(|set(Timeout|Interval)\s*\(\s*["'`+"`"+`]`).FindAllString(string(code), -1); len(found) > 0 {
			t.Errorf("%s: código montado a partir de texto (a política da janela não o deixa rodar): %q", file, found)
		}
	}
}

func TestPageCSPGoesWithTheInterface(t *testing.T) {
	served := withPageCSP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest("GET", "/index.html", nil))
	policy := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self';", "connect-src 'self'", "form-action 'none'"} {
		if !strings.Contains(policy, want) {
			t.Errorf("a política %q deveria ter %q", policy, want)
		}
	}
	// Nada de script embutido, de eval nem de endereços da internet
	for _, bad := range []string{"script-src 'self' 'unsafe", "unsafe-eval", "https:", " *;", "http: "} {
		if strings.Contains(policy, bad) {
			t.Errorf("a política %q não deveria ter %q", policy, bad)
		}
	}
	if rec.Body.String() != "ok" {
		t.Errorf("a resposta deveria passar adiante, veio %q", rec.Body.String())
	}
}
