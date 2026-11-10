//go:build !windows

package desktop

// Run 在非 Windows 平台上不可用，返回 ErrNoWebView，
// 调用方应回退到系统浏览器。
func Run(title, url string, width, height int) error {
	return ErrNoWebView
}
