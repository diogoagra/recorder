package backend

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// A versão mostrada no "Sobre" é a do pacote: o wails.json (de onde saem a
// versão do .app e a do .exe) e a constante Version mudam juntos.
func TestVersionMatchesThePackage(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Version) {
		t.Fatalf("versão fora do formato N.N.N: %q", Version)
	}
	data, err := os.ReadFile("../../wails.json")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Info struct {
			ProductVersion string `json:"productVersion"`
		} `json:"info"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Info.ProductVersion != Version {
		t.Fatalf("o wails.json diz %q e o app, %q: mude os dois", config.Info.ProductVersion, Version)
	}
	if got := (&App{}).GetVersion(); got != Version {
		t.Fatalf("GetVersion() = %q", got)
	}
}
