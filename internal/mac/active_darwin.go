//go:build darwin

package mac

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// NSRunningApplication pode ser consultado de qualquer thread
static int appIsActive(void) {
    @autoreleasepool {
        return [[NSRunningApplication currentApplication] isActive] ? 1 : 0;
    }
}
*/
import "C"

// IsActive diz se o Recorder é o app em primeiro plano.
func IsActive() bool {
	return C.appIsActive() == 1
}
