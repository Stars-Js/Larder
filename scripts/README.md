# scripts

开发与运维辅助脚本。

| 脚本 | 用途 |
| --- | --- |
| `build-windows.ps1` | 构建无控制台的 Windows 桌面版到 `dist/larder-windows-<arch>.exe` |
| `dev-run.ps1` | 加载 `.env` 中的环境变量并 `go run .` 本地启动 |
| `mock_embed_server.py` | 极简本地 OpenAI 兼容 `/v1/embeddings` 服务（确定性小向量），供离线测试 |
| `mock_services.py` | 一个进程同时模拟 `/v1/models`、`/v1/embeddings`、`/v1/rerank` 与 MinerU `/file_parse`，用于验证解析兜底、模型获取、重排与失败重试 |

> 想验证“扫描件 PDF → 外部解析 API”的完整兜底流程，用 `mock_services.py`：

```bash
python scripts/mock_services.py --port 8811
# 然后在界面「设置」中把解析方式设为 mineru，Base URL 填 http://127.0.0.1:8811，Path 填 /parse
```

> 构建 Windows 桌面版：

```powershell
# 默认 amd64
powershell -ExecutionPolicy Bypass -File scripts/build-windows.ps1
# ARM64
powershell -ExecutionPolicy Bypass -File scripts/build-windows.ps1 -Arch arm64
# 产物：dist\larder-windows-amd64.exe / dist\larder-windows-arm64.exe
```

> 发布两个架构的 exe 与校验和：

```bash
make release
# 产物：release/larder-windows-amd64.exe、larder-windows-arm64.exe、SHA256SUMS
```

> 应用图标资源由 `internal/desktop/logo.ico` 生成，已提交为
> `rsrc_windows_amd64.syso` 与 `rsrc_windows_arm64.syso`。更换图标后重新生成：

```bash
go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -ico internal/desktop/logo.ico -o rsrc_windows_amd64.syso
go run github.com/akavel/rsrc@v0.10.2 -arch arm64 -ico internal/desktop/logo.ico -o rsrc_windows_arm64.syso
```
