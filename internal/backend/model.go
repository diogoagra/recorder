package backend

import (
	"fmt"

	"recorder/internal/transcriber"
)

// O modelo do Whisper não vai dentro do pacote do app: na primeira execução
// ele é baixado para a pasta de dados (transcriber.FetchModel, ligado em
// run.go). Enquanto isso o app abre normalmente, para ler as reuniões que já
// existem, e só o que depende da transcrição espera: gravar, importar um
// áudio, reprocessar e o Intérprete.

// whisperModelStatus diz se o modelo está no disco ou em que ponto está o
// download. Sem um modelo acompanhado (nos testes), ele é dado como pronto.
func (a *App) whisperModelStatus() transcriber.ModelStatus {
	if a.model == nil {
		return transcriber.ModelStatus{State: transcriber.ModelReady}
	}
	return a.model.Status()
}

// requireWhisperModel devolve o erro que a tela mostra quando alguém pede
// uma transcrição com o modelo ainda sendo baixado.
func (a *App) requireWhisperModel() error {
	status := a.whisperModelStatus()
	if status.State == transcriber.ModelReady {
		return nil
	}
	return fmt.Errorf("o modelo de transcrição ainda está sendo baixado (%d%%); tente de novo quando o download terminar", status.Percent())
}

// whisperModelPath devolve o caminho do modelo, para subir um whisper-server.
func (a *App) whisperModelPath() (string, error) {
	if err := a.requireWhisperModel(); err != nil {
		return "", err
	}
	if a.model == nil {
		path, ok := transcriber.DefaultModel.Find("")
		if !ok {
			return "", fmt.Errorf("o modelo de transcrição (%s) não foi encontrado", transcriber.DefaultModel.File)
		}
		return path, nil
	}
	path, _ := a.model.Path()
	return path, nil
}

// broadcastWhisperModel avisa a tela do andamento do download do modelo.
func (a *App) broadcastWhisperModel(status transcriber.ModelStatus) {
	a.emit("whisper-model", status)
}
