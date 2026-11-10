//go:build windows

package desktop

import (
	_ "embed"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

//go:embed logo.ico
var logoICO []byte

var (
	procLoadImageW   = user32.NewProc("LoadImageW")
	procSendMessageW = user32.NewProc("SendMessageW")

	logoIconOnce  sync.Once
	logoIconValue uintptr
)

// loadLogoIcon 把内嵌的 logo.ico 写临时文件并加载为 HICON。
// 窗口图标与托盘图标共用同一个句柄，进程退出时由系统回收。
func loadLogoIcon() uintptr {
	logoIconOnce.Do(func() {
		if len(logoICO) == 0 {
			return
		}
		p := filepath.Join(os.TempDir(), "larder-app-icon.ico")
		if err := os.WriteFile(p, logoICO, 0o644); err != nil {
			return
		}
		path, err := syscall.UTF16PtrFromString(p)
		if err != nil {
			return
		}
		const (
			imageIcon      = 1
			lrLoadFromFile = 0x00000010
			lrDefaultSize  = 0x00000040
		)
		h, _, _ := procLoadImageW.Call(0,
			uintptr(unsafe.Pointer(path)),
			imageIcon, 0, 0,
			lrLoadFromFile|lrDefaultSize)
		logoIconValue = h
	})
	return logoIconValue
}

// setWindowIcon 把 logo.ico 设为窗口标题栏与任务栏图标。
func setWindowIcon(hwnd uintptr) {
	hicon := loadLogoIcon()
	if hwnd == 0 || hicon == 0 {
		return
	}
	const (
		wmSetIcon = 0x0080
		iconSmall = 0
		iconBig   = 1
	)
	_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, iconBig, hicon)
	_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, iconSmall, hicon)
}
