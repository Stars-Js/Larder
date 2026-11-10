<p align="center">
  <a href="internal/web/dist/logo.svg"><img src="internal/web/dist/logo.svg" width="96" height="96" alt="Larder"></a>
</p>

<h1 align="center">Larder</h1>

<p align="center">
把本地文档变成 AI 助手可检索、可引用、可追溯的知识库。
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.27%2B-00ADD8?logo=go&logoColor=white">
  <img alt="Windows" src="https://img.shields.io/badge/Windows-10%2F11-0078D4?logo=windows&logoColor=white">
  <img alt="MCP" src="https://img.shields.io/badge/MCP-Streamable%20HTTP-6B4FBB">
  <img alt="SQLite" src="https://img.shields.io/badge/SQLite%20%2B%20sqlite--vec-003B57?logo=sqlite&logoColor=white">
  <a href="LICENSE"><img alt="MIT" src="https://img.shields.io/badge/License-MIT-yellow.svg"></a>
</p>

---

Larder 是一个**单 exe、数据保存在本机**的 RAG 桌面应用：

- 把 Markdown、文本、代码、Office、HTML、EPUB、PDF 等文档解析、切块、向量化；
- 每个知识库一个独立的 SQLite 数据库，向量索引由 `sqlite-vec` 提供；
- 通过 **MCP（Streamable HTTP）**、**HTTP API** 和**内置 Web 界面**对外提供检索；
- 回答可以带**文档名、文件、章节、原文和可点击的检索日志**，并支持按客户端定制引用格式；
- Windows 下双击 exe 打开原生 WebView2 窗口，**不显示控制台**；关闭窗口后驻留托盘，MCP 继续运行。

> 当前版本：**1.0.0**。默认监听 `127.0.0.1:8765`，MCP 端点为 `/mcp`。

---

## 目录

