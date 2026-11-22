package backend

// Version é a versão do app. É a mesma do pacote (productVersion, no
// wails.json da raiz), e um teste confere que as duas andam juntas. Aparece
// na tela "Sobre". As tags do repositório levam o mesmo número, sem prefixo.
const Version = "1.1.0"

// GetVersion devolve a versão do app para a tela "Sobre".
func (a *App) GetVersion() string {
	return Version
}
