package backend

import (
	"fmt"
	"log"
	"slices"
	"strings"

	"recorder/internal/speakers"
	"recorder/internal/storage"
)

// Os workspaces separam as reuniões (ex: um por emprego). Cada um tem as suas
// reuniões, as suas vozes conhecidas e o seu glossário; a busca do chat em
// "todas as reuniões" fica dentro dele. O modelo do Ollama e o áudio do
// computador valem para o app todo.

// GetWorkspaces lista os workspaces na ordem em que foram criados.
func (a *App) GetWorkspaces() ([]storage.Workspace, error) {
	return a.store.GetWorkspaces()
}

// GetCurrentWorkspace devolve o workspace aberto por último na tela.
func (a *App) GetCurrentWorkspace() string {
	return a.store.CurrentWorkspaceID()
}

// SetCurrentWorkspace guarda o workspace aberto na tela: o app volta nele da
// próxima vez, e uma gravação recuperada cuja sessão foi apagada vai para ele.
func (a *App) SetCurrentWorkspace(workspaceID string) error {
	return a.store.SetCurrentWorkspace(workspaceID)
}

// CreateWorkspace cria um workspace vazio.
func (a *App) CreateWorkspace(name string) (*storage.Workspace, error) {
	name, err := a.workspaceName("", name)
	if err != nil {
		return nil, err
	}
	return a.store.CreateWorkspace(name)
}

func (a *App) RenameWorkspace(workspaceID, name string) error {
	name, err := a.workspaceName(workspaceID, name)
	if err != nil {
		return err
	}
	return a.store.RenameWorkspace(workspaceID, name)
}

// DeleteWorkspace apaga o workspace com as suas reuniões (áudio, transcrição,
// resumo e chat), as vozes conhecidas e o glossário. Sempre fica pelo menos um.
func (a *App) DeleteWorkspace(workspaceID string) error {
	workspaces, err := a.store.GetWorkspaces()
	if err != nil {
		return err
	}
	if len(workspaces) <= 1 {
		return fmt.Errorf("é preciso ter pelo menos um workspace")
	}
	ids, err := a.store.WorkspaceSessionIDs(workspaceID)
	if err != nil {
		return err
	}
	if a.recordingIn(ids) {
		return fmt.Errorf("pare a gravação antes de apagar o workspace")
	}
	log.Printf("Apagando o workspace %s com %d reuniões, a pedido do usuário", workspaceID, len(ids))
	for _, id := range ids {
		if err := a.DeleteSession(id); err != nil {
			return err
		}
	}
	return a.store.DeleteWorkspace(workspaceID)
}

// MoveSession passa a sessão para outro workspace. As pessoas nomeadas nela
// passam a ser reconhecidas também nas reuniões do workspace de destino.
func (a *App) MoveSession(sessionID, workspaceID string) error {
	if a.recordingIn([]string{sessionID}) {
		return fmt.Errorf("pare a gravação antes de mover a reunião")
	}
	return speakers.Move(a.store, sessionID, workspaceID)
}

// recordingIn diz se está gravando numa das sessões.
func (a *App) recordingIn(sessionIDs []string) bool {
	recording, sessionID, _ := a.recorder().Status()
	return recording && slices.Contains(sessionIDs, sessionID)
}

// workspaceName confere o nome de um workspace novo ou renomeado: não pode ser
// vazio nem igual ao de outro, sem diferenciar maiúsculas.
func (a *App) workspaceName(workspaceID, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("dê um nome ao workspace")
	}
	workspaces, err := a.store.GetWorkspaces()
	if err != nil {
		return "", err
	}
	for _, ws := range workspaces {
		if ws.ID != workspaceID && strings.EqualFold(ws.Name, name) {
			return "", fmt.Errorf("já existe um workspace chamado %q", ws.Name)
		}
	}
	return name, nil
}