- [功能](#功能)
- [快速开始](#快速开始)
- [界面与工作流](#界面与工作流)
- [服务配置](#服务配置)
- [MCP 接入](#mcp-接入)
- [引用格式](#引用格式)
- [HTTP API](#http-api)
- [配置与数据](#配置与数据)
- [从源码构建](#从源码构建)
- [常见问题](#常见问题)
- [项目结构](#项目结构)

---

## 功能

- **单文件、便携**：一个 exe 即可运行；首次启动在 exe 同级生成 `larder.toml`、`data/` 和日志，不写注册表。
- **多知识库**：每个知识库是 `data/<id>.db`，拥有独立的文档、切块、向量索引和设置，互不干扰。
- **解析流水线**：解析 → 切块 → 向量化（→ 检索时重排）。每篇文档、每个知识库都显示各环节状态，失败原因落库，可一键重新处理。
- **本地解析 + 云 OCR 兜底**：内置纯 Go 解析器处理文本、代码、文字版 PDF、DOCX/PPTX/XLSX/EPUB/HTML；扫描件、无文本层 PDF 可自动走 MinerU / Docling / Tika。
- **OpenAI 兼容服务**：向量化和重排都走标准 `/v1/embeddings`、`/v1/rerank`，可接 SiliconFlow、OpenAI、Ollama、Infinity、Xinference 等。
- **多密钥 + 模型自选**：每个服务可保存多条密钥，支持复制、修改、删除、显示、逐条检测、切换当前密钥；模型自动拉取，也可手动选择。
- **MCP 一键注入**：支持 Claude Code、Claude Desktop、Cursor、ZCode、Cherry Studio、DSH，并可取消注入；只增删 `larder` 条目，保留其它 MCP 配置。
- **按平台定制引用**：Default / ZCode / Cherry Studio / Claude / Cursor / DSH 分别设置 Markdown 悬浮链接或纯文本；`cite`、日志、文档、文件、章节、原文均可勾选和排序。
- **观测能力**：用量页统计解析次数、嵌入/重排 Token；日志页展示检索、入库、设置和 MCP 调用历史，命中片段可点开查看并跳转原文档。
- **界面**：莫兰迪紫 + 毛玻璃磁贴、矢量 Logo、侧边栏动效；检索结果 Markdown 渲染并高亮关键词；支持 Markdown 原文、渲染、Chunks 和 PDF 原文预览。
- **Windows 原生体验**：WebView2 内嵌窗口、系统 DPI 感知、任务栏/托盘常驻；缺少 WebView2 时自动回退到系统浏览器。

---

## 快速开始

### 下载

从 [Releases](../../releases/latest) 下载对应架构的 exe：

| 平台 | 文件 |
| --- | --- |
| Windows x64 | `larder-windows-amd64.exe` |
| Windows ARM64 | `larder-windows-arm64.exe` |
| 校验和 | `SHA256SUMS` |

> 单文件、免安装；首次运行会在 exe 同级生成 `larder.toml` 和 `data/`。

### 1. 运行

如果你拿到的是已构建版本，直接运行：

```powershell
.\larder-windows-amd64.exe
```

首次启动会在 exe 同级生成：

```text
larder-windows-amd64.exe
larder.toml      # 参数文件，界面保存会写回
data/
  <kb-id>.db     # 每个知识库一个数据库
  larder.log     # 运行日志
  activity.log   # 交互日志
  .uploads/      # 上传文件缓存
```

默认会打开原生窗口，地址为 `http://127.0.0.1:8765`。

可用参数：

| 参数 | 说明 |
| --- | --- |
| `-addr 127.0.0.1:9000` | 覆盖监听地址 |
| `-web` | 不创建原生窗口，用系统浏览器打开 |
| `-no-open` | 仅后台运行 HTTP/MCP，不打开任何窗口 |
| `-config path\to\larder.toml` | 指定配置文件 |
| `-print-config` | 打印最终生效的配置后退出 |
| `-log debug` | 覆盖日志级别 |

> 关闭窗口 = 隐藏到托盘，HTTP 与 MCP 继续运行；单击任务栏/托盘图标恢复，托盘右键「退出 Larder」才会停止服务。

### 2. 配置向量化服务（必需）

打开「设置 → 向量化服务」，填 **API 地址** 和 **API 密钥**，例如：

```text
API 地址：https://api.siliconflow.cn/v1
模型：BAAI/bge-m3
```

保存是自动的。填好地址与密钥后，Larder 会自动拉取模型列表；也可以点「刷新模型」或手动下拉选择。

> 没有外部服务时，也可以用 Ollama / Infinity 在本机提供 OpenAI 兼容的 embedding 接口。
> 每个知识库在首次入库时会锁定自己的向量维度；之后更换不同维度的模型会报「维度冲突」，需要新建知识库。

### 3. 新建知识库并入库

1. 在「知识库」页点「新建知识库」；
2. 进入知识库，把文件拖入窗口，或点上传；
3. 等待「解析 → 切块 → 向量化」完成；
4. 失败文档会显示原因，配置好服务后点「重新处理」。

### 4. 检索

- 在「检索」页选择知识库、输入问题即可；
- 或让 AI 客户端通过 MCP 调用 `larder_search`；
- 不指定知识库时，**默认检索全部知识库**。

健康检查：

```bash
curl http://127.0.0.1:8765/healthz
```

---

## 界面与工作流

### 知识库

- 卡片显示文档数、切块数、向量维度、解析服务、嵌入模型、重排模型/开关，以及已解析、已嵌入、失败数量。
- 每个知识库可以单独设置：跟随全局解析 / 内置解析 / MinerU / Docling / Tika、重排开关、切块大小与重叠、`top_k`、候选条数。

### 文档

点击文档可查看：

- **Markdown 原文**：复制、导出 `.md`；
- **渲染视图**：按 Markdown 阅读；
- **Chunks**：查看切块、来源与命中信息；
- **PDF 原文**：内置标签页阅读，也可下载原文件；
- 重新处理、删除。

### 检索

命中片段按 Markdown 渲染，查询关键词高亮；结果保留文档名、章节、来源路径和分数，方便核对。

### 用量

统计解析次数（成功/失败/字符数）、嵌入 Token、重排 Token、总 Token，并按天、知识库、模型汇总。单位自动使用 K/M。

### 日志

记录检索、入库、重新处理、设置保存、MCP 调用等事件：

- 点击任意记录可查看当次命中的片段；
- 可直接打开原文档；
- MCP 引用里的日志短链 `/c/<event>` 会跳到对应日志详情。

### 设置

- 解析、向量化、重排三个服务都只需填 API 地址和密钥；
- 密钥支持多组，第一个为当前使用；
- 模型自动获取，也可手动选择；
- 引用格式按平台配置；
- MCP 服务可随时开关，立即生效；
- 一键注入/取消注入 Agent。

---

## 服务配置

Larder 的三个外部服务都是可选/可替换的：

| 环节 | 是否必需 | 协议 | 说明 |
| --- | --- | --- | --- |
| 解析 | 否 | 内置 / MinerU / Docling / Tika | 不配置时使用内置纯 Go 解析 |
| 向量化 | **是** | OpenAI 兼容 `/v1/embeddings` | 入库和检索都依赖 |
| 重排 | 否 | OpenAI 兼容 `/v1/rerank` | 未配置时退化为纯向量召回 |

### 解析

- **内置解析**：Markdown、TXT、代码、文字版 PDF、DOCX、PPTX、XLSX、EPUB、HTML。
- **MinerU / Docling / Tika**：本地能处理的格式仍然本地处理；扫描件、矢量图形 PDF（无文本层）会自动上传外部 API 做 OCR。
- 在设置里填 API 地址即可自动识别协议：
  - `mineru.net` → MinerU 云 API；
  - 地址含 `docling` / `tika` → 对应服务；
  - 自建 MinerU 默认按 `/file_parse`。

### 向量化

任何 OpenAI 兼容的 `/v1/embeddings` 服务。请求会自动处理 `/v1` 前缀，避免出现 `/v1/v1/embeddings`。

### 重排

任何 OpenAI 兼容的 `/v1/rerank` 服务。重排失败不会让检索失败，会自动退回向量结果。

### 多密钥

界面里可以：

- 添加多条密钥；
- 复制、显示/隐藏、修改、删除；
- 逐条「检测」；
- 「设为当前」切换。

配置文件里对应 `api_keys = ['sk-当前', 'sk-备用']`，第一个为当前使用。

---

## MCP 接入

Larder 通过 **Streamable HTTP** 暴露 MCP：

```text
http://127.0.0.1:8765/mcp
```

推荐在「设置 → MCP 服务 → 选择 Agent」里一键注入。Larder 会自动写入对应客户端配置，并在 URL 后追加平台标识：

```text
http://127.0.0.1:8765/mcp?client=zcode
```

`client` 参数用于选择该平台的引用格式，可选：

```text
default / zcode / cherrystudio / claude-desktop / claude-code / cursor / dsh
```

### 支持的客户端

| 客户端 | 写入位置 / 方式 |
| --- | --- |
| Claude Code | `claude mcp add --transport http larder <url>` |
| Claude Desktop | `claude_desktop_config.json`，通过 `npx mcp-remote` 桥接 |
| Cursor | `~/.cursor/mcp.json` |
| ZCode | `~/.zcode/cli/config.json` 的 `mcp.servers`（HTTP） |
| Cherry Studio | 生成 `data/mcp-cherrystudio.json`，在 Cherry Studio → 设置 → MCP → 从 JSON 导入 |
| DSH | `~/.dsh/settings.yaml` 的 `mcp.servers`（通过 `npx mcp-remote`） |
| 通用 | 复制一份 `mcpServers` JSON 到任意支持远程 MCP 的客户端 |

每个客户端都提供「复制 JSON」和「取消注入」；取消时只删除 `larder` 条目。

### MCP 工具

| 工具 | 作用 |
| --- | --- |
| `larder_use_kb` | 选择后续工具默认使用的知识库（在当前服务进程内生效） |
| `larder_list_kbs` | 列出全部知识库及 ID、名称、文档数 |
| `larder_search` | 语义检索；不指定 `kb` 时默认检索全部知识库 |
| `larder_ingest_file` | 把本地文件解析、切块、向量化后加入知识库 |
| `larder_ingest_url` | 抓取网页或在线文件并入库 |
| `larder_list_documents` | 列出知识库中的文档 |
| `larder_get_document` | 查看文档元信息 |
| `larder_delete_document` | 删除文档及其切块与向量 |

`larder_search` 返回：

- 第一行的 Markdown 右对齐知识库小标（表格形式，兼容 Cherry Studio 等客户端）；
- 每条命中片段的文档名、文件、章节、得分、原文；
- 完整行内引用串（`cite_id` / `cite_link`）；
- 本次检索的日志短链。

> 写入类操作（入库、删除）必须指定知识库；检索类操作默认检索全部知识库，除非用户明确指定。

---

## 引用格式

「设置 → 引用格式」可以按平台分别配置。每个平台支持两种显示方式：

### Markdown 悬浮链接

```markdown
[[cite:id]](http://127.0.0.1:8765/c/<event> "【文档】…【文件】…【章节】…【原文】…")
```

正文显示 `[cite:id]`；鼠标悬停可看来源字段；点击打开本次检索日志。

### 纯文本

```text
[cite:id]（【日志】http://127.0.0.1:8765/c/<event>；【文档】…【文件】…【章节】…【原文】…）
```

适合会移除 Markdown 链接的客户端。ZCode 固定使用纯文本模式。

### 字段规则

- **引用标记 `cite`**：可选；启用时始终在第一位。
- **日志 `log`**：可选；启用时固定在括号内第一位。取消勾选后不输出日志链接，但仍保留来源字段。
- **来源字段**：文档、文件、章节、原文，可勾选并上下排序。
- 修改后自动保存并立即生效；首次为某个平台接入时，需要在该客户端重新注入一次 MCP 地址，使 URL 带上 `?client=<平台>`。

### ZCode 特别说明

ZCode 不会把 MCP 服务的 `instructions` 传给模型，且可能只复制结构化结果里的 `cite_id`。因此 Larder 对 ZCode：

- 固定使用纯文本引用；
- 让 `cite_id` 与 `cite_link` 都返回**完整引用串**（含括号内的日志与来源）；
- 在工具描述和检索结果末尾明确要求逐字复制完整引用，禁止只输出 `[cite:id]`。

更新 Larder 后，请**新建一个 ZCode 会话**（或重启 ZCode）再测试，避免旧会话缓存旧的工具描述。

---

## HTTP API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/healthz` | 健康检查与运行信息 |
| `GET` | `/` | 内置 Web 界面 |
| `GET` | `/c/{event}` | 引用日志短链，跳转到日志详情 |
| `GET` | `/api/badge.svg?kb={id}` | 知识库 SVG 小标 |
| `GET` | `/api/kbs` | 列出全部知识库 |
| `POST` | `/api/kbs` | `{name}` 新建知识库 |
| `GET` | `/api/kbs/{id}` | 知识库详情（文档/切块/维度/状态/设置） |
| `PATCH` | `/api/kbs/{id}` | `{name}` 重命名 |
| `DELETE` | `/api/kbs/{id}` | 删除知识库及全部数据 |
| `GET` | `/api/kbs/{id}/settings` | 读取知识库级设置 |
| `PUT` | `/api/kbs/{id}/settings` | 更新知识库级设置并热应用 |
| `POST` | `/api/kbs/{id}/search` | `{query, top_k}` → 命中列表 |
| `GET` | `/api/kbs/{id}/documents` | 文档列表（含解析/嵌入/重排状态） |
| `GET` | `/api/kbs/{id}/documents/{docId}` | 文档详情 |
| `GET` | `/api/kbs/{id}/documents/{docId}/content` | Markdown 原文 + 全部 Chunks |
| `GET` | `/api/kbs/{id}/documents/{docId}/file` | 原始文件；`?download=1` 下载 |
| `POST` | `/api/kbs/{id}/documents/{docId}/reprocess` | 使用已保存文件/URL 重新处理 |
| `DELETE` | `/api/kbs/{id}/documents/{docId}` | 删除文档 |
| `POST` | `/api/kbs/{id}/ingest/file` | `{path, title?, refresh?}` 本地文件入库 |
| `POST` | `/api/kbs/{id}/ingest/url` | `{url, title?, refresh?}` URL 入库 |
| `POST` | `/api/kbs/{id}/ingest/upload` | `multipart/form-data`，字段 `file` |
| `POST` | `/api/models` | 拉取模型列表；缺省使用已保存地址与密钥 |
| `POST` | `/api/services/test` | 用指定密钥/模型检测服务 |
| `GET` | `/api/mcp/clients` | 可注入的客户端及状态 |
| `POST` | `/api/mcp/install` | `{client}` 一键注入 |
| `POST` | `/api/mcp/uninstall` | `{client}` 取消注入 |
| `GET` | `/api/logs?limit=200` | 最近交互日志 |
| `GET` | `/api/usage?days=30` | 用量统计 |
| `GET` | `/api/settings` | 读取服务配置（密钥掩码） |
| `GET` | `/api/settings/keys?service=embed\|rerank\|parse` | 读取明文密钥（仅本机） |
| `PUT` | `/api/settings` | 更新服务配置并热生效 |

示例：

```bash
# 新建知识库
curl -X POST http://127.0.0.1:8765/api/kbs \
  -H 'Content-Type: application/json' \
  -d '{"name":"产品文档"}'

# 入库本地文件
curl -X POST http://127.0.0.1:8765/api/kbs/<kb-id>/ingest/file \
  -H 'Content-Type: application/json' \
  -d '{"path":"./docs/architecture.md"}'

# 检索
curl -X POST http://127.0.0.1:8765/api/kbs/<kb-id>/search \
  -H 'Content-Type: application/json' \
  -d '{"query":"向量检索是怎么实现的","top_k":5}'
```

---

## 配置与数据

### 配置优先级

```text
内置默认值 < larder.toml < 环境变量
```

配置文件查找顺序：

1. `-config` 指定的路径；
2. `LARDER_CONFIG`；
3. exe 同级 `larder.toml`；
4. 当前目录 `larder.toml`；
5. `configs/larder.toml`；
6. 数据目录 `config.toml`。

首次运行找不到配置时，会自动在 exe 同级生成 `larder.toml`。完整样例见 [`configs/larder.toml`](configs/larder.toml)。

### 常用环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `LARDER_ADDR` | `127.0.0.1:8765` | 监听地址 |
| `LARDER_DATA_DIR` | exe 同级 `data` | 知识库数据目录 |
| `LARDER_CONFIG` | – | 配置文件路径 |
| `LARDER_LOG_LEVEL` | `info` | `debug/info/warn/error` |
| `LARDER_LOG_FORMAT` | `text` | `text/json` |
| `LARDER_MCP_ENABLED` | `true` | 是否提供 `/mcp` |
| `LARDER_EMBED_BASE_URL` | `https://api.openai.com` | 向量服务地址 |
| `LARDER_EMBED_API_KEY` | – | 向量服务密钥 |
| `LARDER_EMBED_MODEL` | `text-embedding-3-small` | 向量模型 |
| `LARDER_EMBED_DIM` | `1536` | 维度提示；实际以服务返回为准 |
| `LARDER_PARSE_PROVIDER` | `plain` | `plain/mineru/docling/tika` |
| `LARDER_PARSE_BASE_URL` / `_API_KEY` / `_PATH` | – | 外部解析服务 |
| `LARDER_RERANK_ENABLED` | `false` | 是否启用重排 |
| `LARDER_RERANK_BASE_URL` / `_API_KEY` / `_MODEL` | – | 重排服务 |

### 密钥与安全

- `larder.toml` 中的 API 密钥是**明文保存**的（本机使用）。分享 exe 或配置前请先删除密钥。
- 更安全的做法是写成环境变量引用：

```toml
[embed.endpoint]
api_key = '${LARDER_EMBED_API_KEY}'
```

- 默认只监听 `127.0.0.1`，不要直接暴露到公网；`/api/settings/keys` 会向本机请求返回明文密钥。
- 每个知识库的向量维度在首次入库后固定；更换模型前请确认维度一致。

---

## 从源码构建

要求：

- Go **1.27.3+**；
- Windows 10/11（原生窗口需要 WebView2 运行时）；
- 构建桌面版时 `CGO_ENABLED=0`。

```bash
git clone <repo-url> Larder
cd Larder
go mod download

# Windows 桌面版（无控制台）
make windows          # dist/larder-windows-amd64.exe
make windows-arm64    # dist/larder-windows-arm64.exe
make release          # release/ + SHA256SUMS
# 或
powershell -ExecutionPolicy Bypass -File scripts/build-windows.ps1
powershell -ExecutionPolicy Bypass -File scripts/build-windows.ps1 -Arch arm64
```

产物：`dist/larder-windows-amd64.exe`、`dist/larder-windows-arm64.exe`。

其他平台/纯 Web 模式：

```bash
go build -o bin/larder .
./bin/larder -web
# 或
go run . -web
```

> 非 Windows 平台没有原生 WebView2 窗口，会自动回退到系统浏览器。

开发检查：

```bash
go test ./...
go vet ./...
```

离线联调（模拟 embedding / rerank / MinerU）：

```bash
python scripts/mock_services.py --port 8811
```

---

## 常见问题

### `1.pdf` 提示无法解析？

这类 PDF 没有文本层（文字被导出成矢量图形），纯文本抽取读不到内容。处理方式：

1. 在「设置 → 解析服务」填 `https://mineru.net` 和 API 密钥；
2. 回到知识库，对失败文档点「重新处理」。

Larder 会本地优先解析；本地读不到时自动上传 MinerU 做 OCR。

> 实测：`1.pdf`（40 页、51.8MB、无文本层）经 MinerU OCR 后得到约 8 万字符、74 个切块，
> `bge-m3` 维度 1024，`bge-reranker-v2-m3` 检索/重排正常。

### MCP 连接返回 404？

在「设置 → MCP 服务」确认「启动 MCP 服务器」已打开。关闭后 `/mcp` 会立即返回 404。

### ZCode 只输出 `[cite:id]`，没有来源和日志？

请确认：

1. 使用当前版本（1.0.0+）；
2. ZCode 的 Larder URL 带 `?client=zcode`；
3. **新建一个 ZCode 会话**或重启 ZCode，让模型重新加载工具描述；
4. 必要时在 Larder 设置里重新点一次「一键注入」。

### Cherry Studio 报 `Unsupported URL transport: stdio`？

使用 Larder 生成的 `data/mcp-cherrystudio.json` 导入，关键字段是：

```json
{
  "type": "streamableHttp",
  "url": "http://127.0.0.1:8765/mcp?client=cherrystudio"
}
```

不要手工把它配成 `stdio`。

### Cherry Studio 报结构化结果 schema 不匹配？

旧版本缓存了旧的工具输出 schema。升级 Larder 后，在 Cherry Studio 里删除旧的 `larder` 条目，
重新导入 `data/mcp-cherrystudio.json`，再新建会话。

### 模型列表为空 / 拉取失败？

检查 API 地址是否包含正确的版本路径（如 `/v1`）、密钥是否有效；也可以点「刷新模型」，
或在设置里手动选择模型。OpenAI 兼容服务通常需要 `GET /v1/models`。

### 入库报「维度冲突」？

同一个知识库的向量维度在首次入库后固定。请换回相同维度的模型，或新建知识库。

### 重排失败会影响检索吗？

不会。重排服务不可用时自动退回向量召回结果，检索仍会返回。

### 端口被占用？

```powershell
.\larder-windows-amd64.exe -addr 127.0.0.1:9000
```

### 没有原生窗口 / 提示缺少 WebView2？

安装 Microsoft Edge WebView2 Runtime；或者用 `-web` 以浏览器模式运行。

---

## 项目结构

```text
.
├── main.go                     # 入口：参数、配置、服务装配、窗口/托盘
├── internal/
│   ├── config/                 # 默认值 < TOML < 环境变量；引用格式配置
│   ├── model/                  # 领域数据结构
│   ├── store/                  # SQLite + sqlite-vec；文档/切块/向量/用量
│   ├── services/               # 解析/向量/重排服务容器，支持热更新
│   ├── kb/                     # 多知识库管理器与知识库级设置
│   ├── chunk/                  # 按标题层级切块，保留代码围栏
│   ├── provider/
│   │   ├── parse/              # plain / hybrid / PDF / Office / EPUB / HTML / MinerU / Docling / Tika
│   │   ├── embed/              # OpenAI 兼容 embeddings
│   │   └── rerank/             # OpenAI 兼容 rerank
│   ├── ingest/                 # 入库编排：判重→解析→切块→向量化→落库→重试
│   ├── search/                 # 检索编排：向量召回→重排→TopK
│   ├── mcp/                    # MCP 工具、引用格式、日志短链
│   ├── api/                    # REST 接口、MCP 注入、设置、用量、日志
│   ├── activity/               # 交互日志与命中片段
│   ├── web/                    # 内嵌前端（go:embed + SPA 回退）
│   ├── desktop/                # Windows WebView2 窗口、DPI、托盘、标题栏
│   ├── route/                  # REST + MCP + 前端组装
│   ├── server/                 # HTTP 生命周期与优雅退出
│   └── httpx/                  # 共享 HTTP 客户端（重试/QPS）
├── configs/larder.toml         # 配置样例
├── scripts/                    # 构建、mock 服务、开发辅助
├── docs/architecture.md        # 数据模型与流水线设计
├── LICENSE                     # MIT
└── go.mod / Makefile
```

依赖方向保持单向：

```text
main → route → {api, mcp} → app → {ingest, search} → {store, provider/*} → model
```

`ingest` / `search` 只依赖接口（`store.Store`、`embed.Embedder`、`rerank.Reranker`、`parse.Parser`），
方便替换实现和测试。

---

## 设计文档

- [`docs/architecture.md`](docs/architecture.md) —— 数据模型、入库与检索流水线、扩展点。

---

## 图标

<details>
<summary>Logo SVG 源码</summary>

```svg
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 128 128" width="128" height="128" role="img" aria-label="Larder">
  <defs>
    <linearGradient id="bg" x1="12" y1="8" x2="116" y2="124" gradientUnits="userSpaceOnUse">
      <stop offset="0" stop-color="#a291d8"/>
      <stop offset="0.52" stop-color="#8b78c8"/>
      <stop offset="1" stop-color="#6c5aa9"/>
    </linearGradient>
    <linearGradient id="sheet" x1="64" y1="28" x2="64" y2="112" gradientUnits="userSpaceOnUse">
      <stop offset="0" stop-color="#ffffff"/>
      <stop offset="1" stop-color="#f5f2ff"/>
    </linearGradient>
    <linearGradient id="backA" x1="30" y1="22" x2="98" y2="108" gradientUnits="userSpaceOnUse">
      <stop offset="0" stop-color="#c7b9ee"/>
      <stop offset="1" stop-color="#a996dd"/>
    </linearGradient>
    <linearGradient id="backB" x1="34" y1="20" x2="106" y2="108" gradientUnits="userSpaceOnUse">
      <stop offset="0" stop-color="#dcecfd"/>
      <stop offset="1" stop-color="#b9d6f5"/>
    </linearGradient>
    <linearGradient id="fold" x1="78" y1="28" x2="98" y2="48" gradientUnits="userSpaceOnUse">
      <stop offset="0" stop-color="#e3f0fd"/>
      <stop offset="1" stop-color="#c4ddf7"/>
    </linearGradient>
    <radialGradient id="glow" cx="34" cy="22" r="82" gradientUnits="userSpaceOnUse">
      <stop offset="0" stop-color="#ffffff" stop-opacity=".28"/>
      <stop offset="1" stop-color="#ffffff" stop-opacity="0"/>
    </radialGradient>
    <filter id="soft" x="-24%" y="-24%" width="148%" height="148%">
      <feDropShadow dx="0" dy="3" stdDeviation="4.5" flood-color="#382b57" flood-opacity=".28"/>
    </filter>
    <filter id="line" x="-20%" y="-40%" width="140%" height="180%">
      <feDropShadow dx="0" dy="1" stdDeviation="1" flood-color="#382b57" flood-opacity=".18"/>
    </filter>
  </defs>

  <rect width="128" height="128" rx="28" fill="url(#bg)"/>
  <rect x="1.4" y="1.4" width="125.2" height="125.2" rx="26.8" fill="none" stroke="#ffffff" stroke-opacity=".34" stroke-width="1.2"/>
  <rect width="128" height="128" rx="28" fill="url(#glow)"/>

  <g filter="url(#soft)">
    <rect x="25" y="24" width="62" height="80" rx="10" fill="url(#backA)" transform="rotate(-7 56 64)"/>
    <rect x="39" y="21" width="63" height="81" rx="10" fill="url(#backB)" transform="rotate(5 70 61)"/>
  </g>

  <path d="M43 26h39.5a9 9 0 0 1 6.4 2.7l12.4 12.4a9 9 0 0 1 2.7 6.4V104a9 9 0 0 1-9 9H43a9 9 0 0 1-9-9V35a9 9 0 0 1 9-9z" fill="url(#sheet)" filter="url(#soft)"/>
  <path d="M84 27.2 102 45.2h-9a9 9 0 0 1-9-9z" fill="url(#fold)"/>
  <path d="M84 27.2V45.2h18" fill="none" stroke="#a9c8ec" stroke-width="1.1" stroke-linejoin="round"/>

  <rect x="47" y="48" width="22" height="22" rx="5.5" fill="#f7c84b" filter="url(#line)"/>
  <rect x="47" y="49" width="22" height="10" rx="5.5" fill="#ffdd7a" opacity=".55"/>

  <g filter="url(#line)">
    <rect x="47" y="80" width="46" height="6.4" rx="3.2" fill="#4b90e2"/>
    <rect x="47" y="92" width="38" height="6.4" rx="3.2" fill="#83bbef"/>
    <rect x="47" y="104" width="21" height="6.4" rx="3.2" fill="#4b90e2"/>
    <rect x="72" y="104" width="21" height="6.4" rx="3.2" fill="#b7a6e6"/>
  </g>
</svg>
```

</details>

---

## License

[MIT](LICENSE) © 2026 Larder contributors
