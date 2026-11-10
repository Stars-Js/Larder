//go:build windows

package desktop

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32BG = windows.NewLazySystemDLL("user32.dll")
	gdi32BG  = windows.NewLazySystemDLL("gdi32.dll")

	procSetClassLongPtrW = user32BG.NewProc("SetClassLongPtrW")
	procCreateSolidBrush = gdi32BG.NewProc("CreateSolidBrush")
	procEnumChildWindows = user32BG.NewProc("EnumChildWindows")
	procGetClientRect    = user32BG.NewProc("GetClientRect")
	procFillRect         = user32BG.NewProc("FillRect")

	windowBackgroundBrush uintptr
)

type winRect struct {
	left, top, right, bottom int32
}

// setWindowBackground 把窗口及其子窗口类背景设为应用深色，
// 避免 WebView2 在快速缩放、重绘时露出白色底。
func setWindowBackground(hwnd uintptr) {
	if hwnd == 0 || windowBackgroundBrush != 0 {
		return
	}
	// COLORREF: 0x00BBGGRR；#17141f -> 0x001f1417。
	brush, _, _ := procCreateSolidBrush.Call(0x001f1417)
	if brush == 0 {
		return
	}
	windowBackgroundBrush = brush
	// GCLP_HBRBACKGROUND = -10。
	_, _, _ = procSetClassLongPtrW.Call(hwnd, ^uintptr(9), brush)

	cb := windows.NewCallback(func(child, _ uintptr) uintptr {
		_, _, _ = procSetClassLongPtrW.Call(child, ^uintptr(9), brush)
		return 1
	})
	_, _, _ = procEnumChildWindows.Call(hwnd, cb, 0)
}

// eraseWindowBackground 在 WM_ERASEBKGND 时直接填深色。
func eraseWindowBackground(hwnd, hdc uintptr) bool {
	if windowBackgroundBrush == 0 || hdc == 0 {
		return false
	}
	var rc winRect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	_, _, _ = procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), windowBackgroundBrush)
	return true
}
