//go:build darwin

package mac

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Carbon -framework Cocoa
#import <Carbon/Carbon.h>
#import <Cocoa/Cocoa.h>

extern void goHotKey(int id, int pressed);

// Atalhos globais do Recorder, pelo id do EventHotKeyID
enum { hotKeyPushToTalk = 1, hotKeyCapture = 2, hotKeyCount = 3 };

static EventHotKeyRef hotKeyRefs[hotKeyCount] = {NULL};
static BOOL hotKeyHandlerInstalled = NO;

static OSStatus hotKeyHandler(EventHandlerCallRef next, EventRef event, void *data) {
    EventHotKeyID hotKeyID;
    if (GetEventParameter(event, kEventParamDirectObject, typeEventHotKeyID, NULL,
                          sizeof(hotKeyID), NULL, &hotKeyID) != noErr) {
        return eventNotHandledErr;
    }
    goHotKey((int)hotKeyID.id, GetEventKind(event) == kEventHotKeyPressed ? 1 : 0);
    return noErr;
}

// setHotKey liga ou desliga um atalho global (⌃⌥ + a tecla keyCode).
// RegisterEventHotKey funciona com outro app em primeiro plano e não pede a
// permissão de Acessibilidade; o registro precisa ser feito na thread principal.
static void setHotKey(int id, int keyCode, const char *name, int on) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (!on) {
            if (hotKeyRefs[id] != NULL) {
                UnregisterEventHotKey(hotKeyRefs[id]);
                hotKeyRefs[id] = NULL;
            }
            return;
        }
        if (hotKeyRefs[id] != NULL) {
            return;
        }
        if (!hotKeyHandlerInstalled) {
            EventTypeSpec types[2] = {
                {kEventClassKeyboard, kEventHotKeyPressed},
                {kEventClassKeyboard, kEventHotKeyReleased},
            };
            InstallApplicationEventHandler(&hotKeyHandler, 2, types, NULL, NULL);
            hotKeyHandlerInstalled = YES;
        }
        EventHotKeyID hotKeyID = {'RCDR', id};
        OSStatus err = RegisterEventHotKey(keyCode, controlKey | optionKey, hotKeyID,
                                           GetApplicationEventTarget(), 0, &hotKeyRefs[id]);
        if (err != noErr) {
            NSLog(@"Recorder: não foi possível registrar o atalho %s (%d)", name, (int)err);
            hotKeyRefs[id] = NULL;
        }
    });
}

// ⌃⌥Espaço: o Intérprete, enquanto a tecla estiver apertada
static void setPushToTalk(int on) {
    setHotKey(hotKeyPushToTalk, kVK_Space, "⌃⌥Espaço", on);
}

// ⌃⌥S: capturar a tela para a reunião aberta
static void setCaptureShortcut(int on) {
    setHotKey(hotKeyCapture, kVK_ANSI_S, "⌃⌥S", on);
}
*/
import "C"

import "sync"

// PushToTalkShortcut é o atalho global do Intérprete.
const PushToTalkShortcut = "⌃⌥Espaço"

var (
	pttOnce   sync.Once
	pttEvents = make(chan bool, 64)
)

// EnablePushToTalk liga (on) ou desliga o atalho global ⌃⌥Espaço. Ligado,
// onChange é chamado com true ao apertar e false ao soltar, mesmo com outro
// app (ex: o Meet) em primeiro plano; as chamadas vêm em ordem, de uma
// goroutine só. Desligado, a combinação volta a ser dos outros apps.
func EnablePushToTalk(on bool, onChange func(pressed bool)) {
	pttOnce.Do(func() {
		go func() {
			for pressed := range pttEvents {
				pttMu.Lock()
				cb := pttCallback
				pttMu.Unlock()
				if cb != nil {
					cb(pressed)
				}
			}
		}()
	})
	pttMu.Lock()
	pttCallback = onChange
	pttMu.Unlock()
	if on {
		C.setPushToTalk(1)
	} else {
		C.setPushToTalk(0)
	}
}

var (
	pttMu       sync.Mutex
	pttCallback func(pressed bool)
)

// CaptureShortcut é o atalho global que captura a tela para a reunião aberta.
const CaptureShortcut = "⌃⌥S"

var (
	captureMu       sync.Mutex
	captureCallback func()
)

// EnableCaptureShortcut liga (on) ou desliga o atalho global ⌃⌥S. Ligado,
// onPress é chamado a cada vez que o atalho é apertado, mesmo com outro app
// (ex: o Meet) em primeiro plano. Desligado, a combinação volta a ser dos
// outros apps.
func EnableCaptureShortcut(on bool, onPress func()) {
	captureMu.Lock()
	captureCallback = onPress
	captureMu.Unlock()
	if on {
		C.setCaptureShortcut(1)
	} else {
		C.setCaptureShortcut(0)
	}
}

// goHotKey é chamado pelo macOS, na thread principal, ao apertar ou soltar um
// dos atalhos: não pode demorar.
//
//export goHotKey
func goHotKey(id, pressed C.int) {
	switch id {
	case C.hotKeyPushToTalk:
		select {
		case pttEvents <- pressed == 1:
		default: // fila cheia: o atalho repetindo sem ninguém consumir
		}
	case C.hotKeyCapture:
		if pressed != 1 {
			return
		}
		captureMu.Lock()
		cb := captureCallback
		captureMu.Unlock()
		if cb != nil {
			go cb()
		}
	}
}
