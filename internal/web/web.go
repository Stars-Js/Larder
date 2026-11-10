// Package web 内嵌前端静态资源，并提供带 SPA 回退的 HTTP 处理器。
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed dist
var distFS embed.FS

// Handler 返回前端静态资源处理器：命中文件则返回文件，否则回退到 index.html。
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 只处理 GET/HEAD；其它交给 404。
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		// 本地应用更新后需要立即看到新界面，静态资源不做强缓存。
		w.Header().Set("Cache-Control", "no-cache")
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			// SPA 回退。
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}
