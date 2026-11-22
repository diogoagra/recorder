//go:build darwin

package mac

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#import <stdlib.h>

void setDockBadge(const char* badgeStr) {
    @autoreleasepool {
        NSString *badge = nil;
        if (badgeStr != NULL) {
            badge = [NSString stringWithUTF8String:badgeStr];
        }
        dispatch_async(dispatch_get_main_queue(), ^{
            [[[NSApplication sharedApplication] dockTile] setBadgeLabel:badge];
        });
    }
}
*/
import "C"
import "unsafe"

func SetBadge(badge string) {
	if badge == "" {
		C.setDockBadge(nil)
	} else {
		cStr := C.CString(badge)
		C.setDockBadge(cStr)
		C.free(unsafe.Pointer(cStr))
	}
}
