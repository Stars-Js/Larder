//go:build windows

package desktop

import (
	"runtime"
	"time"

	webview2 "github.com/jchv/go-webview2"
)

// Run 在一个原生 WebView2 窗口中打开 url，阻塞直到窗口关闭。
// 返回 nil 表示正常关闭；返回 ErrNoWebView 表示无法创建窗口。
func Run(title, url string, width, height int) error {
	// WebView2 的窗口与消息循环必须固定在同一个 OS 线程上。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// 窗口按物理像素创建；按系统 DPI 放大后，WebView 内部的 CSS 视口
	// 仍是 1200×820，窗口在高分屏上大小合适且文字清晰。
	// 再按工作区收窄，避免 1080p + 150% 缩放下窗口高过屏幕。
	width, height = Scale(width), Scale(height)
	if aw, ah := WorkArea(); aw > 0 && ah > 0 {
		maxW, maxH := aw-48, ah-48
		if width > maxW {
			width = maxW
		}
		if height > maxH {
			height = maxH
		}
	}
	if width < 720 {
		width = 720
	}
	if height < 480 {
		height = 480
	}

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		DataPath:  dataPath(),
		WindowOptions: webview2.WindowOptions{
			Title:  title,
			Width:  uint(width),
			Height: uint(height),
			Center: true,
		},
	})
	if w == nil {
		return ErrNoWebView
	}
	defer w.Destroy()

	w.SetSize(width, height, webview2.HintNone)
	w.SetTitle(title)

	// 标题栏与页面同色（magpie 风格）：窗口一出现即生效。
	hwnd := uintptr(w.Window())
	setWindowBackground(hwnd)
	setWindowIcon(hwnd)
	installTray(hwnd)
	tintTitleBar(hwnd)

	w.Navigate(url)

	// WebView2 初始化完成后把窗口带到前台并再次着色（内容就绪后最稳）。
	go func() {
		time.Sleep(350 * time.Millisecond)
		w.Dispatch(func() {
			tintTitleBar(uintptr(w.Window()))
			forceForeground(uintptr(w.Window()))
		})
	}()

	w.Run()
	return nil
}
