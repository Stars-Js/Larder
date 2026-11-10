//go:build windows

package desktop

import (
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// DWM 窗口属性常量（见 dwmapi.h）。
const (
	dwmwaUseImmersiveDarkMode    = 20 // Win10 2004+
	dwmwaUseImmersiveDarkModeOld = 19 // 早期 Win10
	dwmwaBorderColor             = 34 // Win11+
	dwmwaCaptionColor            = 35 // Win11+
	dwmwaTextColor               = 36 // Win11+
)

var (
	dwmapi = windows.NewLazySystemDLL("dwmapi.dll")

	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

// 与前端 app.css 中 --bg / --fg 保持一致，让标题栏与页面融为一体。
var (
	lightCaption = rgb(0xee, 0xea, 0xf5) // --bg（浅色）
	lightText    = rgb(0x2a, 0x24, 0x35) // --fg（浅色）
	darkCaption  = rgb(0x17, 0x14, 0x1f) // --bg（深色）
	darkText     = rgb(0xf2, 0xee, 0xf8) // --fg（深色）
)

// rgb 把 RGB 组装为 COLORREF（0x00BBGGRR）。
func rgb(r, g, b uint32) uint32 { return r | g<<8 | b<<16 }

func dwmSet(hwnd uintptr, attr uint32, value unsafe.Pointer, size uintptr) bool {
	r, _, _ := procDwmSetWindowAttribute.Call(hwnd, uintptr(attr), uintptr(value), size)
	return r == 0 // S_OK
}

// isDarkMode 读取系统“应用使用深色模式”设置（与 magpie 的判定一致）。
func isDarkMode() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return false
	}
	return v == 0 // 0 = 深色
}

// tintTitleBar 把原生标题栏涂成页面自身的背景色，并让标题文字与之协调，
// 效果与 magpie 在 Windows 上的标题栏一致：栏与页面读作同一块表面。
func tintTitleBar(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	dark := isDarkMode()

	// 深色/浅色沉浸式标题栏（决定最小化/最大化/关闭按钮图标明暗）。
	var immersive int32
	if dark {
		immersive = 1
	}
	if !dwmSet(hwnd, dwmwaUseImmersiveDarkMode, unsafe.Pointer(&immersive), unsafe.Sizeof(immersive)) {
		dwmSet(hwnd, dwmwaUseImmersiveDarkModeOld, unsafe.Pointer(&immersive), unsafe.Sizeof(immersive))
	}

	caption, text := lightCaption, lightText
	if dark {
		caption, text = darkCaption, darkText
	}

	// Win11 起可精确指定标题栏与文字颜色；更早系统靠沉浸式深色模式即可。
	dwmSet(hwnd, dwmwaCaptionColor, unsafe.Pointer(&caption), unsafe.Sizeof(caption))
	dwmSet(hwnd, dwmwaTextColor, unsafe.Pointer(&text), unsafe.Sizeof(text))
	dwmSet(hwnd, dwmwaBorderColor, unsafe.Pointer(&caption), unsafe.Sizeof(caption))
}

// forceForeground 把窗口带到前台，避免双击 exe 后窗口被其它窗口盖住。
func forceForeground(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	const (
		swRestore = 9
		swShow    = 5
		vkMenu    = 0x12 // Alt，用于解除前台锁定
	)
	user32 := windows.NewLazySystemDLL("user32.dll")
	procShowWindow := user32.NewProc("ShowWindow")
	procSetForeground := user32.NewProc("SetForegroundWindow")
	procBringToTop := user32.NewProc("BringWindowToTop")
	procSetActive := user32.NewProc("SetActiveWindow")
	procKeybd := user32.NewProc("keybd_event")

	procShowWindow.Call(hwnd, swRestore)
	// 按一下再松开 Alt，可绕过 Windows 的前台窗口限制。
	procKeybd.Call(vkMenu, 0, 0, 0)
	procKeybd.Call(vkMenu, 0, 2, 0) // KEYEVENTF_KEYUP
	procSetForeground.Call(hwnd)
	procBringToTop.Call(hwnd)
	procSetActive.Call(hwnd)
	procShowWindow.Call(hwnd, swShow)
}
