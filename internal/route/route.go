// Package route 把 REST、MCP 与前端静态资源组装成单一的 HTTP 处理器。
package route

import (
	"log/slog"
	"net/http"

	"larder/internal/activity"
	"larder/internal/api"
	"larder/internal/kb"
	"larder/internal/mcp"
)

// Deps 是路由装配所需的依赖。
type Deps struct {
	Manager  *kb.Manager
	API      api.Deps
	MCPPath  string
	Logger   *slog.Logger
	Activity *activity.Logger
}

// Handler 返回根路由：MCP 挂到配置路径，其余交给 REST（含前端静态资源）。
func Handler(d Deps) http.Handler {
	root := http.NewServeMux()

	// MCP 服务端（Streamable HTTP）。始终挂载，但按当前配置动态启停：
	// 关闭时返回 404，设置里切换后立即生效，无需重启进程。
	mcpSrv := mcp.New(d.Manager, d.API.MCPURL, d.Activity)
	mcpHandler := mcpSrv.Handler()
	root.Handle(d.MCPPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.API.Services.Config().Server.MCPEnabled {
			http.NotFound(w, r)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))

	// REST 与前端静态资源及其它所有路径。
	root.Handle("/", api.Handler(d.API))

	return recoverMiddleware(d.Logger, root)
}

func recoverMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("请求处理 panic", "panic", rec, "path", r.URL.Path)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
