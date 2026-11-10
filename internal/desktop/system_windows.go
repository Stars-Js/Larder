//go:build windows

package desktop

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")

	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	procGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procSystemParametersInfoW         = user32.NewProc("SystemParametersInfoW")
	procMessageBoxW                   = user32.NewProc("MessageBoxW")
)

type rect struct {
	Left, Top, Right, Bottom int32
}

// WorkArea 返回主显示器工作区（不含任务栏）的物理像素尺寸。
func WorkArea() (width, height int) {
	const spiGetWorkArea = 0x0030
	var r rect
	if ret, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0); ret != 0 {
		if r.Right > r.Left && r.Bottom > r.Top {
			return int(r.Right - r.Left), int(r.Bottom - r.Top)
		}
	}
	w, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	h, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	return int(w), int(h)
}

// Init 在创建任何窗口前把进程标记为 Per-Monitor DPI 感知。
// 不这样做时 Windows 会把整个窗口位图拉伸到系统缩放比例，界面会发虚。
func Init() {
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	const perMonitorV2 = ^uintptr(3)
	if r, _, _ := procSetProcessDpiAwarenessContext.Call(perMonitorV2); r != 0 {
		return
	}
	// Windows 10 1703 之前没有该 API，退回系统级 DPI 感知。
	_, _, _ = procSetProcessDPIAware.Call()
}

// Scale 把按 96 DPI 设计的逻辑尺寸换算成当前系统的物理像素。
func Scale(n int) int {
	dpi := 96
	if r, _, _ := procGetDpiForSystem.Call(); r > 0 {
		dpi = int(r)
	}
	return n * dpi / 96
}

// MessageBox 弹出一个原生提示框（无控制台时用于显示启动错误）。
func MessageBox(title, text string) {
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(text)
	const (
		mbOK            = 0x00000000
		mbIconError     = 0x00000010
		mbSetForeground = 0x00010000
	)
	_, _, _ = procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(m)),
		uintptr(unsafe.Pointer(t)),
		uintptr(mbOK|mbIconError|mbSetForeground))
}
