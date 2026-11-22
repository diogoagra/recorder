package backend

import "sync"

// O aviso de trabalho em segundo plano que a tela mostra ("Gerando resumo da
// sessão...", "Transcrevendo o áudio importado (37%)"). Mais de um trabalho
// pode correr ao mesmo tempo (um áudio importado sendo transcrito enquanto
// outra reunião é resumida): a tela mostra o mais recente, e o aviso só some
// quando todos terminam. Antes era um liga/desliga só, e o fim de um trabalho
// apagava o aviso de outro ainda em andamento.

type statusJobs struct {
	mu   sync.Mutex
	next int
	jobs []*statusJob // em ordem de início
	// Aviso do assistente fora de um trabalho (ex: a busca do chat preparando
	// o índice); termina quando o assistente manda ""
	loose *statusJob
}

type statusJob struct {
	app       *App
	id        int
	sessionID string
	message   string
}

// beginJob começa um trabalho da sessão ("" se não for de uma) e o mostra na
// tela. Quem chama precisa chamar end no fim.
func (a *App) beginJob(sessionID, message string) *statusJob {
	a.jobs.mu.Lock()
	a.jobs.next++
	job := &statusJob{app: a, id: a.jobs.next, sessionID: sessionID, message: message}
	a.jobs.jobs = append(a.jobs.jobs, job)
	a.jobs.mu.Unlock()
	a.emitStatus()
	return job
}

// update troca o que a tela diz do trabalho (ex: o andamento).
func (j *statusJob) update(message string) {
	j.app.jobs.mu.Lock()
	j.message = message
	j.app.jobs.mu.Unlock()
	j.app.emitStatus()
}

// end termina o trabalho. Se outro continua, a tela passa a mostrar ele.
func (j *statusJob) end() {
	jobs := &j.app.jobs
	jobs.mu.Lock()
	for i, other := range jobs.jobs {
		if other == j {
			jobs.jobs = append(jobs.jobs[:i], jobs.jobs[i+1:]...)
			break
		}
	}
	if jobs.loose == j {
		jobs.loose = nil
	}
	jobs.mu.Unlock()
	j.app.emitStatus()
}

// statusMessage troca o que a tela diz do trabalho mais recente (ex: uma
// etapa dele). Sem trabalho em andamento, não faz nada.
func (a *App) statusMessage(message string) {
	a.jobs.mu.Lock()
	n := len(a.jobs.jobs)
	if n > 0 {
		a.jobs.jobs[n-1].message = message
	}
	a.jobs.mu.Unlock()
	if n > 0 {
		a.emitStatus()
	}
}

// assistantStatus recebe o que o assistente está fazendo ("" quando termina).
// Dentro de um trabalho (o resumo), é uma etapa dele; fora (a busca do chat),
// vira um aviso próprio até o assistente terminar.
func (a *App) assistantStatus(message string) {
	a.jobs.mu.Lock()
	loose, n := a.jobs.loose, len(a.jobs.jobs)
	a.jobs.mu.Unlock()

	switch {
	case message == "":
		if loose != nil {
			loose.end()
		}
	case loose != nil:
		loose.update(message)
	case n > 0:
		a.statusMessage(message)
	default:
		job := a.beginJob("", message)
		a.jobs.mu.Lock()
		a.jobs.loose = job
		a.jobs.mu.Unlock()
	}
}

// currentStatus devolve o aviso que a tela deve mostrar agora: o do trabalho
// mais recente, ou working false se não há nenhum.
func (a *App) currentStatus() (working bool, message, sessionID string) {
	a.jobs.mu.Lock()
	defer a.jobs.mu.Unlock()
	if n := len(a.jobs.jobs); n > 0 {
		job := a.jobs.jobs[n-1]
		return true, job.message, job.sessionID
	}
	return false, "", ""
}

func (a *App) emitStatus() {
	working, message, sessionID := a.currentStatus()
	a.emit("ollama-status", map[string]interface{}{
		"working":    working,
		"message":    message,
		"session_id": sessionID,
	})
}
