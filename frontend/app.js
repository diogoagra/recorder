document.addEventListener('DOMContentLoaded', () => {
    const transcriptsContainer = document.getElementById('transcriptsContainer');
    const chatMessages = document.getElementById('chatMessages');
    const chatForm = document.getElementById('chatForm');
    const chatInput = document.getElementById('chatInput');
    const sendBtn = document.getElementById('sendBtn');
    
    const toggleRecordBtn = document.getElementById('toggleRecordBtn');
    const recordLabel = document.getElementById('recordLabel');
    const recordTimer = document.getElementById('recordTimer');
    const recordingStatusText = document.getElementById('recordingStatusText');
    const sessionList = document.getElementById('sessionList');
    const sessionSearch = document.getElementById('sessionSearch');
    const transcriptTab = document.getElementById('transcriptTab');
    const transcriptsEmpty = document.getElementById('transcriptsEmpty');
    const audioBar = document.getElementById('audioBar');
    const newSessionBtn = document.getElementById('newSessionBtn');
    const deleteSessionBtn = document.getElementById('deleteSessionBtn');
    const renameSessionBtn = document.getElementById('renameSessionBtn');
    const regenerateSummaryBtn = document.getElementById('regenerateSummaryBtn');
    const reprocessSessionBtn = document.getElementById('reprocessSessionBtn');
    const sessionAudioPlayer = document.getElementById('sessionAudioPlayer');

    const searchAllSessions = document.getElementById('searchAllSessions');
    const notesInput = document.getElementById('notesInput');
    const notesSaved = document.getElementById('notesSaved');
    const captureScreenBtn = document.getElementById('captureScreenBtn');
    const screenshotsSection = document.getElementById('screenshotsSection');
    const screenshotsList = document.getElementById('screenshotsList');

    // Ícone de um botão: os desenhos ficam no <svg> escondido do começo do index.html
    const svgIcon = (name) => `<svg class="ic" aria-hidden="true"><use href="#i-${name}"></use></svg>`;

    let currentSessionID = '';
    let settingsOpen = false; // a tela de configurações está aberta
    // Workspaces separam as reuniões (ex: um por emprego); a lista mostra as do atual
    let workspaces = [];
    let currentWorkspaceID = '';
    // Reunião aberta em cada workspace, para voltar nela ao trocar de volta
    const lastSessionByWorkspace = {};
    let isRecording = false;
    let recordingStartedAt = 0; // ms, para o cronômetro
    let recordTimerInterval = null;
    let pendingBlocks = 0; // trechos gravados esperando transcrição
    let importing = false; // lendo e convertendo um áudio importado
    let servicesReady = false;
    let initFailed = false; // a carga inicial falhou (o motivo fica num aviso na tela)
    let allSessions = [];
    // Reuniões que não estão na lista e já foram procuradas (ou foram apagadas
    // aqui): uma fala delas não faz recarregar a lista de novo
    const missingSessions = new Set();
    // Servidor local do app que entrega o áudio e as capturas (já com o segredo dele)
    let audioServerURL = '';
    let currentAudioParts = [];
    let currentAudioPartIndex = 0;

    // --- Avisos e erros ---
    // Uma falha do backend aparece num aviso que some sozinho, sem interromper
    // quem está no meio de uma reunião, e vai para o log do app (no app
    // empacotado não há console para olhar).
    const toasts = document.getElementById('toasts');

    function errorText(e) {
        if (e === undefined || e === null) return '';
        return e.message ? e.message : String(e);
    }

    // Erro que não precisa de aviso na tela: só o log
    function logError(message, e) {
        console.error(message, e);
        const detail = errorText(e);
        try {
            if (window.runtime && window.runtime.LogError) {
                window.runtime.LogError(detail ? `${message}: ${detail}` : message);
            }
        } catch (err) {
            // O log não pode derrubar quem veio avisar do erro
        }
    }

    // kind: 'error' ou 'info'. Com sticky, o aviso fica até ser fechado.
    function showToast(text, kind = 'error', sticky = false) {
        // O mesmo aviso de novo (ex: uma falha a cada evento) não empilha: só renova o tempo
        let toast = Array.from(toasts.children).find(t => t.dataset.text === text);
        if (!toast) {
            toast = document.createElement('div');
            toast.className = 'toast ' + kind;
            toast.dataset.text = text;
            toast.setAttribute('role', kind === 'error' ? 'alert' : 'status');
            const label = document.createElement('span');
            label.textContent = text;
            const close = document.createElement('button');
            close.type = 'button';
            close.className = 'toast-close';
            close.title = 'Fechar';
            close.textContent = '×';
            close.addEventListener('click', () => toast.remove());
            toast.append(label, close);
            toasts.append(toast);
        }
        clearTimeout(toast.hideTimer);
        if (!sticky) toast.hideTimer = setTimeout(() => toast.remove(), kind === 'error' ? 8000 : 5000);
    }

    // Erro que o usuário precisa ver: aviso na tela e log
    function reportError(message, e) {
        logError(message, e);
        const detail = errorText(e);
        showToast(detail ? `${message}: ${detail}` : message);
    }

    window.addEventListener('unhandledrejection', (event) => reportError('Erro inesperado', event.reason));
    window.addEventListener('error', (event) => logError('Erro inesperado', event.error || event.message));

    // --- Markdown da IA ---
    // O resumo e as respostas do chat vêm da IA em Markdown. O HTML gerado passa
    // sempre pelo DOMPurify: a IA pode devolver HTML de verdade (por engano, ou
    // induzida pelo que foi dito na reunião), e um script aqui alcançaria o app
    // inteiro. Além dos scripts, fica de fora o que busca conteúdo de fora, muda
    // o estilo da tela ou se passa por um controle do app.
    //
    // A lista é do que pode ficar, e não do que sai: só o que o Markdown gera
    // (títulos, listas, tabelas, links, ênfase, código). Com uma lista do que
    // sai, passava o que ninguém lembrou de proibir: um <table background="...">
    // fazia a janela buscar um endereço de fora ao mostrar o resumo, sem clique
    // (e o endereço podia levar o texto da reunião), e um <label for="..."> fazia
    // um clique no resumo acionar um botão do app.
    const markdownPolicy = {
        ALLOWED_TAGS: ['h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'p', 'br', 'hr', 'blockquote', 'pre', 'code',
            'strong', 'b', 'em', 'i', 'u', 's', 'del', 'sub', 'sup', 'ul', 'ol', 'li',
            'table', 'thead', 'tbody', 'tr', 'th', 'td', 'a', 'input'],
        ALLOWED_ATTR: ['href', 'title', 'align', 'start', 'colspan', 'rowspan', 'type', 'checked', 'disabled'],
        ALLOW_DATA_ATTR: false,
        ALLOW_ARIA_ATTR: false,
    };
    if (window.DOMPurify) {
        // Do Markdown só vem um <input>: a caixa de uma lista de tarefas ("- [ ] ...")
        DOMPurify.addHook('afterSanitizeAttributes', (node) => {
            if (node.nodeName !== 'INPUT') return;
            if (node.getAttribute('type') === 'checkbox') {
                node.setAttribute('disabled', '');
            } else {
                node.remove();
            }
        });
    }

    // Único lugar que transforma texto da IA em HTML. Sem as bibliotecas (ficam
    // em vendor/; não deveria acontecer), mostra o texto como veio.
    function renderMarkdown(el, text) {
        let html = null;
        if (window.marked && window.DOMPurify) {
            try {
                html = DOMPurify.sanitize(marked.parse(text || ''), markdownPolicy);
            } catch (e) {
                logError('Erro ao converter o Markdown', e);
            }
        }
        el.classList.toggle('plain-text', html === null);
        if (html === null) {
            el.textContent = text || '';
        } else {
            el.innerHTML = html;
        }
        linkTimes(el);
    }

    // Um link no resumo ou numa resposta do chat abre no navegador do sistema: o
    // webview trocaria a tela do app pelo site, sem ter como voltar
    function openMarkdownLink(e) {
        const link = e.target.closest ? e.target.closest('a[href]') : null;
        if (!link) return;
        e.preventDefault();
        // Só endereço completo da web ou de e-mail: um link relativo apontaria para dentro do app
        if (/^\s*(https?:\/\/|mailto:)/i.test(link.getAttribute('href')) && window.runtime && window.runtime.BrowserOpenURL) {
            window.runtime.BrowserOpenURL(link.href);
        }
    }
    [document.getElementById('sessionSummaryContent'), chatMessages].forEach(el => {
        el.addEventListener('click', openMarkdownLink);
        el.addEventListener('auxclick', openMarkdownLink);
    });

    function displaySessionSummary(summaryText) {
        const content = document.getElementById('sessionSummaryContent');
        const empty = !summaryText || summaryText.trim() === '';
        content.classList.toggle('empty', empty);
        if (empty) {
            content.textContent = 'O resumo aparece aqui quando a gravação for parada.';
        } else {
            renderMarkdown(content, summaryText);
        }
    }


    const audioVisualizer = document.getElementById('audioVisualizer');
    const visualizerCtx = audioVisualizer.getContext('2d');
    let audioHistory = new Array(60).fill(0.01);

    // startedAt: quando a gravação começou (ms), para o cronômetro
    function updateRecordingUI(recording, startedAt) {
        isRecording = recording;
        toggleRecordBtn.classList.toggle('recording', recording);
        recordLabel.textContent = recording ? 'Parar' : 'Gravar';
        audioVisualizer.style.display = recording ? 'block' : 'none';
        clearInterval(recordTimerInterval);
        recordTimer.textContent = '';
        if (recording) {
            recordingStartedAt = startedAt || Date.now();
            const tick = () => { recordTimer.textContent = formatOffset((Date.now() - recordingStartedAt) / 1000); };
            tick();
            recordTimerInterval = setInterval(tick, 1000);
        }
        updateRecordStatus();
        renderSessionList();
    }

    // Texto embaixo do botão: o que está acontecendo com a gravação e a transcrição
    function updateRecordStatus() {
        toggleRecordBtn.disabled = !servicesReady || !currentSessionID;
        let text;
        if (!servicesReady) {
            text = initFailed ? 'Não foi possível iniciar o Recorder' : 'Inicializando IA...';
        } else if (importing) {
            text = 'Importando áudio...';
        } else if (!currentSessionID) {
            text = 'Escolha ou crie uma reunião para gravar';
        } else if (isRecording) {
            text = pendingBlocks >= 2 ? `Gravando · ${pendingBlocks} trechos esperando transcrição` : 'Gravando e transcrevendo';
        } else if (pendingBlocks > 0) {
            text = pendingBlocks === 1 ? 'Transcrevendo o último trecho...' : `Transcrevendo... ${pendingBlocks} trechos na fila`;
        } else {
            text = 'Pronto para gravar';
        }
        recordingStatusText.textContent = text;
    }

    // Escuta o evento nativo emitido pelo Go com os níveis de volume
    if (window.runtime) {
        window.runtime.EventsOn("audio-level", (level) => {
            if (!isRecording) return;
            
            // Empurra o histórico (aplica suavização + boost)
            const boost = Math.min(level * 4, 1.0); // amplifica para ficar visível
            audioHistory.push(Math.max(boost, 0.02)); 
            audioHistory.shift();
            
            // Limpa o canvas
            const width = audioVisualizer.width;
            const height = audioVisualizer.height;
            visualizerCtx.clearRect(0, 0, width, height);
            
            // Desenha as barras
            const barWidth = width / audioHistory.length;
            const isDark = document.documentElement.dataset.theme === 'dark';
            visualizerCtx.fillStyle = isDark ? '#10b981' : '#059669'; // verde

            for (let i = 0; i < audioHistory.length; i++) {
                const val = audioHistory[i];
                const barHeight = Math.max(val * height, 2); 
                const x = i * barWidth;
                const y = (height - barHeight) / 2; // centralizado
                
                visualizerCtx.beginPath();
                visualizerCtx.roundRect(x, y, barWidth - 1, barHeight, 2);
                visualizerCtx.fill();
            }
        });
    }

    // Abas da sessão: Resumo | Transcrição | Chat | Notas
    const tabPanels = {
        summary: document.getElementById('summaryTab'),
        transcript: transcriptTab,
        chat: document.getElementById('chatTab'),
        notes: document.getElementById('notesTab'),
    };
    let currentTab = 'summary';

    // O botão de exportar salva a aba aberta
    const exportSessionBtn = document.getElementById('exportSessionBtn');
    const exportTitles = {
        summary: 'Exportar resumo para um arquivo',
        transcript: 'Exportar transcrição para um arquivo',
        chat: 'Exportar conversa do chat para um arquivo',
        notes: 'Exportar notas para um arquivo',
    };

    function showTab(name) {
        currentTab = name;
        exportSessionBtn.title = exportTitles[name];
        document.querySelectorAll('.tab').forEach(tab => {
            const active = tab.dataset.tab === name;
            tab.classList.toggle('active', active);
            tab.setAttribute('aria-selected', active);
        });
        Object.entries(tabPanels).forEach(([key, panel]) => { panel.hidden = key !== name; });
        if (name === 'chat') {
            chatMessages.scrollTop = chatMessages.scrollHeight;
            chatInput.focus();
        } else if (name === 'transcript' && isRecording) {
            transcriptTab.scrollTop = transcriptTab.scrollHeight;
        } else if (name === 'notes') {
            notesInput.focus();
        }
    }

    document.querySelectorAll('.tab').forEach(tab => {
        tab.addEventListener('click', () => showTab(tab.dataset.tab));
    });

    exportSessionBtn.addEventListener('click', async () => {
        if (!currentSessionID) return;
        try {
            // O que acabou de ser escrito nas notas entra no arquivo
            await flushNotes();
            await window.go.backend.App.ExportSession(currentSessionID, currentTab);
        } catch (e) {
            logError('Erro ao exportar', e);
            alert('Não foi possível exportar: ' + e);
        }
    });

    // Cmd/Ctrl+A fora de um campo de texto seleciona só o conteúdo da aba
    // aberta, e não a tela inteira, para copiar o resumo, a transcrição ou o chat
    const selectAllTargets = {
        summary: tabPanels.summary,
        transcript: transcriptsContainer,
        chat: chatMessages,
    };
    document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape' && settingsOpen && !document.querySelector('dialog[open]')) {
            closeSettings();
            return;
        }
        if (e.key.toLowerCase() !== 'a' || !(e.metaKey || e.ctrlKey) || e.shiftKey || e.altKey) return;
        if (!currentSessionID || document.querySelector('dialog[open]')) return;
        if (e.target.closest && e.target.closest('input, textarea, select, [contenteditable]')) return;
        e.preventDefault();
        if (currentTab === 'notes') {
            notesInput.focus();
            notesInput.select();
            return;
        }
        window.getSelection().selectAllChildren(selectAllTargets[currentTab]);
    });

    // Aba ao abrir uma sessão: a transcrição enquanto grava ou sem resumo ainda
    function defaultTab(session, transcriptCount) {
        if (isRecording) return 'transcript';
        if (session && session.summary && session.summary.trim() !== '') return 'summary';
        return transcriptCount > 0 ? 'transcript' : 'summary';
    }

    // Trabalho da IA ou da transcrição em andamento (evento "ollama-status").
    // Pode ser de outra reunião que não a aberta: aí o aviso diz de qual. Fora
    // da tela de uma reunião (boas-vindas, configurações), aparece na barra
    // lateral, embaixo do estado da gravação.
    let backgroundWork = { working: false, message: '', session_id: '' };

    function renderBackgroundWork() {
        let text = backgroundWork.message || 'Processando...';
        if (backgroundWork.session_id && backgroundWork.session_id !== currentSessionID) {
            const session = allSessions.find(s => s.id === backgroundWork.session_id);
            if (session) text = `${sessionTitle(session.name)}: ${text}`;
        }
        document.getElementById('ollamaStatusIndicator').style.display = backgroundWork.working ? 'flex' : 'none';
        document.getElementById('ollamaStatusText').innerText = text;
        const sidebarStatus = document.getElementById('backgroundStatus');
        sidebarStatus.hidden = !backgroundWork.working || (!!currentSessionID && !settingsOpen);
        sidebarStatus.textContent = text;
    }

    // O Intérprete só traduz com uma reunião aberta
    let interpreterSession = null;

    function updateViewMode() {
        if (interpreterSession !== currentSessionID) {
            interpreterSession = currentSessionID;
            window.go.backend.App.SetInterpreterSession(currentSessionID).catch(e => logError('Erro ao avisar o Intérprete da reunião aberta', e));
            // O atalho da captura de tela só é do Recorder com uma reunião aberta
            window.go.backend.App.SetCaptureShortcut(!!currentSessionID).catch(e => logError('Erro ao ligar o atalho da captura de tela', e));
            if (typeof renderInterpreter === 'function') renderInterpreter();
        }
        // O aviso de trabalho em andamento muda de lugar e de texto com a tela aberta
        renderBackgroundWork();
        const welcomeView = document.getElementById('welcomeView');
        const mainSessionView = document.getElementById('mainSessionView');
        document.getElementById('settingsView').hidden = !settingsOpen;
        // O botão da engrenagem fica apertado enquanto as configurações estão abertas
        document.getElementById('settingsBtn').classList.toggle('active', settingsOpen);
        if (settingsOpen) {
            welcomeView.style.display = 'none';
            mainSessionView.style.display = 'none';
        } else if (!currentSessionID) {
            welcomeView.style.display = 'flex';
            mainSessionView.style.display = 'none';
        } else {
            welcomeView.style.display = 'none';
            mainSessionView.style.display = 'flex';
        }
    }

    // Uma carga da lista por vez. Um pedido que chega durante uma carga não
    // dispara outra em paralelo (os eventos do backend pedem várias seguidas):
    // espera uma única carga seguinte, que já vê o que mudou depois que a atual
    // começou (ex: a reunião recém-criada).
    let sessionsLoad = null;
    let sessionsQueued = null;

    function loadSessions() {
        if (!sessionsLoad) {
            sessionsLoad = fetchSessions().finally(() => { sessionsLoad = null; });
            return sessionsLoad;
        }
        if (!sessionsQueued) {
            sessionsQueued = sessionsLoad.then(() => {
                sessionsQueued = null;
                return loadSessions();
            });
        }
        return sessionsQueued;
    }

    async function fetchSessions() {
        try {
            const sessions = await window.go.backend.App.GetSessions() || [];
            const shown = currentSession(); // a reunião aberta, como a tela a mostra agora
            allSessions = sessions;
            const selectedSession = currentSession();
            if (selectedSession && selectedSession.workspace_id === currentWorkspaceID) {
                // Só redesenha o resumo se ele mudou: um resumo sendo escrito, ou o
                // aviso de que está sendo refeito, continua na tela
                if (!shown || shown.summary !== selectedSession.summary) {
                    displaySessionSummary(selectedSession.summary);
                }
                // O áudio dela cresceu (ex: a gravação foi salva): o player acompanha
                if (shown && shown.duration !== selectedSession.duration) loadSessionAudio();
            } else if (!sessionsQueued) {
                // Com outra carga na fila, quem decide é ela: esta pode ter
                // começado antes de a reunião aberta existir
                currentSessionID = '';
                displaySessionSummary('');
            }
            renderSessionList();
            updateViewMode();
            updateRecordStatus();
        } catch (e) {
            reportError('Não foi possível carregar as reuniões', e);
        }
    }

    function currentSession() {
        return allSessions.find(s => s.id === currentSessionID);
    }

    // Reuniões do workspace aberto (allSessions tem as de todos)
    function workspaceSessions() {
        return allSessions.filter(s => s.workspace_id === currentWorkspaceID);
    }

    function currentWorkspace() {
        return workspaces.find(w => w.id === currentWorkspaceID);
    }

    // --- Workspaces ---
    const workspaceSelect = document.getElementById('workspaceSelect');
    const renameWorkspaceBtn = document.getElementById('renameWorkspaceBtn');
    const deleteWorkspaceBtn = document.getElementById('deleteWorkspaceBtn');
    const moveSessionBtn = document.getElementById('moveSessionBtn');
    const NEW_WORKSPACE = '__new__';

    async function loadWorkspaces() {
        try {
            workspaces = await window.go.backend.App.GetWorkspaces() || [];
            if (!workspaces.some(w => w.id === currentWorkspaceID)) {
                currentWorkspaceID = await window.go.backend.App.GetCurrentWorkspace();
            }
        } catch (e) {
            reportError('Não foi possível carregar os workspaces', e);
        }
        renderWorkspaces();
    }

    function renderWorkspaces() {
        workspaceSelect.innerHTML = '';
        workspaces.forEach(w => workspaceSelect.add(new Option(w.name, w.id, false, w.id === currentWorkspaceID)));
        workspaceSelect.add(new Option('＋ Novo workspace…', NEW_WORKSPACE));
        workspaceSelect.value = currentWorkspaceID;
        deleteWorkspaceBtn.disabled = workspaces.length <= 1;
        deleteWorkspaceBtn.title = workspaces.length <= 1 ? 'É preciso ter pelo menos um workspace' : 'Apagar workspace';
        // Mover só faz sentido com outro workspace para onde ir
        moveSessionBtn.style.display = workspaces.length > 1 ? '' : 'none';
    }

    // Trocar de workspace no meio de uma gravação ou importação esconderia a reunião
    function canSwitchWorkspace() {
        if (isRecording) {
            alert('Pare a gravação antes de trocar de workspace.');
            return false;
        }
        if (importing) {
            alert('Espere a importação do áudio terminar antes de trocar de workspace.');
            return false;
        }
        return true;
    }

    async function switchWorkspace(id) {
        if (id === currentWorkspaceID) return;
        lastSessionByWorkspace[currentWorkspaceID] = currentSessionID;
        currentWorkspaceID = id;
        window.go.backend.App.SetCurrentWorkspace(id).catch(e => logError('Erro ao salvar o workspace atual', e));
        const last = lastSessionByWorkspace[id];
        currentSessionID = workspaceSessions().some(s => s.id === last) ? last : '';
        sessionSearch.value = '';
        searchAllSessions.checked = false;
        renderWorkspaces();
        if (settingsOpen) loadWorkspaceSettings();
        await openSession();
    }

    workspaceSelect.addEventListener('change', async () => {
        const chosen = workspaceSelect.value;
        workspaceSelect.value = currentWorkspaceID; // só muda de verdade se der certo
        if (!canSwitchWorkspace()) return;
        if (chosen !== NEW_WORKSPACE) {
            await switchWorkspace(chosen);
            return;
        }
        const name = await showPrompt('Novo workspace', 'Nome do workspace (ex: o nome da empresa). Cada workspace tem as suas reuniões, vozes conhecidas e glossário.', '', false, 'Criar');
        if (!name) return;
        try {
            const ws = await window.go.backend.App.CreateWorkspace(name);
            await loadWorkspaces();
            await switchWorkspace(ws.id);
        } catch (e) {
            alert('Não foi possível criar o workspace: ' + e);
        }
    });

    renameWorkspaceBtn.addEventListener('click', async () => {
        const ws = currentWorkspace();
        if (!ws) return;
        const name = await showPrompt('Renomear workspace', 'Novo nome do workspace:', ws.name, false, 'Renomear');
        if (!name || name === ws.name) return;
        try {
            await window.go.backend.App.RenameWorkspace(ws.id, name);
            await loadWorkspaces();
            // As configurações abertas mostram o nome do workspace
            renderSettingsWorkspaceName();
        } catch (e) {
            alert('Não foi possível renomear o workspace: ' + e);
        }
    });

    deleteWorkspaceBtn.addEventListener('click', async () => {
        const ws = currentWorkspace();
        if (!ws || workspaces.length <= 1 || !canSwitchWorkspace()) return;
        const count = workspaceSessions().length;
        const meetings = count === 0 ? 'Ele não tem reuniões.'
            : `${count === 1 ? 'A reunião dele também será apagada' : `As ${count} reuniões dele também serão apagadas`}, com áudio, transcrição, resumo e chat. Para guardar alguma, mova-a antes para outro workspace.`;
        const ok = await showConfirm(`Apagar o workspace "${ws.name}"?`, `${meetings} Os participantes, as vozes conhecidas e o glossário dele também serão apagados.`);
        if (!ok) return;
        try {
            await window.go.backend.App.DeleteWorkspace(ws.id);
        } catch (e) {
            alert('Não foi possível apagar o workspace: ' + e);
            return;
        }
        delete lastSessionByWorkspace[ws.id];
        // O glossário e as notas que estavam sendo escritos eram do workspace
        // apagado: não há mais onde salvar
        if (settingsWorkspaceID === ws.id) {
            clearTimeout(glossaryTimer);
            glossaryDirty = false;
            clearTimeout(participantsTimer);
            participantsDirty = false;
        }
        const notesSession = allSessions.find(s => s.id === notesSessionID);
        if (notesSession && notesSession.workspace_id === ws.id) {
            clearTimeout(notesTimer);
            notesDirty = false;
        }
        currentWorkspaceID = '';
        currentSessionID = '';
        await loadWorkspaces();
        await loadSessions();
        const last = lastSessionByWorkspace[currentWorkspaceID];
        if (workspaceSessions().some(s => s.id === last)) currentSessionID = last;
        // As configurações abertas passam a mostrar o glossário e as vozes do workspace que ficou
        if (settingsOpen) loadWorkspaceSettings();
        await openSession();
    });

    // Mover a reunião aberta para outro workspace
    const moveDialog = document.getElementById('moveDialog');
    const moveWorkspaceSelect = document.getElementById('moveWorkspaceSelect');

    moveSessionBtn.addEventListener('click', () => {
        if (!currentSessionID) return;
        if (isRecording) {
            alert('Pare a gravação antes de mover a reunião.');
            return;
        }
        moveWorkspaceSelect.innerHTML = '';
        workspaces.filter(w => w.id !== currentWorkspaceID)
            .forEach(w => moveWorkspaceSelect.add(new Option(w.name, w.id)));
        moveDialog.returnValue = '';
        moveDialog.showModal();
    });

    moveDialog.addEventListener('close', async () => {
        if (moveDialog.returnValue !== 'confirm' || !currentSessionID) return;
        const sessionID = currentSessionID;
        const target = moveWorkspaceSelect.value;
        // Depois de mover, abre a reunião seguinte da lista, para seguir organizando
        const shown = visibleSessions();
        const index = shown.findIndex(s => s.id === sessionID);
        const next = shown[index + 1] || shown[index - 1];
        try {
            await window.go.backend.App.MoveSession(sessionID, target);
        } catch (e) {
            alert('Não foi possível mover a reunião: ' + e);
            return;
        }
        currentSessionID = next ? next.id : '';
        await loadSessions();
        await openSession();
    });

    // "08/10/2026 - Revisão de Sprint" -> "Revisão de Sprint": a data já aparece embaixo
    function sessionTitle(name) {
        const title = name.replace(/^\s*\d{1,2}\/\d{1,2}\/\d{2,4}\s*[-–—]\s*/, '');
        return title || name;
    }

    function formatDuration(seconds) {
        if (!seconds) return '';
        const minutes = Math.round(seconds / 60);
        if (minutes < 1) return 'menos de 1 min';
        if (minutes < 60) return `${minutes} min`;
        return `${Math.floor(minutes / 60)} h ${String(minutes % 60).padStart(2, '0')} min`;
    }

    // Para buscar sem diferenciar maiúsculas nem acentos
    function normalize(text) {
        return text.normalize('NFD').replace(/[\u0300-\u036f]/g, '').toLowerCase();
    }

    // Reuniões do workspace que passam pela busca, na ordem da lista
    function visibleSessions() {
        const query = normalize(sessionSearch.value.trim());
        return workspaceSessions().filter(s => !query || normalize(`${s.name} ${s.summary || ''} ${s.notes || ''}`).includes(query));
    }

    // "Ontem 10:00 · 16 min": quando a reunião foi gravada e quanto durou
    function sessionWhen(s) {
        const recorded = new Date(s.recorded_at);
        const when = `${formatDateGroup(s.recorded_at)} ${recorded.toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' })}`;
        return [when, formatDuration(s.duration)].filter(Boolean).join(' · ');
    }

    // Vai na frente da linha de data da reunião que está sendo gravada
    function recordingDot() {
        const dot = document.createElement('span');
        dot.className = 'rec-dot';
        dot.textContent = '● Gravando · ';
        return dot;
    }

    // Cabeçalho da área principal: o nome da reunião aberta e, embaixo, a mesma
    // linha de data da lista
    const sessionHeading = document.getElementById('sessionHeading');
    const sessionMeta = document.getElementById('sessionMeta');

    function renderSessionHeader() {
        const s = currentSession();
        sessionHeading.textContent = s ? sessionTitle(s.name) : '';
        sessionHeading.title = s ? s.name : '';
        sessionMeta.textContent = s ? sessionWhen(s) : '';
        if (s && isRecording) sessionMeta.prepend(recordingDot());
    }

    function renderSessionList() {
        // O cabeçalho mostra a reunião aberta como a lista: muda junto com ela
        // (nome novo, gravação começando ou parando, duração)
        renderSessionHeader();
        const shown = visibleSessions();
        sessionList.innerHTML = '';
        if (shown.length === 0) {
            const empty = document.createElement('li');
            empty.className = 'session-list-empty';
            empty.textContent = sessionSearch.value.trim() ? 'Nenhuma reunião encontrada.' : 'Nenhuma reunião neste workspace ainda.';
            sessionList.append(empty);
            return;
        }
        shown.forEach(s => {
            const item = document.createElement('button');
            item.type = 'button';
            item.className = 'session-item' + (s.id === currentSessionID ? ' active' : '');
            item.title = s.name;
            const title = document.createElement('span');
            title.className = 'session-title';
            title.textContent = sessionTitle(s.name);
            const meta = document.createElement('span');
            meta.className = 'session-meta';
            // Com um nome sugerido pela IA esperando resposta, a lista avisa: a
            // faixa com a sugestão só aparece com a reunião aberta
            const suggested = nameSuggestions[s.id] ? 'nome sugerido' : '';
            meta.textContent = [sessionWhen(s), suggested].filter(Boolean).join(' · ');
            if (isRecording && s.id === currentSessionID) meta.prepend(recordingDot());
            item.append(title, meta);
            item.addEventListener('click', () => {
                if (settingsOpen && (s.id === currentSessionID || !isRecording)) closeSettings();
                selectSession(s.id);
            });
            const li = document.createElement('li');
            li.append(item);
            sessionList.append(li);
        });
    }

    sessionSearch.addEventListener('input', renderSessionList);
    sessionSearch.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') {
            sessionSearch.value = '';
            renderSessionList();
        }
    });

    function selectSession(id) {
        if (id === currentSessionID) return;
        if (isRecording) {
            alert("Pare a gravação antes de trocar de sessão!");
            return;
        }
        currentSessionID = id;
        openSession();
    }

    // Mostra a sessão atual e abre a aba que faz mais sentido para ela
    async function openSession() {
        const sessionID = currentSessionID;
        const session = currentSession();
        renderSessionList();
        updateViewMode();
        updateRecordStatus();
        // As notas não esperam as falas: o campo passa a ser desta reunião já
        loadNotes();
        updateSendBtn();
        renderNameSuggestion();
        displaySessionSummary(session ? session.summary : '');
        if (!sessionID) {
            sessionAudioPlayer.pause();
            return;
        }
        const transcriptCount = await loadHistory();
        // Outra reunião foi aberta enquanto esta carregava: quem escolhe a aba é ela
        if (sessionID !== currentSessionID) return;
        showTab(defaultTab(session, transcriptCount));
    }

    // Barra de status: a IA do assistente em uso. Com uma IA na nuvem, o aviso
    // de que o texto das reuniões sai do computador fica sempre à vista.
    const statusLLM = document.getElementById('statusLLM');
    const llmStatusLabels = {
        ollama: 'IA: Ollama, neste computador',
        agy: 'IA: agy, na nuvem',
        claude: 'IA: Claude, na nuvem',
    };

    function renderStatusBar(provider) {
        const cloud = provider === 'agy' || provider === 'claude';
        statusLLM.textContent = llmStatusLabels[provider] || '';
        statusLLM.hidden = !llmStatusLabels[provider];
        statusLLM.classList.toggle('cloud', cloud);
        statusLLM.title = cloud
            ? 'O texto das reuniões é enviado ao serviço da IA escolhida nas configurações'
            : 'O texto das reuniões não sai do computador';
    }

    async function checkServicesHealth() {
        try {
            const health = await window.go.backend.App.CheckServices();
            renderStatusBar(health.provider);
            const ollamaHealth = document.getElementById('ollamaHealth');
            const whisperHealth = document.getElementById('whisperHealth');
            const errorDialog = document.getElementById('errorDialog');

            // A IA verificada é a escolhida nas configurações: o Ollama, o agy ou o claude
            const cloud = health.provider === 'agy' || health.provider === 'claude';
            const llmOK = health[health.provider];
            document.getElementById('llmHealthLabel').innerText = cloud ? `${health.provider} (IA na nuvem):` : 'Ollama:';
            document.getElementById('llmHealthHelp').innerText = cloud
                ? `o ${health.provider} está instalado (o comando ${health.provider} no terminal)`
                : 'o Ollama está rodando na porta 11434';
            if (ollamaHealth) {
                ollamaHealth.innerText = llmOK ? 'OK' : (cloud ? 'Não encontrado' : 'Indisponível');
                ollamaHealth.style.color = llmOK ? 'var(--accent-text)' : 'var(--danger-text)';
            }
            // Sem o Ollama, dá para seguir com uma IA na nuvem que esteja instalada
            const ollamaDown = health.provider === 'ollama' && !health.ollama;
            document.getElementById('useAgyBtn').style.display = ollamaDown && health.agy ? '' : 'none';
            document.getElementById('useClaudeBtn').style.display = ollamaDown && health.claude ? '' : 'none';
            if (whisperHealth) {
                whisperHealth.innerText = health.whisper ? 'OK' : 'Indisponível';
                whisperHealth.style.color = health.whisper ? 'var(--accent-text)' : 'var(--danger-text)';
            }
            document.getElementById('whisperPort').innerText = health.whisper_port;

            if (!health.ok) {
                if (errorDialog && !errorDialog.open) {
                    errorDialog.showModal();
                }
                return false;
            } else {
                if (errorDialog && errorDialog.open) {
                    errorDialog.close();
                }
                return true;
            }
        } catch (e) {
            // Sem aviso na tela: a verificação se repete a cada 2 s até dar certo
            logError('Erro ao verificar os serviços', e);
            return false;
        }
    }

    async function init() {
        try {
            updateRecordStatus();
            // A versão, na barra de status
            window.go.backend.App.GetVersion()
                .then(version => { document.getElementById('statusVersion').textContent = `Recorder ${version}`; })
                .catch(e => logError('Erro ao ler a versão do app', e));

            const retryHealthBtn = document.getElementById('retryHealthBtn');
            [['useAgyBtn', 'agy'], ['useClaudeBtn', 'claude']].forEach(([id, provider]) => {
                document.getElementById(id).addEventListener('click', async () => {
                    try {
                        await window.go.backend.App.SetLLM(provider, '');
                    } catch (e) {
                        alert(`Não foi possível usar o ${provider}: ` + e);
                        return;
                    }
                    await checkServicesHealth();
                });
            });
            if (retryHealthBtn) {
                retryHealthBtn.addEventListener('click', async () => {
                    retryHealthBtn.disabled = true;
                    retryHealthBtn.innerText = 'Verificando...';
                    await checkServicesHealth();
                    retryHealthBtn.disabled = false;
                    retryHealthBtn.innerText = 'Tentar Novamente';
                });
            }

            let initialized = false;
            
            const finishInit = async () => {
                const status = await window.go.backend.App.GetStatus();
                
                await loadWorkspaces();
                if (status.session_id) {
                    currentSessionID = status.session_id;
                    // A gravação em andamento pode ser de outro workspace
                    allSessions = await window.go.backend.App.GetSessions() || [];
                    const recording = currentSession();
                    if (recording && recording.workspace_id !== currentWorkspaceID) {
                        currentWorkspaceID = recording.workspace_id;
                        renderWorkspaces();
                    }
                }
                try {
                    screenCapture = await window.go.backend.App.GetScreenCapture();
                } catch (e) {
                    logError('Erro ao ler a captura de tela', e);
                }
                renderCaptureButton();
                servicesReady = true;
                pendingBlocks = status.pending || 0;
                updateRecordingUI(status.recording, status.started_at);
                
                await loadSessions();
                audioServerURL = await window.go.backend.App.GetAudioServerURL();
                if (currentSessionID) {
                    openSession();
                }
                setupEvents();
            };

            const checkReady = async () => {
                if (initialized) return;
                
                const isOk = await checkServicesHealth();
                if (isOk) {
                    initialized = true;
                    finishInit().catch((e) => {
                        // Sem isto a tela ficava em "Inicializando IA..." para sempre, sem dizer por quê
                        initFailed = true;
                        updateRecordStatus();
                        logError('Erro ao iniciar', e);
                        showToast(`Não foi possível iniciar o Recorder: ${errorText(e)}. Feche o app e abra de novo.`, 'error', true);
                    });
                } else {
                    setTimeout(checkReady, 2000);
                }
            };
            
            checkReady();
        } catch (e) {
            reportError('Não foi possível iniciar o Recorder', e);
        }
    }

    // Um diálogo de cada vez. Cada tipo usa um único <dialog>: um pedido que
    // chegasse com ele aberto trocava o texto e a resposta do que estava na
    // tela. O pedido seguinte espera o anterior fechar.
    let dialogQueue = Promise.resolve();

    function queueDialog(open) {
        const result = dialogQueue.then(open);
        dialogQueue = result.catch(() => {});
        return result;
    }

    function showConfirm(title, message, okLabel = 'Apagar', danger = true) {
        return queueDialog(() => new Promise((resolve) => {
            const dialog = document.getElementById('confirmDialog');
            const okBtn = document.getElementById('confirmOkBtn');
            document.getElementById('confirmTitle').innerText = title;
            document.getElementById('confirmMessage').innerText = message;
            okBtn.innerText = okLabel;
            okBtn.classList.toggle('danger-btn', danger);
            
            // Fechar com Esc não muda o returnValue: sem isso valeria o do último uso
            dialog.returnValue = '';
            dialog.addEventListener('close', () => resolve(dialog.returnValue === 'confirm'), { once: true });
            dialog.showModal();
        }));
    }

    function showPrompt(title, message, defaultValue = '', allowEmpty = false, confirmLabel = 'Salvar') {
        return queueDialog(() => new Promise((resolve) => {
            const dialog = document.getElementById('promptDialog');
            const input = document.getElementById('promptInput');
            document.getElementById('promptTitle').innerText = title;
            document.getElementById('promptMessage').innerText = message;
            document.getElementById('promptConfirmBtn').innerText = confirmLabel;
            input.value = defaultValue;
            input.required = !allowEmpty;
            
            dialog.addEventListener('close', () => {
                if (dialog.returnValue === 'confirm') {
                    resolve(input.value.trim());
                } else {
                    resolve(null);
                }
            }, { once: true });
            dialog.returnValue = '';
            dialog.showModal();
        }));
    }

    deleteSessionBtn.addEventListener('click', async () => {
        const sessionID = currentSessionID;
        if (!sessionID) return;
        const confirmDelete = await showConfirm('Apagar Sessão', isRecording
            ? 'Esta sessão está sendo gravada. A gravação para e a sessão é apagada com todas as suas transcrições. Tem certeza?'
            : 'Tem certeza que deseja apagar a sessão atual e todas as suas transcrições?');
        if (!confirmDelete) return;

        try {
            await window.go.backend.App.DeleteSession(sessionID);
        } catch (e) {
            reportError('Não foi possível apagar a sessão', e);
            return;
        }
        // O que era dela não tem mais onde ser salvo nem mostrado
        missingSessions.add(sessionID);
        delete nameSuggestions[sessionID];
        if (notesSessionID === sessionID) {
            clearTimeout(notesTimer);
            notesDirty = false;
        }
        if (currentSessionID === sessionID) currentSessionID = '';
        // O backend para a gravação da reunião apagada: a tela acompanha
        try {
            const status = await window.go.backend.App.GetStatus();
            pendingBlocks = status.pending || 0;
            updateRecordingUI(status.recording, status.started_at);
        } catch (e) {
            logError('Erro ao ler o estado da gravação', e);
        }
        await loadSessions();
        await openSession();
    });

    async function renameSessionTo(sessionID, name) {
        try {
            await window.go.backend.App.RenameSession(sessionID, name);
            // The UI will be updated via the 'session-renamed' event
        } catch (e) {
            reportError('Não foi possível renomear a sessão', e);
        }
    }

    renameSessionBtn.addEventListener('click', async () => {
        const sessionID = currentSessionID;
        if (!sessionID) return;
        const session = currentSession();
        const name = await showPrompt('Renomear Sessão', 'Novo nome da sessão:', session ? session.name : '', false, 'Renomear');
        if (!name) return;
        await renameSessionTo(sessionID, name);
    });

    // --- Nome sugerido pela IA ---
    // Fica numa faixa na própria reunião, e espera ela ser aberta, em vez de um
    // diálogo que interrompia o que o usuário estivesse fazendo (e tomava o
    // lugar de outro diálogo aberto).
    const nameSuggestions = {}; // session_id -> nome sugerido
    const nameSuggestionBar = document.getElementById('nameSuggestion');

    function renderNameSuggestion() {
        const name = nameSuggestions[currentSessionID];
        nameSuggestionBar.hidden = !name;
        document.getElementById('nameSuggestionName').textContent = name || '';
    }

    function dropNameSuggestion(sessionID) {
        delete nameSuggestions[sessionID];
        renderNameSuggestion();
        renderSessionList(); // tira o aviso "nome sugerido" da lista
    }

    document.getElementById('nameSuggestionAcceptBtn').addEventListener('click', () => {
        const sessionID = currentSessionID, name = nameSuggestions[sessionID];
        if (!name) return;
        dropNameSuggestion(sessionID);
        renameSessionTo(sessionID, name);
    });

    document.getElementById('nameSuggestionEditBtn').addEventListener('click', async () => {
        const sessionID = currentSessionID, suggested = nameSuggestions[sessionID];
        if (!suggested) return;
        const name = await showPrompt('Sugestão de Nome', 'A IA sugeriu este nome para a sessão (edite se quiser):', suggested, false, 'Renomear');
        if (!name) return; // desistiu: a sugestão continua na faixa
        dropNameSuggestion(sessionID);
        await renameSessionTo(sessionID, name);
    });

    document.getElementById('nameSuggestionDismissBtn').addEventListener('click', () => dropNameSuggestion(currentSessionID));

    regenerateSummaryBtn.addEventListener('click', async () => {
        if (!currentSessionID) return;
        const instructions = await showPrompt('Refazer Resumo', 'Opcional: O que você gostaria de focar ou mudar neste novo resumo? (Deixe em branco para o padrão)', '', true, 'Refazer');
        if (instructions === null) return; // User cancelled
        await regenerateSummary(instructions);
    });

    async function regenerateSummary(instructions) {
        const sessionID = currentSessionID;
        try {
            document.getElementById('sessionSummaryContent').innerHTML = '<i>Gerando novo resumo, por favor aguarde...</i>';
            await window.go.backend.App.RegenerateSummary(sessionID, instructions);
        } catch (e) {
            reportError('Não foi possível refazer o resumo', e);
            // O resumo que a reunião tinha continua salvo e volta para a tela.
            // Com outra reunião aberta, a tela é dela: nada a desenhar.
            if (sessionID === currentSessionID) {
                const session = currentSession();
                displaySessionSummary(session ? session.summary : '');
            }
        }
    }

    toggleRecordBtn.addEventListener('click', async () => {
        if (!currentSessionID) return;

        try {
            let status;
            if (isRecording) {
                status = await window.go.backend.App.StopRecording();
                // Dar tempo para o backend salvar o .m4a (a duração na lista muda
                // junto, e é por ela que o player sabe que o áudio mudou)
                setTimeout(() => { loadSessions().then(() => loadSessionAudio()); }, 2000);
            } else {
                status = await window.go.backend.App.StartRecording(currentSessionID);
                if (status.recording) showTab('transcript');
            }
            if (status.session_id) {
                currentSessionID = status.session_id;
            }
            updateRecordingUI(status.recording, status.started_at);
        } catch (e) {
            reportError(isRecording ? 'Não foi possível parar a gravação' : 'Não foi possível iniciar a gravação', e);
        }
    });
    
    const handleNewSession = async () => {
        const ws = currentWorkspace();
        const name = await showPrompt('Nova Sessão', `Nome da nova sessão/reunião${ws ? ` em ${ws.name}` : ''}:`, '', false, 'Criar');
        if (!name) return;
        
        try {
            const session = await window.go.backend.App.CreateSession(currentWorkspaceID, name);
            
            if (isRecording) {
                await window.go.backend.App.StopRecording();
                updateRecordingUI(false);
            }
            
            currentSessionID = session.id;
            sessionSearch.value = '';
            closeSettings();
            await loadSessions();
            openSession();
        } catch(e) {
            reportError('Não foi possível criar a sessão', e);
        }
    };

    newSessionBtn.addEventListener('click', handleNewSession);
    
    const welcomeNewSessionBtn = document.getElementById('welcomeNewSessionBtn');
    if (welcomeNewSessionBtn) {
        welcomeNewSessionBtn.addEventListener('click', handleNewSession);
    }

    // Importar um áudio gravado fora do app (ex: no celular) vira uma sessão
    // nova, transcrita em segundo plano como uma gravação feita aqui
    const importButtons = [document.getElementById('importAudioBtn'), document.getElementById('welcomeImportAudioBtn')];

    const handleImportAudio = async () => {
        if (isRecording) {
            alert('Pare a gravação antes de importar um áudio.');
            return;
        }
        importing = true;
        importButtons.forEach(btn => { btn.disabled = true; });
        updateRecordStatus();
        try {
            const session = await window.go.backend.App.ImportAudio(currentWorkspaceID);
            if (!session) return; // desistiu na escolha do arquivo
            if (isRecording) {
                // Uma gravação começou enquanto o arquivo era lido: a tela
                // continua na reunião que está sendo gravada
                await loadSessions();
                showToast(`Áudio importado em "${sessionTitle(session.name)}". Ele está na lista de reuniões.`, 'info');
                return;
            }
            closeSettings();
            currentSessionID = session.id;
            sessionSearch.value = '';
            await loadSessions();
            await openSession();
            // As falas aparecem aqui conforme cada trecho é transcrito
            showTab('transcript');
        } catch (e) {
            logError('Erro ao importar áudio', e);
            alert('Erro ao importar o áudio: ' + e);
        } finally {
            importing = false;
            importButtons.forEach(btn => { btn.disabled = false; });
            updateRecordStatus();
        }
    };

    importButtons.forEach(btn => btn.addEventListener('click', handleImportAudio));

    function formatDateGroup(dateString) {
        const date = new Date(dateString);
        const today = new Date();
        const yesterday = new Date(today);
        yesterday.setDate(yesterday.getDate() - 1);

        if (date.toDateString() === today.toDateString()) {
            return "Hoje";
        } else if (date.toDateString() === yesterday.toDateString()) {
            return "Ontem";
        } else {
            return date.toLocaleDateString('pt-BR');
        }
    }

    // Falas em ordem cronológica, agrupadas pelo dia em que a reunião foi
    // gravada. As falas com posição no áudio usam a data da gravação, e não a
    // hora em que foram transcritas (ex: gravação recuperada ou reprocessada
    // dias depois); as antigas, sem posição, a hora da transcrição.
    function appendTranscript(t) {
        if (t.session_id !== currentSessionID) return;
        transcriptsEmpty.hidden = true;

        const session = currentSession();
        const day = t.offset >= 0 && session ? session.recorded_at : t.timestamp;
        const groupName = formatDateGroup(day);
        const groupId = 'group-' + groupName.replace(/\//g, '-').replace(/\s/g, '-');

        let groupDiv = document.getElementById(groupId);
        if (!groupDiv) {
            groupDiv = document.createElement('div');
            groupDiv.className = 'date-group';
            groupDiv.id = groupId;

            const header = document.createElement('div');
            header.className = 'date-header';
            header.innerText = groupName;
            
            groupDiv.appendChild(header);
            transcriptsContainer.append(groupDiv);
        }

        const item = document.createElement('div');
        item.className = 'transcript-item';
        
        const timeEl = document.createElement('span');
        timeEl.className = 'transcript-time';
        if (t.offset >= 0) {
            // Posição da fala dentro do áudio da sessão; clicar toca a partir dali
            const offsetEl = document.createElement('button');
            offsetEl.type = 'button';
            offsetEl.className = 'transcript-offset';
            offsetEl.textContent = formatOffset(t.offset);
            offsetEl.title = 'Ouvir a partir daqui';
            offsetEl.addEventListener('click', () => playFrom(t.offset));
            timeEl.append(offsetEl);
        } else {
            // Falas antigas, sem a posição no áudio: hora em que foram transcritas
            timeEl.textContent = new Date(t.timestamp).toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' });
        }
        if (t.speaker) {
            // Clicar em quem falou permite dizer quem é a pessoa
            const speakerEl = document.createElement('button');
            speakerEl.type = 'button';
            speakerEl.className = 'transcript-speaker ' + speakerColorClass(t.speaker);
            speakerEl.textContent = t.speaker;
            speakerEl.title = 'Clique para dizer quem é';
            speakerEl.addEventListener('click', () => renameSpeaker(t.speaker));
            timeEl.append(' · ', speakerEl);
        }
        const textEl = document.createElement('div');
        textEl.className = 'transcript-text';
        textEl.textContent = t.text;
        item.append(timeEl, textEl);
        groupDiv.append(item);
    }

    // Momentos citados como [12:34] ou [1:02:03] viram links que tocam o áudio
    const timePattern = /\[(?:(\d{1,2}):)?(\d{1,2}):(\d{2})\]/g;

    function linkTimes(root) {
        if (!root) return;
        const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
        const nodes = [];
        while (walker.nextNode()) {
            if (timePattern.test(walker.currentNode.nodeValue)) nodes.push(walker.currentNode);
            timePattern.lastIndex = 0;
        }
        nodes.forEach(node => {
            const fragment = document.createDocumentFragment();
            let last = 0;
            node.nodeValue.replace(timePattern, (match, h, m, s, index) => {
                fragment.append(node.nodeValue.slice(last, index));
                fragment.append(timeLink(match.slice(1, -1), (parseInt(h || '0', 10) * 60 + parseInt(m, 10)) * 60 + parseInt(s, 10)));
                last = index + match.length;
                return match;
            });
            fragment.append(node.nodeValue.slice(last));
            node.replaceWith(fragment);
        });
    }

    function timeLink(label, seconds) {
        const link = document.createElement('button');
        link.type = 'button';
        link.className = 'time-link';
        link.textContent = label;
        link.title = 'Ouvir a partir daqui';
        link.addEventListener('click', () => playFrom(seconds));
        return link;
    }

    // Tarefas combinadas na reunião, com caixa para marcar como feita
    async function loadTasks() {
        const sessionID = currentSessionID;
        const section = document.getElementById('tasksSection');
        const list = document.getElementById('tasksList');
        let tasks = [];
        try {
            tasks = await window.go.backend.App.GetTasks(sessionID) || [];
        } catch (e) {
            reportError('Não foi possível carregar as tarefas', e);
        }
        if (sessionID !== currentSessionID) return; // trocou de reunião enquanto carregava
        list.innerHTML = '';
        section.style.display = tasks.length > 0 ? 'block' : 'none';
        tasks.forEach(task => {
            const li = document.createElement('li');
            const check = document.createElement('input');
            check.type = 'checkbox';
            check.checked = task.done;
            check.addEventListener('change', () => {
                const done = check.checked;
                li.classList.toggle('done', done);
                window.go.backend.App.SetTaskDone(task.id, done).catch(e => {
                    // A caixa volta ao que está guardado
                    check.checked = !done;
                    li.classList.toggle('done', !done);
                    reportError('Não foi possível marcar a tarefa', e);
                });
            });
            const text = document.createElement('span');
            text.className = 'task-text';
            text.textContent = task.text;
            const details = document.createElement('small');
            details.className = 'task-details';
            const parts = [task.owner, task.deadline && `prazo: ${task.deadline}`].filter(Boolean);
            details.textContent = parts.join(' · ');
            if (task.offset >= 0) {
                if (parts.length > 0) details.append(' · ');
                details.append(timeLink(formatOffset(task.offset), task.offset));
            }
            li.classList.toggle('done', task.done);
            li.append(check, text, details);
            list.append(li);
        });
    }

    // Nomes prováveis das pessoas, tirados da conversa, esperando confirmação
    let confirmedNames = 0;

    async function loadSpeakerSuggestions() {
        const sessionID = currentSessionID;
        const section = document.getElementById('speakerSuggestions');
        const list = document.getElementById('speakerSuggestionsList');
        let suggestions = [];
        try {
            suggestions = await window.go.backend.App.GetSpeakerSuggestions(sessionID) || [];
        } catch (e) {
            reportError('Não foi possível carregar as sugestões de nomes', e);
        }
        if (sessionID !== currentSessionID) return; // trocou de reunião enquanto carregava
        list.innerHTML = '';
        section.style.display = suggestions.length > 0 ? 'block' : 'none';
        suggestions.forEach(sg => {
            const li = document.createElement('li');
            const who = document.createElement('div');
            who.className = 'suggestion-who';
            const from = document.createElement('span');
            from.className = 'transcript-speaker ' + speakerColorClass(sg.speaker);
            from.textContent = sg.speaker;
            // O app já deu o nome à voz (pistas bastantes na conversa): fica o
            // porquê, com como desfazer
            const applied = sg.speaker === sg.name;
            if (applied) {
                who.append(from, ' recebeu o nome pela conversa');
            } else {
                const to = document.createElement('strong');
                to.textContent = sg.name;
                who.append(from, ' parece ser ', to);
            }
            const evidence = document.createElement('small');
            evidence.className = 'suggestion-evidence';
            evidence.textContent = `“${sg.evidence}”`;
            linkTimes(evidence);
            const confirm = document.createElement('button');
            confirm.type = 'button';
            confirm.className = 'primary-btn';
            confirm.textContent = applied ? 'Está certo' : 'Confirmar';
            confirm.addEventListener('click', () => answerSuggestion(sg, true));
            const dismiss = document.createElement('button');
            dismiss.type = 'button';
            dismiss.className = 'secondary-btn';
            dismiss.textContent = applied ? 'Não é' : 'Ignorar';
            if (applied) dismiss.title = 'Desfaz o nome: a voz volta a ser "Pessoa N" e deixa de contar para a voz guardada dessa pessoa';
            dismiss.addEventListener('click', () => answerSuggestion(sg, false));
            const actions = document.createElement('div');
            actions.className = 'suggestion-actions';
            actions.append(confirm, dismiss);
            li.append(who, evidence, actions);
            list.append(li);
        });
    }

    async function answerSuggestion(sg, accept) {
        const sessionID = currentSessionID;
        // Num nome que o app já tinha dado, quem muda as falas é o "Não é"
        const applied = sg.speaker === sg.name;
        try {
            if (accept) {
                await window.go.backend.App.ConfirmSpeakerSuggestion(sessionID, sg.speaker, sg.name);
                if (!applied) confirmedNames++;
            } else {
                await window.go.backend.App.DismissSpeakerSuggestion(sessionID, sg.speaker);
                if (applied) confirmedNames++;
            }
        } catch (e) {
            reportError(accept ? 'Não foi possível confirmar o nome' : 'Não foi possível ignorar a sugestão', e);
            return;
        }
        if (sessionID !== currentSessionID) return;
        await reloadTranscripts();
        await loadSpeakerSuggestions();

        // Depois da última sugestão, oferece refazer o resumo com os nomes
        const remaining = document.getElementById('speakerSuggestionsList').children.length;
        if (remaining === 0 && confirmedNames > 0) {
            confirmedNames = 0;
            const redo = await showConfirm('Refazer o resumo?', 'Os nomes de quem falou mudaram. Quer refazer o resumo da sessão com eles?', 'Refazer', false);
            if (redo) await regenerateSummary('');
        }
    }

    // 754.2 -> "12:34"; a partir de uma hora, "1:02:03"
    function formatOffset(seconds) {
        const total = Math.floor(seconds);
        const h = Math.floor(total / 3600);
        const m = Math.floor((total % 3600) / 60);
        const s = String(total % 60).padStart(2, '0');
        return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${s}` : `${String(m).padStart(2, '0')}:${s}`;
    }

    // Toca o áudio da sessão a partir de seconds. As sessões antigas divididas
    // em várias partes não têm uma posição única, então ficam de fora.
    function playFrom(seconds) {
        if (currentAudioParts.length !== 1) return;
        const seek = () => {
            sessionAudioPlayer.currentTime = seconds;
            sessionAudioPlayer.play().catch(e => logError('Erro ao tocar o áudio', e));
        };
        if (sessionAudioPlayer.readyState >= 1) {
            seek();
        } else {
            sessionAudioPlayer.addEventListener('loadedmetadata', seek, { once: true });
        }
    }

    // Cor de cada pessoa: "Pessoa 3" usa a 3ª cor e um nome usa uma cor fixa
    // calculada a partir dele. As cores se repetem a partir da 6ª pessoa.
    function speakerColorClass(speaker) {
        const auto = /^Pessoa (\d+)$/.exec(speaker);
        let n = 0;
        if (auto) {
            n = parseInt(auto[1], 10) - 1;
        } else {
            for (const ch of speaker) n = (n * 31 + ch.codePointAt(0)) >>> 0;
        }
        return 'speaker-' + (n % 6 + 1);
    }

    async function renameSpeaker(label) {
        const sessionID = currentSessionID;
        const name = await showPrompt(
            'Quem está falando?',
            `Nome para "${label}" nesta sessão. A voz fica guardada para reconhecer a pessoa nas próximas reuniões.`,
            /^Pessoa \d+$/.test(label) ? '' : label
        );
        if (!name || name === label) return;

        try {
            await window.go.backend.App.RenameSpeaker(sessionID, label, name);
        } catch (e) {
            reportError('Não foi possível renomear a pessoa', e);
            return;
        }
        if (sessionID !== currentSessionID) return;
        await reloadTranscripts();

        const redo = await showConfirm('Refazer o resumo?', `As falas de "${label}" agora aparecem como "${name}". Quer refazer o resumo da sessão com o nome?`, 'Refazer', false);
        if (redo) await regenerateSummary('');
    }

    // Recarrega as falas mantendo a posição da rolagem. Devolve quantas são.
    // Só a recarga mais recente da reunião aberta desenha: a resposta de uma
    // reunião aberta antes (ou de um pedido anterior) pode chegar depois.
    let transcriptsLoad = 0;
    let transcriptsSessionID = ''; // reunião das falas que estão na tela

    async function reloadTranscripts() {
        const sessionID = currentSessionID;
        const load = ++transcriptsLoad;
        const scroll = transcriptTab.scrollTop;
        const dataTrans = await window.go.backend.App.GetTranscripts(sessionID) || [];
        if (load !== transcriptsLoad || sessionID !== currentSessionID) return dataTrans.length;
        transcriptsContainer.innerHTML = '';
        transcriptsEmpty.hidden = false;
        transcriptsSessionID = sessionID;
        dataTrans.forEach(t => appendTranscript(t));
        transcriptTab.scrollTop = scroll;
        return dataTrans.length;
    }

    // Carrega as falas, tarefas, sugestões, chat e áudio da sessão atual (as
    // notas são carregadas ao abrir a sessão). Devolve quantas falas a sessão
    // tem. Se outra reunião for aberta, ou esta recarregada, antes do fim, para
    // por ali: quem desenha é a carga mais nova.
    let historyLoad = 0;

    async function loadHistory() {
        const sessionID = currentSessionID;
        if (!sessionID) return 0;
        const load = ++historyLoad;
        const stale = () => load !== historyLoad || sessionID !== currentSessionID;

        clearChat();
        // As falas de outra reunião não ficam na tela enquanto as desta carregam
        if (transcriptsSessionID !== sessionID) {
            transcriptsContainer.innerHTML = '';
            transcriptsEmpty.hidden = true;
            transcriptsSessionID = sessionID;
        }

        let transcriptCount = 0;
        try {
            transcriptTab.scrollTop = 0;
            transcriptCount = await reloadTranscripts();
            if (stale()) return transcriptCount;
            loadTasks();
            loadSpeakerSuggestions();
            loadScreenshots();
            await loadChat();
            if (stale()) return transcriptCount;
            await loadSessionAudio();
        } catch (e) {
            reportError('Não foi possível carregar a reunião', e);
        }
        return transcriptCount;
    }

    // O áudio que está no player: reunião, duração e partes. Enquanto for o
    // mesmo, o player não é recarregado: recarregar voltava para o começo o que
    // o usuário estava ouvindo (ex: quando chegava um resumo refeito).
    let loadedAudio = '';

    async function loadSessionAudio() {
        const sessionID = currentSessionID;
        if (!sessionID) return;
        try {
            // Sem áudio, o Go devolve null
            const parts = await window.go.backend.App.GetSessionAudioParts(sessionID) || [];
            if (sessionID !== currentSessionID) return; // trocou de reunião enquanto carregava
            const session = currentSession();
            const audio = [sessionID, session ? session.duration : 0, ...parts].join('|');
            if (audio === loadedAudio) return;
            loadedAudio = audio;
            currentAudioParts = parts;
            if (parts.length > 0) {
                audioBar.style.display = 'flex';
                currentAudioPartIndex = 0;
                playAudioPart(0);
            } else {
                audioBar.style.display = 'none';
                sessionAudioPlayer.pause();
                sessionAudioPlayer.removeAttribute('src');
            }
        } catch (e) {
            reportError('Não foi possível carregar o áudio da reunião', e);
        }
    }

    function playAudioPart(index) {
        if (!currentAudioParts || index >= currentAudioParts.length) return;
        currentAudioPartIndex = index;
        const part = currentAudioParts[index];
        // O arquivo cresce quando uma gravação é retomada: evita a versão em cache
        const url = `${audioServerURL}/${part}?v=${Date.now()}`;
        
        const wasPlaying = !sessionAudioPlayer.paused;
        sessionAudioPlayer.src = url;
        
        if (wasPlaying || index > 0) {
            sessionAudioPlayer.play().catch(e => logError('Erro ao tocar o áudio', e));
        }
    }

    sessionAudioPlayer.addEventListener('ended', () => {
        if (currentAudioPartIndex + 1 < currentAudioParts.length) {
            playAudioPart(currentAudioPartIndex + 1);
            sessionAudioPlayer.play().catch(e => logError('Erro ao tocar o áudio', e));
        }
    });

    // --- Player do áudio da reunião ---
    // Os controles são do app, e não os do navegador (que mudam de cara do
    // macOS para o Windows). Quem toca continua sendo o <audio>: os botões só
    // agem nele e mostram o estado dele, e os tempos do resumo, da transcrição
    // e do chat seguem funcionando como antes (playFrom).
    const audioPlayBtn = document.getElementById('audioPlayBtn');
    const audioTime = document.getElementById('audioTime');
    const audioSeek = document.getElementById('audioSeek');
    const audioRate = document.getElementById('audioRate');
    const audioMuteBtn = document.getElementById('audioMuteBtn');
    const audioVolume = document.getElementById('audioVolume');

    function renderAudioButtons() {
        const playing = !sessionAudioPlayer.paused && !sessionAudioPlayer.ended;
        audioPlayBtn.innerHTML = svgIcon(playing ? 'pause' : 'play');
        audioPlayBtn.title = playing ? 'Pausar' : 'Tocar';
        const silent = sessionAudioPlayer.muted || sessionAudioPlayer.volume === 0;
        audioMuteBtn.innerHTML = svgIcon(silent ? 'speaker-off' : 'speaker');
        audioMuteBtn.title = silent ? 'Voltar o som' : 'Tirar o som';
        const volume = sessionAudioPlayer.muted ? 0 : sessionAudioPlayer.volume;
        audioVolume.value = volume;
        // --fill: até onde a barra aparece preenchida (style.css)
        audioVolume.style.setProperty('--fill', `${volume * 100}%`);
    }

    function renderAudioTime() {
        // Sem áudio carregado (ou num arquivo ainda sem duração conhecida), zero
        const duration = Number.isFinite(sessionAudioPlayer.duration) ? sessionAudioPlayer.duration : 0;
        audioSeek.max = duration;
        audioSeek.value = sessionAudioPlayer.currentTime;
        audioSeek.disabled = duration === 0;
        audioSeek.style.setProperty('--fill', duration > 0 ? `${sessionAudioPlayer.currentTime / duration * 100}%` : '0%');
        audioTime.textContent = `${formatOffset(sessionAudioPlayer.currentTime)} / ${formatOffset(duration)}`;
    }

    ['play', 'pause', 'ended', 'volumechange', 'emptied'].forEach(type => sessionAudioPlayer.addEventListener(type, renderAudioButtons));
    ['timeupdate', 'durationchange', 'loadedmetadata', 'seeked', 'emptied'].forEach(type => sessionAudioPlayer.addEventListener(type, renderAudioTime));

    audioPlayBtn.addEventListener('click', () => {
        if (sessionAudioPlayer.paused) {
            sessionAudioPlayer.play().catch(e => logError('Erro ao tocar o áudio', e));
        } else {
            sessionAudioPlayer.pause();
        }
    });

    audioSeek.addEventListener('input', () => {
        sessionAudioPlayer.currentTime = Number(audioSeek.value);
        renderAudioTime();
    });

    audioRate.addEventListener('change', () => {
        const rate = Number(audioRate.value);
        // A padrão também: é ela que vale quando o áudio é recarregado (ex: a
        // gravação foi retomada e o arquivo cresceu)
        sessionAudioPlayer.defaultPlaybackRate = rate;
        sessionAudioPlayer.playbackRate = rate;
    });

    audioMuteBtn.addEventListener('click', () => {
        if (!sessionAudioPlayer.muted && sessionAudioPlayer.volume === 0) {
            // O volume foi arrastado até zero: voltar o som é devolver o volume
            sessionAudioPlayer.volume = 1;
        } else {
            sessionAudioPlayer.muted = !sessionAudioPlayer.muted;
        }
    });

    audioVolume.addEventListener('input', () => {
        sessionAudioPlayer.muted = false;
        sessionAudioPlayer.volume = Number(audioVolume.value);
    });

    renderAudioButtons();
    renderAudioTime();

    if (reprocessSessionBtn) {
        reprocessSessionBtn.addEventListener('click', async () => {
            const sessionID = currentSessionID;
            if (!sessionID) return;
            const confirm = await showConfirm('Reprocessar Áudio', 'Tem certeza que deseja apagar a transcrição atual e transcrever todo o áudio desta sessão novamente?');
            if (!confirm || sessionID !== currentSessionID) return;
            // As falas antigas saem da tela já: o backend as apaga, e as novas
            // chegam uma a uma pelo evento "transcript" (a chamada só volta no
            // fim da transcrição). As sugestões de nomes eram das vozes antigas.
            transcriptsContainer.innerHTML = '';
            transcriptsEmpty.hidden = false;
            document.getElementById('speakerSuggestions').style.display = 'none';
            let error = null;
            try {
                await window.go.backend.App.ReprocessSession(sessionID);
            } catch(e) {
                error = e;
                logError("Erro ao reprocessar", e);
            }
            // No fim, ou se o backend recusou (sem apagar nada), a tela mostra o
            // que está guardado. Com outra reunião aberta, não há o que recarregar.
            if (sessionID === currentSessionID) await loadHistory();
            if (error) alert("Erro ao reprocessar áudio: " + error);
        });
    }

    function setupEvents() {
        window.runtime.EventsOn('transcript', (t) => {
            // Sessão criada ao recuperar uma gravação interrompida: a lista é
            // recarregada uma vez por reunião que não está nela
            if (!allSessions.some(s => s.id === t.session_id) && !missingSessions.has(t.session_id)) {
                missingSessions.add(t.session_id);
                loadSessions();
            }
            // Acompanha as falas novas se a transcrição já estava no fim
            const atEnd = transcriptTab.scrollTop + transcriptTab.clientHeight >= transcriptTab.scrollHeight - 80;
            appendTranscript(t);
            if (atEnd && t.session_id === currentSessionID) {
                transcriptTab.scrollTop = transcriptTab.scrollHeight;
            }
        });

        // ⌃⌥S em qualquer app: captura a tela para a reunião aberta
        window.runtime.EventsOn('capture-shortcut', () => captureScreen());

        window.runtime.EventsOn('transcription-queue', (payload) => {
            pendingBlocks = payload.pending;
            updateRecordStatus();
        });

        // Resumo e resposta do chat chegando enquanto são escritos
        window.runtime.EventsOn('summary-progress', (payload) => {
            if (payload.session_id !== currentSessionID) return;
            const content = document.getElementById('sessionSummaryContent');
            content.classList.remove('empty');
            renderMarkdown(content, payload.summary);
        });

        window.runtime.EventsOn('chat-progress', (payload) => {
            const pending = pendingChats.get(payload.session_id);
            if (payload.session_id !== currentSessionID || !pending) return;
            renderMarkdown(pending.typingMsg.querySelector('.message-content'), payload.answer);
            chatMessages.scrollTop = chatMessages.scrollHeight;
        });

        window.runtime.EventsOn('tasks', (payload) => {
            if (payload.session_id === currentSessionID) loadTasks();
        });

        // Duas vozes eram da mesma pessoa: as falas já mostradas mudaram de rótulo
        window.runtime.EventsOn('speakers-changed', (payload) => {
            if (payload.session_id !== currentSessionID) return;
            reloadTranscripts().catch(e => reportError('Não foi possível recarregar as falas', e));
        });

        window.runtime.EventsOn('speaker-suggestions', (payload) => {
            if (payload.session_id === currentSessionID) loadSpeakerSuggestions();
        });

        window.runtime.EventsOn('summary', (payload) => {
            const sess = allSessions.find(s => s.id === payload.session_id);
            if (sess) {
                sess.summary = payload.summary;
            }
            if (payload.session_id === currentSessionID) {
                displaySessionSummary(payload.summary);
            }
            // O áudio da sessão já foi salvo: atualiza data e duração na lista e,
            // se o áudio mudou, o player (o mesmo áudio continua de onde estava)
            loadSessions().then(() => {
                if (payload.session_id === currentSessionID) loadSessionAudio();
            });
        });

        // working fica ligado enquanto houver algum trabalho; session_id diz de
        // qual reunião é ("" se não for de nenhuma)
        window.runtime.EventsOn('ollama-status', (payload) => {
            backgroundWork = {
                working: !!payload.working,
                // Sem mensagem nova, continua a anterior
                message: payload.message || (payload.working ? backgroundWork.message : ''),
                session_id: payload.session_id || '',
            };
            renderBackgroundWork();
        });

        // Falha num trabalho de segundo plano, que nenhuma chamada esperava (ex:
        // a transcrição de um áudio importado parou). Se for de outra reunião
        // que não a aberta, o aviso diz de qual. Fica na tela até ser fechado:
        // quem deixou o trabalho rodando pode não estar olhando.
        window.runtime.EventsOn('notice', (payload) => {
            if (!payload || !payload.message) return;
            let text = payload.message;
            if (payload.session_id && payload.session_id !== currentSessionID) {
                const session = allSessions.find(s => s.id === payload.session_id);
                if (session) text = `${sessionTitle(session.name)}: ${text}`;
            }
            showToast(text, 'error', true);
        });

        window.runtime.EventsOn('session-renamed', (payload) => {
            const sess = allSessions.find(s => s.id === payload.session_id);
            if (sess) {
                sess.name = payload.name;
            }
            // Com o nome trocado, a sugestão da IA para ela perde o sentido
            delete nameSuggestions[payload.session_id];
            renderNameSuggestion();
            renderSessionList();
            renderBackgroundWork();
        });

        // A IA sugeriu um nome para a reunião: fica guardado e aparece numa
        // faixa quando ela estiver aberta
        window.runtime.EventsOn('suggest-session-name', (payload) => {
            const sess = allSessions.find(s => s.id === payload.session_id);
            if (!payload.name || (sess && sess.name === payload.name)) return;
            nameSuggestions[payload.session_id] = payload.name;
            renderNameSuggestion();
            renderSessionList();
        });
    }

    // --- Chat ---
    // Perguntas esperando resposta: session_id -> { userMsg, typingMsg }. A
    // resposta pode chegar com outra reunião aberta (fica salva e aparece ao
    // voltar); se a reunião for reaberta antes, os balões voltam para a conversa.
    const pendingChats = new Map();

    function updateSendBtn() {
        sendBtn.disabled = chatInput.value.trim().length === 0 || pendingChats.has(currentSessionID);
    }

    function clearChat() {
        Array.from(chatMessages.children).forEach(child => {
            if (child.id !== 'welcomeMessage') {
                child.remove();
            }
        });
    }

    // Uma mensagem salva tem id e pode ser apagada
    function setMessageID(msgDiv, id) {
        msgDiv.dataset.id = id;
        const deleteBtn = document.createElement('button');
        deleteBtn.type = 'button';
        deleteBtn.className = 'msg-delete-btn';
        deleteBtn.title = 'Apagar mensagem';
        deleteBtn.textContent = 'X';
        deleteBtn.addEventListener('click', () => deleteMessage(id, msgDiv));
        msgDiv.prepend(deleteBtn);
    }

    // A resposta do assistente é Markdown; o que o usuário escreveu (e um aviso
    // de erro, com plain) entra como texto, nunca como HTML
    function appendMessage(role, content, id = null, plain = role !== 'assistant') {
        const msgDiv = document.createElement('div');
        msgDiv.className = `message ${role}`;
        const contentEl = document.createElement('div');
        contentEl.className = 'message-content';
        if (plain) {
            contentEl.textContent = content;
        } else {
            renderMarkdown(contentEl, content);
        }
        msgDiv.append(contentEl);
        if (id) {
            setMessageID(msgDiv, id);
        }

        chatMessages.appendChild(msgDiv);
        chatMessages.scrollTop = chatMessages.scrollHeight;
        return msgDiv;
    }

    // O balão do assistente enquanto a resposta não chega
    function appendTyping() {
        const msgDiv = appendMessage('assistant', '', null, true);
        msgDiv.querySelector('.message-content').innerHTML = '<div class="typing-indicator"><span></span><span></span><span></span></div>';
        return msgDiv;
    }

    async function deleteMessage(id, msgDiv) {
        const confirmDel = await showConfirm("Apagar Mensagem", "Tem certeza que deseja apagar esta mensagem?");
        if (!confirmDel) return;
        
        try {
            await window.go.backend.App.DeleteChatMessage(id);
            msgDiv.remove();
        } catch (e) {
            reportError('Não foi possível apagar a mensagem', e);
        }
    }

    // Põe na tela a conversa guardada da reunião aberta. Só a carga mais
    // recente desenha.
    let chatLoad = 0;

    async function loadChat() {
        const sessionID = currentSessionID;
        const load = ++chatLoad;
        let messages = [];
        try {
            messages = await window.go.backend.App.GetChatHistory(sessionID) || [];
        } catch (e) {
            reportError('Não foi possível carregar a conversa do chat', e);
        }
        if (load !== chatLoad || sessionID !== currentSessionID) return;
        clearChat();
        messages.forEach(m => appendMessage(m.role, m.content, m.id));
        // A pergunta desta reunião que ainda espera resposta volta para o fim
        const pending = pendingChats.get(sessionID);
        if (pending) {
            chatMessages.append(pending.userMsg, pending.typingMsg);
            chatMessages.scrollTop = chatMessages.scrollHeight;
        }
    }

    chatInput.addEventListener('input', function() {
        this.style.height = 'auto';
        this.style.height = (this.scrollHeight) + 'px';
        updateSendBtn();
    });

    chatInput.addEventListener('keydown', function(e) {
        if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            if (!sendBtn.disabled) {
                chatForm.dispatchEvent(new Event('submit'));
            }
        }
    });

    chatForm.addEventListener('submit', async (e) => {
        e.preventDefault();
        const sessionID = currentSessionID;
        const question = chatInput.value.trim();
        // Sem pergunta, ou com uma desta reunião ainda sem resposta, não envia
        if (!sessionID || !question || pendingChats.has(sessionID)) return;
        chatInput.value = '';
        chatInput.style.height = 'auto';

        const pending = { userMsg: appendMessage('user', question), typingMsg: appendTyping() };
        pendingChats.set(sessionID, pending);
        updateSendBtn();

        let data = null, error = null;
        try {
            data = await window.go.backend.App.SendChatMessage(sessionID, question, searchAllSessions.checked);
        } catch (e) {
            error = e;
        }
        pendingChats.delete(sessionID);
        updateSendBtn();

        if (sessionID !== currentSessionID) {
            // Outra reunião está aberta: a resposta ficou salva e aparece ao voltar
            if (error) reportError('Não foi possível responder à pergunta do chat', error);
            return;
        }
        if (!pending.typingMsg.isConnected) {
            // A conversa estava sendo recarregada quando a resposta chegou: vale o que está guardado
            if (error) reportError('Não foi possível responder à pergunta do chat', error);
            await loadChat();
            return;
        }
        pending.typingMsg.remove();
        if (error) {
            logError('Erro ao responder no chat', error);
            appendMessage('assistant', 'Não foi possível responder: ' + errorText(error), null, true);
            return;
        }
        if (data.answer) {
            if (data.user_msg_id) {
                setMessageID(pending.userMsg, data.user_msg_id);
            }
            appendMessage('assistant', data.answer, data.assistant_msg_id);
        }
    });

    // --- Notas da reunião: o que o usuário escreve fica salvo enquanto ele digita ---
    let notesSessionID = ''; // reunião das notas que estão no campo
    let notesDirty = false;
    let notesTimer = null;
    // As gravações vão em fila: a última coisa escrita é a que fica, e as notas
    // de uma reunião só são lidas depois de salvas
    let notesSaving = Promise.resolve();

    function flashNotesSaved(text) {
        notesSaved.textContent = text;
        notesSaved.classList.add('show');
        clearTimeout(notesSaved.hideTimer);
        notesSaved.hideTimer = setTimeout(() => notesSaved.classList.remove('show'), 1500);
    }

    function flushNotes() {
        clearTimeout(notesTimer);
        if (notesDirty) {
            notesDirty = false;
            const sessionID = notesSessionID, text = notesInput.value;
            // Sem reunião no campo não há onde salvar (ele fica travado nesse
            // caso; isto é só para nunca dizer "Salvo" sem ter salvado)
            if (!sessionID) return notesSaving;
            // A busca da lista de reuniões também olha nas notas
            const session = allSessions.find(s => s.id === sessionID);
            if (session) session.notes = text;
            notesSaving = notesSaving
                .then(() => window.go.backend.App.SetNotes(sessionID, text))
                .then(() => {
                    if (sessionID === notesSessionID) flashNotesSaved('Salvo ✓');
                })
                .catch(e => {
                    reportError('Não foi possível salvar as notas', e);
                    if (sessionID !== notesSessionID) return;
                    notesDirty = true; // tenta de novo na próxima vez
                    flashNotesSaved('Não foi possível salvar');
                });
        }
        return notesSaving;
    }

    // Põe no campo as notas da reunião aberta. O campo de uma reunião que já
    // está aberta não é recarregado: ele pode ter texto ainda não salvo. O
    // campo só destrava com as notas de uma reunião carregadas (no index.html
    // ele já começa travado).
    async function loadNotes() {
        const sessionID = currentSessionID;
        if (sessionID === notesSessionID) return;
        flushNotes(); // as da reunião anterior
        notesSessionID = sessionID;
        notesInput.value = '';
        notesInput.readOnly = true;
        notesSaved.classList.remove('show');
        if (!sessionID) return; // nenhuma reunião aberta
        try {
            await notesSaving;
            const text = await window.go.backend.App.GetNotes(sessionID) || '';
            if (sessionID !== notesSessionID) return; // trocou de reunião enquanto carregava
            notesInput.value = text;
            notesInput.readOnly = false;
        } catch (e) {
            reportError('Não foi possível carregar as notas', e);
            if (sessionID !== notesSessionID) return;
            // O campo fica travado: escrever agora apagaria as notas guardadas
            notesSessionID = '';
            flashNotesSaved('Não foi possível carregar as notas');
        }
    }

    notesInput.addEventListener('input', () => {
        notesDirty = true;
        clearTimeout(notesTimer);
        notesTimer = setTimeout(flushNotes, 800);
    });
    notesInput.addEventListener('blur', () => flushNotes());
    // Janela escondida ou fechando: não espera o intervalo da digitação
    document.addEventListener('visibilitychange', () => { if (document.hidden) flushNotes(); });
    window.addEventListener('pagehide', () => flushNotes());

    // --- Capturas de tela da reunião (ex: um slide mostrado na chamada) ---
    const screenshotDialog = document.getElementById('screenshotDialog');
    const screenshotImage = document.getElementById('screenshotImage');
    const screenshotMoment = document.getElementById('screenshotMoment');
    let screenCapture = { supported: false, shortcut: '' };
    let capturing = false;
    let shownScreenshot = null; // a captura aberta em tamanho grande

    function renderCaptureButton() {
        captureScreenBtn.hidden = !screenCapture.supported;
        document.getElementById('captureShortcut').textContent = screenCapture.shortcut || '';
        captureScreenBtn.title = 'Arraste para escolher uma área da tela ou aperte Espaço para escolher uma janela (Esc desiste). A captura fica guardada com a reunião.'
            + (screenCapture.shortcut ? ` Com uma reunião aberta, ${screenCapture.shortcut} funciona em qualquer app.` : '');
    }

    // As capturas e as miniaturas vêm do servidor local do app
    function screenshotURL(path) {
        return `${audioServerURL}${path}`;
    }

    // Quando a captura foi feita: o momento da gravação, que toca o áudio dali,
    // ou o dia e a hora, se a reunião não estava sendo gravada. Com sentence,
    // numa frase ("Capturada aos 12:34"), para a captura aberta.
    function screenshotWhen(shot, sentence = false) {
        if (shot.offset >= 0) {
            const link = timeLink(formatOffset(shot.offset), shot.offset);
            link.title = 'Ouvir o que se falava nesta hora';
            return sentence ? ['Capturada aos ', link] : [link];
        }
        const day = formatDateGroup(shot.created_at);
        const time = new Date(shot.created_at).toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' });
        if (!sentence) return [`${day} ${time}`];
        // "hoje às 14:32", "ontem às 14:32" ou "em 10/11/2026 às 14:32"
        return [`Capturada ${/\d/.test(day) ? 'em ' + day : day.toLowerCase()} às ${time}`];
    }

    async function loadScreenshots() {
        const sessionID = currentSessionID;
        let shots = [];
        try {
            shots = await window.go.backend.App.GetScreenshots(sessionID) || [];
        } catch (e) {
            reportError('Não foi possível carregar as capturas de tela', e);
        }
        if (sessionID !== currentSessionID) return;
        screenshotsList.innerHTML = '';
        screenshotsSection.hidden = shots.length === 0;
        shots.forEach(shot => {
            const open = document.createElement('button');
            open.type = 'button';
            open.className = 'screenshot-thumb';
            open.title = 'Ver a captura';
            const img = document.createElement('img');
            img.src = screenshotURL(shot.thumb);
            img.alt = 'Captura de tela';
            img.loading = 'lazy';
            open.append(img);
            open.addEventListener('click', () => showScreenshot(shot));
            const when = document.createElement('span');
            when.className = 'screenshot-when';
            when.append(...screenshotWhen(shot));
            const li = document.createElement('li');
            li.className = 'screenshot-item';
            li.append(open, when);
            screenshotsList.append(li);
        });
    }

    // Deixa escolher uma área ou uma janela da tela e guarda na reunião aberta
    async function captureScreen() {
        if (!screenCapture.supported || !currentSessionID || capturing) return;
        capturing = true;
        captureScreenBtn.disabled = true;
        const sessionID = currentSessionID;
        try {
            const shot = await window.go.backend.App.CaptureScreen(sessionID);
            if (shot && sessionID === currentSessionID) {
                await loadScreenshots();
                screenshotsList.scrollLeft = screenshotsList.scrollWidth;
                if (currentTab !== 'notes') flashNotesTab();
            }
        } catch (e) {
            logError('Erro ao capturar a tela', e);
            alert('Não foi possível capturar a tela: ' + e);
        } finally {
            capturing = false;
            captureScreenBtn.disabled = false;
        }
    }

    // Captura feita pelo atalho com outra aba aberta: a aba Notas avisa que chegou
    function flashNotesTab() {
        const tab = document.querySelector('.tab[data-tab="notes"]');
        tab.classList.remove('flash');
        void tab.offsetWidth; // recomeça a animação
        tab.classList.add('flash');
    }

    captureScreenBtn.addEventListener('click', () => captureScreen());

    function showScreenshot(shot) {
        shownScreenshot = shot;
        screenshotImage.src = screenshotURL(shot.url);
        screenshotMoment.replaceChildren(...screenshotWhen(shot, true));
        // O momento toca o áudio da reunião, que fica atrás do diálogo
        const link = screenshotMoment.querySelector('.time-link');
        if (link) link.addEventListener('click', () => screenshotDialog.close());
        screenshotDialog.showModal();
    }

    screenshotDialog.addEventListener('close', () => {
        shownScreenshot = null;
        screenshotImage.removeAttribute('src');
    });

    document.getElementById('screenshotCloseBtn').addEventListener('click', () => screenshotDialog.close());

    document.getElementById('screenshotRevealBtn').addEventListener('click', async () => {
        if (!shownScreenshot) return;
        try {
            await window.go.backend.App.RevealScreenshot(shownScreenshot.id);
        } catch (e) {
            alert('Não foi possível mostrar a captura: ' + e);
        }
    });

    document.getElementById('screenshotDeleteBtn').addEventListener('click', async () => {
        const shot = shownScreenshot;
        if (!shot) return;
        const ok = await showConfirm('Apagar captura', 'Tem certeza que deseja apagar esta captura de tela?');
        if (!ok) return;
        try {
            await window.go.backend.App.DeleteScreenshot(shot.id);
        } catch (e) {
            alert('Não foi possível apagar a captura: ' + e);
            return;
        }
        screenshotDialog.close();
        if (shot.session_id === currentSessionID) loadScreenshots();
    });

    // Tema claro/escuro. O index.html já aplicou o escolhido (ou o do sistema)
    // antes de desenhar a tela; o botão mostra o tema para o qual vai trocar
    const themeToggleBtn = document.getElementById('themeToggleBtn');
    const sunIcon = svgIcon('sun');
    const moonIcon = svgIcon('moon');

    function applyTheme(theme) {
        document.documentElement.dataset.theme = theme;
        const meta = document.querySelector('meta[name="color-scheme"]');
        if (meta) meta.content = theme;
        const isDark = theme === 'dark';
        themeToggleBtn.innerHTML = isDark ? sunIcon : moonIcon;
        themeToggleBtn.title = isDark ? 'Usar o tema claro' : 'Usar o tema escuro';
    }

    function savedTheme() {
        try { return localStorage.getItem('theme'); } catch (e) { return null; }
    }

    themeToggleBtn.addEventListener('click', () => {
        const newTheme = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
        applyTheme(newTheme);
        try { localStorage.setItem('theme', newTheme); } catch (e) {}
    });

    // Sem tema escolhido no app, acompanha o sistema
    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', (e) => {
        if (!savedTheme()) applyTheme(e.matches ? 'dark' : 'light');
    });

    applyTheme(document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light');

    // Abrir diretório de dados
    const openDataDirBtn = document.getElementById('openDataDirBtn');
    if (openDataDirBtn) {
        openDataDirBtn.addEventListener('click', () => {
            window.go.backend.App.OpenDataDirectory().catch(e => reportError('Não foi possível abrir a pasta de dados', e));
        });
    }

    // Sobre: créditos do desenvolvedor
    const aboutDialog = document.getElementById('aboutDialog');
    document.getElementById('aboutBtn').addEventListener('click', () => {
        window.go.backend.App.GetVersion()
            .then(version => { document.getElementById('aboutVersion').textContent = version; })
            .catch(e => logError('Erro ao ler a versão do app', e));
        aboutDialog.showModal();
    });
    // O site e o e-mail abrem no navegador e no app de e-mail do sistema: o
    // webview trocaria a tela do app pelo site (e não abre mailto: sozinho)
    aboutDialog.querySelectorAll('.about-link').forEach(link => link.addEventListener('click', (e) => {
        e.preventDefault();
        window.runtime.BrowserOpenURL(e.currentTarget.href);
    }));

    // O Cancelar dos diálogos (aqui, e não num onclick no HTML: a política de
    // conteúdo da janela não deixa rodar script escrito no HTML)
    document.querySelectorAll('.dialog-cancel').forEach(btn => btn.addEventListener('click', () => {
        btn.closest('dialog').close('cancel');
    }));

    // Soltar um arquivo ou um link na janela faria o webview abri-lo no lugar
    // do app, sem ter como voltar. Arrastar texto para um campo continua valendo.
    ['dragover', 'drop'].forEach(type => window.addEventListener(type, (e) => {
        const types = e.dataTransfer ? Array.from(e.dataTransfer.types || []) : [];
        const intoField = e.target && e.target.closest && e.target.closest('input, textarea');
        if (types.includes('Files') || !intoField) e.preventDefault();
    }));

    // Configurações: tela própria no lugar da reunião aberta. Cada mudança é
    // salva na hora (IA do assistente, áudio do computador, glossário)
    const settingsBtn = document.getElementById('settingsBtn');
    const settingsView = document.getElementById('settingsView');
    const systemAudioToggle = document.getElementById('systemAudioToggle');

    settingsBtn.addEventListener('click', () => openSettings());
    document.getElementById('settingsCloseBtn').addEventListener('click', () => closeSettings());
    document.querySelectorAll('.settings-nav button').forEach(btn => {
        btn.addEventListener('click', () => scrollToSettings(btn.dataset.section));
    });

    function scrollToSettings(id) {
        const section = document.getElementById(id);
        if (section && !section.hidden) section.scrollIntoView({ behavior: 'smooth', block: 'start' });
        document.querySelectorAll('.settings-nav button').forEach(b => b.classList.toggle('active', b.dataset.section === id));
    }

    async function openSettings(section) {
        settingsOpen = true;
        updateViewMode();
        settingsView.querySelector('.settings-scroll').scrollTop = 0;
        document.querySelectorAll('.settings-nav button').forEach(b => b.classList.remove('active'));
        llmModelSelect.innerHTML = '<option value="">Carregando modelos...</option>';
        try {
            systemAudioToggle.checked = await window.go.backend.App.GetSystemAudioEnabled();
        } catch(e) {
            reportError('Não foi possível ler a configuração do áudio do computador', e);
        }
        await refreshInterpreter();
        loadWorkspaceSettings();
        await loadLLMSettings();
        if (section) scrollToSettings(section);
    }

    function closeSettings() {
        if (!settingsOpen) return;
        flushGlossary();
        flushParticipants();
        settingsOpen = false;
        updateViewMode();
    }

    function renderSettingsWorkspaceName() {
        const ws = currentWorkspace();
        settingsView.querySelectorAll('.settings-workspace').forEach(el => { el.textContent = ws ? ws.name : ''; });
    }

    // Glossário e vozes conhecidas são do workspace aberto
    async function loadWorkspaceSettings() {
        flushGlossary();
        flushParticipants();
        renderSettingsWorkspaceName();
        const workspaceID = currentWorkspaceID;
        settingsWorkspaceID = workspaceID;
        loadKnownPeople();
        loadParticipants(workspaceID);
        // O campo fica travado até o glossário deste workspace chegar: o texto
        // que estava nele é de outro e não pode ser salvo neste
        glossaryInput.readOnly = true;
        try {
            const text = await window.go.backend.App.GetGlossary(workspaceID) || '';
            if (workspaceID !== settingsWorkspaceID) return; // trocou de workspace enquanto carregava
            glossaryInput.value = text;
            glossaryInput.readOnly = false;
        } catch (e) {
            if (workspaceID !== settingsWorkspaceID) return;
            glossaryInput.value = '';
            reportError('Não foi possível carregar o glossário', e);
        }
    }

    async function loadLLMSettings() {
        try {
            llmSettings = await window.go.backend.App.GetLLMSettings();
        } catch (e) {
            reportError('Não foi possível ler a IA escolhida', e);
            llmSettings = { provider: 'ollama', ollama_model: '', agy_model: '', agy_found: false, claude_model: '', claude_found: false };
        }
        agyProviderOption.disabled = !llmSettings.agy_found;
        agyProviderHint.textContent = llmSettings.agy_found ? agyHint : 'Não encontrado neste computador. Instale o CLI do Antigravity (comando agy) para usar.';
        claudeProviderOption.disabled = !llmSettings.claude_found;
        claudeProviderHint.textContent = llmSettings.claude_found ? claudeHint : 'Não encontrado neste computador. Instale o Claude Code (comando claude) para usar.';
        providerRadios.forEach(r => { r.checked = r.value === llmSettings.provider; });
        loadModels(llmSettings.provider);
    }

    // "Salvo ✓" ao lado do título da seção, por um instante
    function flashSaved(el) {
        const flag = el.closest('.settings-section').querySelector('.saved-flag');
        if (!flag) return;
        flag.textContent = 'Salvo ✓';
        flag.classList.add('show');
        clearTimeout(flag.hideTimer);
        flag.hideTimer = setTimeout(() => flag.classList.remove('show'), 1500);
    }

    async function saveLLM(model) {
        const provider = selectedProvider();
        try {
            await window.go.backend.App.SetLLM(provider, model);
            llmSettings.provider = provider;
            if (model) llmSettings[`${provider}_model`] = model;
            renderStatusBar(provider);
            flashSaved(llmModelSelect);
        } catch (e) {
            alert('Não foi possível trocar a IA do assistente: ' + e);
            providerRadios.forEach(r => { r.checked = r.value === llmSettings.provider; });
            loadModels(llmSettings.provider);
        }
    }

    systemAudioToggle.addEventListener('change', async () => {
        try {
            await window.go.backend.App.SetSystemAudioEnabled(systemAudioToggle.checked);
            flashSaved(systemAudioToggle);
        } catch (e) {
            alert("Não foi possível capturar o áudio do computador: " + e);
            systemAudioToggle.checked = !systemAudioToggle.checked;
        }
    });

    // IA do assistente: o Ollama, local, ou o agy, na nuvem; cada uma com os seus modelos
    const llmModelSelect = document.getElementById('llmModelSelect');
    const providerRadios = document.querySelectorAll('input[name="llmProvider"]');
    const agyProviderOption = document.getElementById('agyProviderOption');
    const agyProviderHint = document.getElementById('agyProviderHint');
    const agyHint = agyProviderHint.textContent;
    const claudeProviderOption = document.getElementById('claudeProviderOption');
    const claudeProviderHint = document.getElementById('claudeProviderHint');
    const claudeHint = claudeProviderHint.textContent;
    let llmSettings = null;

    function selectedProvider() {
        const checked = document.querySelector('input[name="llmProvider"]:checked');
        return checked ? checked.value : 'ollama';
    }

    async function loadModels(provider) {
        llmModelSelect.innerHTML = '<option value="">Carregando modelos...</option>';
        let models = [];
        let error = '';
        try {
            if (provider === 'agy') {
                models = (await window.go.backend.App.GetAgyModels() || []).map(m => ({ value: m.id, text: m.name || m.id }));
            } else if (provider === 'claude') {
                models = (await window.go.backend.App.GetClaudeModels() || []).map(m => ({ value: m.id, text: m.name || m.id }));
            } else {
                models = (await window.go.backend.App.GetOllamaModels() || []).map(m => ({ value: m, text: m }));
            }
        } catch (e) {
            error = provider === 'ollama' ? 'Erro ao buscar modelos (Ollama ativo?)' : `Erro ao buscar os modelos do ${provider}`;
        }
        if (selectedProvider() !== provider) return; // trocou de IA enquanto carregava
        const current = llmSettings[`${provider}_model`];
        // O modelo escolhido aparece mesmo se a lista não vier (ex: Ollama parado)
        if (current && !models.some(m => m.value === current)) models.unshift({ value: current, text: current });
        llmModelSelect.innerHTML = '';
        if (models.length === 0) {
            llmModelSelect.add(new Option(error || 'Nenhum modelo local encontrado', ''));
            return;
        }
        models.forEach(m => llmModelSelect.add(new Option(m.text, m.value, false, m.value === current)));
    }

    providerRadios.forEach(r => r.addEventListener('change', () => {
        loadModels(r.value);
        saveLLM(''); // mantém o modelo que já estava escolhido nessa IA
    }));
    llmModelSelect.addEventListener('change', () => { if (llmModelSelect.value) saveLLM(llmModelSelect.value); });

    // Vozes conhecidas: pessoas nomeadas na transcrição, reconhecidas nas próximas reuniões
    const knownPeopleList = document.getElementById('knownPeopleList');
    const glossaryInput = document.getElementById('glossaryInput');

    // Workspace do glossário e das vozes mostrados nas configurações
    let settingsWorkspaceID = '';

    async function loadKnownPeople() {
        const workspaceID = settingsWorkspaceID;
        let people = [];
        try {
            people = await window.go.backend.App.GetKnownPeople(workspaceID) || [];
        } catch (e) {
            reportError('Não foi possível carregar as vozes conhecidas', e);
        }
        // Trocou de workspace enquanto carregava: a lista é do outro, e os
        // botões dela agiriam no workspace errado
        if (workspaceID !== settingsWorkspaceID) return;
        knownPeopleList.innerHTML = '';
        if (people.length === 0) {
            const empty = document.createElement('li');
            empty.className = 'known-people-empty';
            empty.textContent = 'Nenhuma ainda. Clique em quem falou na transcrição para dizer quem é.';
            knownPeopleList.append(empty);
            return;
        }
        people.forEach(name => {
            const li = document.createElement('li');
            const label = document.createElement('span');
            label.textContent = name;
            const rename = document.createElement('button');
            rename.type = 'button';
            rename.className = 'secondary-btn';
            rename.textContent = 'Renomear';
            rename.title = 'Corrige o nome da pessoa, também nas falas e tarefas das reuniões deste workspace';
            rename.addEventListener('click', () => renamePerson(workspaceID, name, people));
            const forget = document.createElement('button');
            forget.type = 'button';
            forget.className = 'secondary-btn';
            forget.textContent = 'Esquecer';
            forget.title = 'Apaga a voz guardada. As transcrições já feitas continuam com o nome.';
            forget.addEventListener('click', async () => {
                const ok = await showConfirm(`Esquecer a voz de "${name}"?`,
                    'O Recorder deixa de reconhecer essa pessoa nas próximas reuniões deste workspace. As transcrições já feitas continuam com o nome.', 'Esquecer');
                if (!ok) return;
                try {
                    await window.go.backend.App.ForgetPerson(workspaceID, name);
                    loadKnownPeople();
                } catch (e) {
                    reportError('Não foi possível esquecer a voz', e);
                }
            });
            const actions = document.createElement('div');
            actions.className = 'known-people-actions';
            actions.append(rename, forget);
            li.append(label, actions);
            knownPeopleList.append(li);
        });
    }

    // Corrige o nome de uma voz conhecida (ex: "Diego" que na verdade é "Diogo")
    async function renamePerson(workspaceID, name, people) {
        const newName = await showPrompt(
            'Renomear voz',
            `Novo nome para "${name}". As falas e as tarefas das reuniões deste workspace que usam "${name}" também mudam.`,
            name, false, 'Renomear'
        );
        if (!newName || newName === name) return;
        if (/^Pessoa \d+$/.test(newName)) {
            alert('Use o nome da pessoa, e não um rótulo automático como "Pessoa 1".');
            return;
        }
        if (people.includes(newName)) {
            const merge = await showConfirm('Juntar as vozes?', `Já existe uma voz guardada como "${newName}". Se forem a mesma pessoa, as duas viram uma só.`, 'Juntar', false);
            if (!merge) return;
        }
        try {
            await window.go.backend.App.RenamePerson(workspaceID, name, newName);
        } catch (e) {
            alert('Não foi possível renomear a voz: ' + e);
        }
        loadKnownPeople();
    }

    let glossaryTimer = null;
    let glossaryDirty = false;

    function flushGlossary() {
        clearTimeout(glossaryTimer);
        if (!glossaryDirty) return;
        glossaryDirty = false;
        const workspaceID = settingsWorkspaceID, text = glossaryInput.value;
        window.go.backend.App.SetGlossary(workspaceID, text)
            .then(() => flashSaved(glossaryInput))
            .catch(e => {
                reportError('Não foi possível salvar o glossário', e);
                // O texto continua no campo: tenta de novo na próxima vez
                if (workspaceID === settingsWorkspaceID) glossaryDirty = true;
            });
    }

    glossaryInput.addEventListener('input', () => {
        glossaryDirty = true;
        clearTimeout(glossaryTimer);
        glossaryTimer = setTimeout(flushGlossary, 800);
    });
    glossaryInput.addEventListener('blur', flushGlossary);

    // Participantes do workspace: salvos do mesmo jeito que o glossário
    const participantsInput = document.getElementById('participantsInput');
    let participantsTimer = null;
    let participantsDirty = false;

    async function loadParticipants(workspaceID) {
        // Travado até a lista deste workspace chegar: o texto que estava no
        // campo é de outro e não pode ser salvo neste
        participantsInput.readOnly = true;
        try {
            const text = await window.go.backend.App.GetParticipants(workspaceID) || '';
            if (workspaceID !== settingsWorkspaceID) return; // trocou de workspace enquanto carregava
            participantsInput.value = text;
            participantsInput.readOnly = false;
        } catch (e) {
            if (workspaceID !== settingsWorkspaceID) return;
            participantsInput.value = '';
            reportError('Não foi possível carregar os participantes', e);
        }
    }

    function flushParticipants() {
        clearTimeout(participantsTimer);
        if (!participantsDirty) return;
        participantsDirty = false;
        const workspaceID = settingsWorkspaceID, text = participantsInput.value;
        window.go.backend.App.SetParticipants(workspaceID, text)
            .then(() => flashSaved(participantsInput))
            .catch(e => {
                reportError('Não foi possível salvar os participantes', e);
                // O texto continua no campo: tenta de novo na próxima vez
                if (workspaceID === settingsWorkspaceID) participantsDirty = true;
            });
    }

    participantsInput.addEventListener('input', () => {
        participantsDirty = true;
        clearTimeout(participantsTimer);
        participantsTimer = setTimeout(flushParticipants, 800);
    });
    participantsInput.addEventListener('blur', flushParticipants);

    // --- Intérprete: segurando o botão ou ⌃⌥Espaço, a reunião ouve em inglês,
    // com a sua voz, o que você diz em português ---
    const interpreterPanel = document.getElementById('interpreterPanel');
    const interpreterToggle = document.getElementById('interpreterToggle');
    const pttBtn = document.getElementById('pttBtn');
    const interpreterState = document.getElementById('interpreterState');
    const interpreterSettings = document.getElementById('interpreterSettings');
    let interpreterInfo = null;

    async function refreshInterpreter() {
        try {
            interpreterInfo = await window.go.backend.App.GetInterpreterInfo();
        } catch (e) {
            logError('Erro ao ler o Intérprete', e);
            return;
        }
        renderInterpreter();
    }

    // O que está faltando para ligar, em ordem
    function interpreterMissing(info) {
        if (!info.virtual_mic) return info.virtual_mic_driver ? 'O macOS ainda não carregou o BlackHole' : 'Falta o microfone virtual (BlackHole)';
        if (!info.voice_installed) return 'Falta instalar a voz';
        if (!info.sample_recorded) return 'Falta calibrar a sua voz';
        return '';
    }

    function voiceLoadingText(voice) {
        if (voice.state === 'baixando') return `Baixando o modelo de voz (${voice.progress}%)... só na primeira vez`;
        if (voice.state === 'carregando') return 'Carregando a sua voz...';
        if (voice.state === 'erro') return 'Erro na voz: ' + voice.error;
        return '';
    }

    function renderInterpreter() {
        const info = interpreterInfo;
        if (!info || !info.supported) {
            interpreterPanel.hidden = true;
            interpreterSettings.hidden = true;
            document.getElementById('settingsNavInterpreter').hidden = true;
            return;
        }
        interpreterPanel.hidden = false;
        interpreterSettings.hidden = false;
        document.getElementById('settingsNavInterpreter').hidden = false;
        interpreterToggle.checked = info.enabled;
        // Ligar exige uma reunião aberta; desligar, sempre pode
        interpreterToggle.disabled = !info.enabled && !currentSessionID;
        document.querySelectorAll('.ptt-shortcut').forEach(el => { el.textContent = info.shortcut; });
        pttBtn.hidden = !info.enabled;
        pttBtn.title = `Segure (ou segure ${info.shortcut}) enquanto fala em português`;
        // Com a voz em erro o botão continua valendo: apertar avisa e sobe a voz de novo
        pttBtn.disabled = (info.enabled && info.voice.state !== 'pronto' && info.voice.state !== 'erro') || !currentSessionID;
        renderInterpreterState(info.status);
        renderInterpreterChecklist(info);
        renderCorrection(info);
    }

    // Quem corrige a tradução do Whisper: o Claude Haiku, o Ollama ou ninguém
    const correctionRadios = document.querySelectorAll('input[name="interpreterCorrection"]');
    const correctionClaudeOption = document.getElementById('correctionClaudeOption');
    const correctionClaudeHint = document.getElementById('correctionClaudeHint');
    const correctionClaudeText = correctionClaudeHint.textContent;

    function renderCorrection(info) {
        correctionRadios.forEach(r => { r.checked = r.value === info.correction; });
        correctionClaudeOption.disabled = !info.claude_found;
        correctionClaudeHint.textContent = info.claude_found ? correctionClaudeText
            : 'O claude (Claude Code) não foi encontrado neste computador.';
    }

    correctionRadios.forEach(r => r.addEventListener('change', async () => {
        try {
            await window.go.backend.App.SetInterpreterCorrection(r.value);
            if (interpreterInfo) interpreterInfo.correction = r.value;
            flashSaved(r);
        } catch (e) {
            alert('Não foi possível mudar a correção: ' + e);
            if (interpreterInfo) renderCorrection(interpreterInfo);
        }
    }));

    function renderInterpreterState(status) {
        const info = interpreterInfo;
        if (!info) return;
        if (status) info.status = status;
        interpreterState.classList.remove('error');
        interpreterState.textContent = '';
        pttBtn.classList.toggle('active', !!(info.status && info.status.state === 'ouvindo'));
        if (!info.enabled) {
            const missing = interpreterMissing(info);
            interpreterState.textContent = missing ? `${missing}: clique em configurar.`
                : !currentSessionID ? 'Abra ou crie uma reunião para usar o Intérprete.'
                : `Desligado. No Meet, o microfone deve ser o ${info.virtual_mic}.`;
            return;
        }
        if (!currentSessionID) {
            interpreterState.textContent = 'Abra uma reunião para traduzir. Enquanto isso, o Meet ouve a sua voz normal.';
            return;
        }
        const loading = voiceLoadingText(info.voice);
        if (loading) {
            interpreterState.textContent = loading;
            interpreterState.classList.toggle('error', info.voice.state === 'erro');
            return;
        }
        const s = info.status || { state: 'pronto' };
        const texts = {
            ouvindo: 'Ouvindo o seu português... a reunião não ouve você.',
            traduzindo: 'Traduzindo...',
            falando: 'A reunião está ouvindo o inglês.',
            pronto: `Pronto. Segure ${info.shortcut} para falar em inglês.`,
        };
        interpreterState.textContent = texts[s.state] || texts.pronto;
        if (s.message) {
            // O aviso do último aperto (por que não virou inglês, ou quem corrigiu)
            const msg = document.createElement('span');
            msg.className = 'state-message';
            msg.textContent = s.message;
            interpreterState.append(msg);
        }
        if (s.en && s.state !== 'ouvindo') {
            const last = document.createElement('span');
            last.className = 'last-en';
            last.textContent = `“${s.en}”` + (s.delay > 0 ? ` (${s.delay.toFixed(1).replace('.', ',')} s)` : '');
            last.title = `Você disse: ${s.pt}` + (s.literal ? `\nWhisper, antes da correção: ${s.literal}` : '');
            interpreterState.append(last);
        }
    }

    function checkItem(li, ok, title, detail, action) {
        li.innerHTML = '';
        const icon = document.createElement('span');
        icon.className = 'chk-icon ' + (ok ? 'ok' : 'missing');
        icon.textContent = ok ? '✓' : '✗';
        const body = document.createElement('span');
        body.className = 'chk-body';
        body.append(title);
        if (detail) {
            const small = document.createElement('small');
            if (detail instanceof Node) small.append(detail); else small.textContent = detail;
            body.append(small);
        }
        li.append(icon, body);
        if (action) {
            action.classList.add('chk-action');
            li.append(action);
        }
    }

    function code(text) {
        const el = document.createElement('code');
        el.textContent = text;
        return el;
    }

    function actionButton(label, onClick) {
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'secondary-btn';
        btn.textContent = label;
        btn.addEventListener('click', onClick);
        return btn;
    }

    function renderInterpreterChecklist(info) {
        // Microfone virtual
        if (info.virtual_mic) {
            checkItem(document.getElementById('chkVirtualMic'), true, `Microfone virtual: ${info.virtual_mic}`);
        } else if (info.virtual_mic_driver) {
            // Instalado agora há pouco: o macOS só carrega o driver ao reiniciar o serviço de áudio
            const detail = document.createElement('span');
            detail.append('Instalado, mas o macOS ainda não o carregou. No Terminal (pede a sua senha; o som some por um segundo): ', code('sudo killall coreaudiod'), ' (ou reinicie o Mac).');
            checkItem(document.getElementById('chkVirtualMic'), false, 'Microfone virtual (BlackHole)', detail,
                actionButton('Verificar', refreshInterpreter));
        } else {
            const detail = document.createElement('span');
            detail.append('Instale no Terminal (pede a sua senha): ', code('brew install blackhole-2ch'));
            checkItem(document.getElementById('chkVirtualMic'), false, 'Microfone virtual (BlackHole)', detail,
                actionButton('Verificar', refreshInterpreter));
        }
        // Voz
        const chkVoice = document.getElementById('chkVoice');
        if (info.installing) {
            checkItem(chkVoice, false, 'Voz: instalando...', info.installing);
        } else if (info.voice_installed) {
            checkItem(chkVoice, true, 'Voz instalada', info.model_downloaded
                ? 'Modelo de voz no disco (Qwen3-TTS 1.7B).'
                : 'O modelo de voz (~4 GB) é baixado na primeira vez que o Intérprete for ligado.');
        } else if (!info.uv_found) {
            const detail = document.createElement('span');
            detail.append('Precisa do uv para instalar. No Terminal: ', code('brew install uv'));
            checkItem(chkVoice, false, 'Voz', detail, actionButton('Verificar', refreshInterpreter));
        } else {
            checkItem(chkVoice, false, 'Voz', info.install_error || 'Instala o ambiente da voz em ~/.recorder/voz (uns 500 MB, mais o modelo depois).',
                actionButton('Instalar', async () => {
                    try {
                        await window.go.backend.App.InstallInterpreterVoice();
                    } catch (e) {
                        alert('Não foi possível instalar a voz: ' + e);
                    }
                }));
        }
        // Calibração da voz
        checkItem(document.getElementById('chkSample'), info.sample_recorded,
            info.sample_recorded ? 'Voz calibrada' : 'Calibrar a sua voz (uns 2 minutos lendo um texto em inglês)',
            info.sample_recorded ? `Imitando o trecho: “${info.sample_text}”` : 'Você escolhe, de ouvido, a voz que mais parece com a sua.',
            actionButton(info.sample_recorded ? 'Calibrar de novo' : 'Calibrar', openSampleDialog));
    }

    interpreterToggle.addEventListener('change', async () => {
        const on = interpreterToggle.checked;
        interpreterToggle.disabled = true;
        try {
            await window.go.backend.App.SetInterpreterEnabled(on);
        } catch (e) {
            alert('Não foi possível ligar o Intérprete: ' + e);
        } finally {
            interpreterToggle.disabled = false;
            await refreshInterpreter();
        }
    });

    document.getElementById('interpreterSetupBtn').addEventListener('click', () => openSettings('interpreterSettings'));

    // Segurar o botão: fala enquanto estiver apertado
    let pttHeld = false;
    const pttDown = (e) => {
        e.preventDefault();
        if (pttHeld || pttBtn.disabled) return;
        pttHeld = true;
        pttBtn.classList.add('active');
        window.go.backend.App.InterpreterTalk(true).catch(e => reportError('O Intérprete não respondeu', e));
    };
    const pttUp = () => {
        if (!pttHeld) return;
        pttHeld = false;
        window.go.backend.App.InterpreterTalk(false).catch(e => reportError('O Intérprete não respondeu', e));
    };
    pttBtn.addEventListener('pointerdown', pttDown);
    pttBtn.addEventListener('pointerup', pttUp);
    pttBtn.addEventListener('pointerleave', pttUp);
    pttBtn.addEventListener('pointercancel', pttUp);
    window.addEventListener('blur', pttUp);

    // Calibração da voz: grava uns minutos, o Recorder separa os melhores
    // trechos e gera a mesma frase com cada um; você escolhe a que mais parece com você
    const sampleDialog = document.getElementById('sampleDialog');
    const sampleRecordBtn = document.getElementById('sampleRecordBtn');
    const sampleStatus = document.getElementById('sampleStatus');
    const calibRecordStep = document.getElementById('calibRecordStep');
    const calibChooseStep = document.getElementById('calibChooseStep');
    const calibOptions = document.getElementById('calibOptions');
    const calibPlayer = document.getElementById('calibPlayer');
    let sampleRecording = false;
    let sampleTimer = null;

    function setCalibStatus(text, error = false) {
        sampleStatus.textContent = text;
        sampleStatus.classList.toggle('error', error);
    }

    function openSampleDialog() {
        document.getElementById('sampleReading').textContent = interpreterInfo ? interpreterInfo.reading_text : '';
        calibRecordStep.hidden = false;
        calibChooseStep.hidden = true;
        setCalibStatus('');
        sampleRecordBtn.textContent = 'Começar a gravar';
        sampleRecordBtn.disabled = false;
        sampleRecording = false;
        sampleDialog.showModal();
        // A última calibração fica salva: dá para ouvir e escolher de novo sem gravar
        calibLastBtn.hidden = true;
        window.go.backend.App.GetVoiceCalibration().then(state => {
            lastCalibration = state;
            calibLastBtn.hidden = !(state && state.options && state.options.length > 0);
            // Uma calibração ainda sendo processada (só pode haver uma por vez):
            // o diálogo reaberto mostra o andamento em vez de oferecer gravar outra
            if (state && state.stage === 'processando') renderCalibration(state);
        }).catch(e => logError('Erro ao ler a última calibração da voz', e));
    }

    const calibLastBtn = document.getElementById('calibLastBtn');
    let lastCalibration = null;
    calibLastBtn.addEventListener('click', () => {
        if (lastCalibration) renderCalibration({ ...lastCalibration, stage: 'pronto' });
    });

    function stopSampleTimer() {
        clearInterval(sampleTimer);
        sampleTimer = null;
    }

    function playCalib(url) {
        calibPlayer.src = url + '?v=' + Date.now();
        calibPlayer.play().catch(e => logError('Erro ao tocar a amostra da voz', e));
    }

    function renderCalibration(state) {
        if (!sampleDialog.open) return;
        if (state.stage === 'processando') {
            sampleRecordBtn.disabled = true;
            setCalibStatus(state.message || 'Preparando as opções...');
            return;
        }
        if (state.stage === 'erro') {
            sampleRecordBtn.disabled = false;
            sampleRecordBtn.textContent = 'Gravar de novo';
            setCalibStatus(state.message, true);
            return;
        }
        if (state.stage !== 'pronto') return;
        sampleRecordBtn.disabled = false;
        sampleRecordBtn.textContent = 'Gravar de novo';
        calibRecordStep.hidden = true;
        calibChooseStep.hidden = false;
        setCalibStatus('');
        calibOptions.innerHTML = '';
        state.options.forEach(o => {
            const li = document.createElement('li');
            const head = document.createElement('div');
            head.className = 'calib-head';
            const label = document.createElement('strong');
            label.textContent = o.label;
            const listen = document.createElement('button');
            listen.type = 'button';
            listen.className = 'secondary-btn';
            listen.textContent = '▶ Ouvir em inglês';
            listen.addEventListener('click', () => playCalib(state.base_url + o.preview));
            const use = document.createElement('button');
            use.type = 'button';
            use.className = 'primary-btn';
            use.textContent = o.id === 'atual' ? 'Manter esta' : 'Usar esta voz';
            use.addEventListener('click', async () => {
                try {
                    await window.go.backend.App.ChooseVoiceOption(o.id);
                } catch (e) {
                    setCalibStatus('Não foi possível usar esta voz: ' + e, true);
                    return;
                }
                calibOptions.querySelectorAll('li').forEach(x => x.classList.remove('chosen'));
                li.classList.add('chosen');
                setCalibStatus(`${o.label} escolhida: o Intérprete passa a falar com ela.`);
                refreshInterpreter();
            });
            head.append(label, listen, use);
            const detail = document.createElement('small');
            detail.className = 'calib-detail';
            const ref = document.createElement('button');
            ref.type = 'button';
            ref.className = 'time-link';
            ref.textContent = o.id === 'atual' ? 'amostra atual' : `seu trecho de ${Math.round(o.seconds)} s`;
            ref.title = 'Ouvir o trecho original da sua voz';
            ref.addEventListener('click', () => playCalib(state.base_url + o.reference));
            detail.append('Imitando o ', ref, `: “${o.text}”`);
            li.append(head, detail);
            calibOptions.append(li);
        });
    }

    sampleRecordBtn.addEventListener('click', async () => {
        if (!sampleRecording) {
            calibPlayer.pause();
            try {
                await window.go.backend.App.StartVoiceCalibration();
            } catch (e) {
                setCalibStatus('Não foi possível gravar: ' + e, true);
                return;
            }
            sampleRecording = true;
            calibRecordStep.hidden = false;
            calibChooseStep.hidden = true;
            sampleRecordBtn.textContent = 'Parar e gerar as opções';
            const started = Date.now();
            const tick = () => {
                const secs = Math.floor((Date.now() - started) / 1000);
                const hint = secs < 20 ? ' (mínimo 20 s)' : secs < 120 ? ' (o ideal são uns 2 minutos)' : '';
                setCalibStatus(`● Gravando... ${Math.floor(secs / 60)}:${String(secs % 60).padStart(2, '0')}${hint}`);
            };
            tick();
            sampleTimer = setInterval(tick, 500);
            return;
        }
        sampleRecording = false;
        stopSampleTimer();
        sampleRecordBtn.disabled = true;
        setCalibStatus('Preparando as opções...');
        try {
            await window.go.backend.App.FinishVoiceCalibration();
        } catch (e) {
            setCalibStatus(String(e), true);
            sampleRecordBtn.disabled = false;
            sampleRecordBtn.textContent = 'Gravar de novo';
        }
    });

    document.getElementById('sampleCancelBtn').addEventListener('click', () => sampleDialog.close());
    sampleDialog.addEventListener('close', () => {
        stopSampleTimer();
        calibPlayer.pause();
        if (sampleRecording) {
            sampleRecording = false;
            window.go.backend.App.CancelVoiceCalibration().catch(e => logError('Erro ao cancelar a calibração da voz', e));
        }
    });

    if (window.runtime) {
        window.runtime.EventsOn('interpreter-info', (info) => { interpreterInfo = info; renderInterpreter(); });
        window.runtime.EventsOn('interpreter-status', (status) => renderInterpreterState(status));
        window.runtime.EventsOn('voice-calibration', (state) => renderCalibration(state));
    }
    refreshInterpreter();

    init();
});
