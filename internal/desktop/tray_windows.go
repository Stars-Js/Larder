//go:build windows

package desktop

import (
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 托盘 / 窗口关闭行为。
//
// - 启动后窗口正常显示，同时注册任务栏通知区图标；
// - 点击窗口关闭按钮：隐藏到托盘，HTTP / MCP 继续运行；
// - 单击/双击托盘图标：恢复主窗口；
// - 托盘右键菜单：显示主界面 / 退出 Larder；
// - 退出时销毁窗口，主进程随后关闭 HTTP 服务与 MCP。
const (
	trayID          = 1
	wmTrayCallback  = 0x8001
	wmTrayQuit      = 0x8002
	wmClose         = 0x0010
	wmNull          = 0x0000
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmContextMenu   = 0x007B
	ninSelect       = 0x0400
	ninKeySelect    = 0x0401

	nimAdd        = 0x00000000
	nimModify     = 0x00000001
	nimDelete     = 0x00000002
	nimSetVersion = 0x00000004

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifInfo    = 0x00000010
	nifShowTip = 0x00000080

	niifInfo = 0x00000001

	mfString    = 0x00000000
	mfSeparator = 0x00000800
	tpmRight    = 0x0002
	tpmReturn   = 0x0100
	tpmNoNotify = 0x0080

	swMinimize = 6
	swRestore  = 9
)

type notifyIconData struct {
	Size            uint32
	Wnd             uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	TimeoutVersion  uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUID            [16]byte
	BalloonIcon     uintptr
}

type point struct {
	X, Y int32
}

var (
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	procShellNotifyIconW    = shell32.NewProc("Shell_NotifyIconW")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procSetWindowLongPtrW   = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW     = user32.NewProc("CallWindowProcW")

	trayCallback  uintptr
	oldWindowProc uintptr
	quitting      atomic.Bool
	hideBallooned atomic.Bool
	trayInstalled atomic.Bool
)

func installTray(hwnd uintptr) {
	if hwnd == 0 || !trayInstalled.CompareAndSwap(false, true) {
		return
	}
	trayCallback = syscall.NewCallback(trayWndProc)
	// GWLP_WNDPROC = -4
	old, _, _ := procSetWindowLongPtrW.Call(hwnd, ^uintptr(3), trayCallback)
	if old == 0 {
		trayInstalled.Store(false)
		return
	}
	oldWindowProc = old
	addTrayIcon(hwnd)
	trayBalloon(hwnd, "Larder 已启动", "关闭窗口会最小化到任务栏；单击任务栏或托盘图标返回，右键托盘可退出。")
}

const wmEraseBkgnd = 0x0014

func trayWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmEraseBkgnd:
		if eraseWindowBackground(hwnd, wparam) {
			return 1
		}
		return callOldWindowProc(hwnd, msg, wparam, lparam)
	case wmClose:
		if quitting.Load() {
			return callOldWindowProc(hwnd, msg, wparam, lparam)
		}
		minimizeMainWindow(hwnd)
		if hideBallooned.CompareAndSwap(false, true) {
			trayBalloon(hwnd, "Larder 仍在后台运行", "已最小化到任务栏，MCP 服务保持开启；单击任务栏或托盘图标返回，右键托盘可退出。")
		}
		return 0
	case wmTrayCallback:
		switch lparam & 0xFFFF {
		case wmLButtonUp, wmLButtonDblClk, ninSelect, ninKeySelect:
			showMainWindow(hwnd)
		case wmRButtonUp, wmContextMenu:
			showTrayMenu(hwnd)
		}
		return 0
	case wmTrayQuit:
		quitFromTray(hwnd)
		return 0
	default:
		return callOldWindowProc(hwnd, msg, wparam, lparam)
	}
}

func callOldWindowProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if oldWindowProc == 0 {
		return 0
	}
	r, _, _ := procCallWindowProcW.Call(oldWindowProc, hwnd, msg, wparam, lparam)
	return r
}

func addTrayIcon(hwnd uintptr) {
	data := notifyIconData{
		Wnd:             hwnd,
		ID:              trayID,
		Flags:           nifMessage | nifIcon | nifTip | nifShowTip,
		CallbackMessage: wmTrayCallback,
		Icon:            loadLogoIcon(),
	}
	data.Size = uint32(unsafe.Sizeof(data))
	copyUTF16(data.Tip[:], "Larder · 本地知识库")
	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&data)))

	// NOTIFYICON_VERSION_4：回调参数里带事件与图标 ID。
	version := data
	version.Flags = 0
	version.TimeoutVersion = 4
	procShellNotifyIconW.Call(nimSetVersion, uintptr(unsafe.Pointer(&version)))
}

func removeTrayIcon(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	data := notifyIconData{Wnd: hwnd, ID: trayID}
	data.Size = uint32(unsafe.Sizeof(data))
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
}

func trayBalloon(hwnd uintptr, title, text string) {
	if hwnd == 0 {
		return
	}
	data := notifyIconData{
		Wnd:            hwnd,
		ID:             trayID,
		Flags:          nifInfo,
		InfoFlags:      niifInfo,
		TimeoutVersion: 5000,
	}
	data.Size = uint32(unsafe.Sizeof(data))
	copyUTF16(data.Info[:], text)
	copyUTF16(data.InfoTitle[:], title)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&data)))
}

func showTrayMenu(hwnd uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	appendMenu(menu, mfString, 1, "显示主界面")
	appendMenu(menu, mfSeparator, 0, "")
	appendMenu(menu, mfString, 2, "退出 Larder")

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(menu,
		tpmRight|tpmReturn|tpmNoNotify,
		uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	procPostMessageW.Call(hwnd, wmNull, 0, 0)
	switch cmd {
	case 1:
		showMainWindow(hwnd)
	case 2:
		quitFromTray(hwnd)
	}
}

func appendMenu(menu, flags, id uintptr, text string) {
	p, _ := syscall.UTF16PtrFromString(text)
	procAppendMenuW.Call(menu, flags, id, uintptr(unsafe.Pointer(p)))
}

func showMainWindow(hwnd uintptr) {
	procShowWindow.Call(hwnd, swRestore)
	procSetForegroundWindow.Call(hwnd)
}

func minimizeMainWindow(hwnd uintptr) {
	procShowWindow.Call(hwnd, swMinimize)
}

func quitFromTray(hwnd uintptr) {
	if hwnd == 0 || !quitting.CompareAndSwap(false, true) {
		return
	}
	removeTrayIcon(hwnd)
	procPostMessageW.Call(hwnd, wmClose, 0, 0)
}

func copyUTF16(dst []uint16, s string) {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return
	}
	n := len(u)
	if n > len(dst) {
		n = len(dst)
		u[len(dst)-1] = 0
	}
	copy(dst[:n], u[:n])
}
