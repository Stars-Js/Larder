// Package desktop 在原生窗口内承载 Larder 的 Web 界面，
// 让 exe 自身即可完成全部操作，无需跳转到系统浏览器。
package desktop

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrNoWebView 表示当前平台/环境无法创建原生窗口。
var ErrNoWebView = errors.New("desktop: 当前环境不支持原生窗口")

// dataPath 返回 WebView2 的缓存目录（用于持久化窗口状态、localStorage 等）。
func dataPath() string {
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		p := filepath.Join(dir, "Larder", "webview")
		_ = os.MkdirAll(p, 0o755)
		return p
	}
	return ""
}
