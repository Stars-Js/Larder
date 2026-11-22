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

> 当前版本：**1.1.1**。默认监听 `127.0.0.1:8765`，MCP 端点为 `/mcp`。

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
- **按平台定制引用**：Default / ZCode / Cherry Studio / Claude / Cursor / DSH 分别设置悬浮链接或纯文本；纯文本严格按参考文献格式输出，悬浮模式可勾选链接、文献词条、文献文件名、原文并排序。
- **观测能力**：用量页统计解析次数、嵌入/重排 Token；日志页展示检索、入库、设置和 MCP 调用历史，命中片段可点开查看并跳转原文档。
- **界面**：莫兰迪紫 + 分层毛玻璃、极光背景与太阳/月亮主题切换（亮色/暗色，选择会记住）；矢量 Logo、侧边栏动效；检索结果 Markdown 渲染并高亮关键词；支持 Markdown 原文、渲染、Chunks 和 PDF 原文预览。
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
  <知识库名>/
    <kb-id>.db   # 该知识库的 SQLite（元数据 + 向量）
    pdf/         # 普通知识库上传的 PDF 原件
    files/       # 其它源文件（Word/PPT/Markdown/HTML…）
    image/<docID>/  # 解析器抽出的图片
    exports/     # 导出的 .bib 等文件
  larder.log     # 运行日志
  activity.log   # 交互日志
```

旧版 `data/<kb-id>.db`、`.uploads/`、`.assets/` 会在启动时自动迁移：数据库移到
`data/<知识库名>/<kb-id>.db`，图片移到 `data/<知识库名>/image/<docID>/`；迁移完成后
空的 `.assets/` 会删除。Zotero 文献库的 PDF 仍留在 Zotero，不复制。

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

- **Markdown 原文**：复制、导出 `Markdown + image/`（ZIP）；有解析文件或 PDF 时可点「打开解析目录」「打开文件目录」；
  普通知识库的解析文件不落盘，随时可导出；
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
| `larder_list_libraries` | 列出文献库（含 Zotero 集合、文档数、上次同步） |
| `larder_select_library` | 首次问答时按会话选择文献库 |
| `larder_use_kb` | 兼容旧名，等价于选择文献库 |
| `larder_list_kbs` | 列出全部知识库及 ID、名称、文档数 |
| `larder_search` | 语义检索；未指定库且存在多个库时返回 `selection_required` |
| `larder_get_references` | 按 GB/T 7714、APA、IEEE、Vancouver、MLA 生成参考文献与 `cite_link`；`include_bibtex=true` 时附 BibTeX |
| `larder_export_bibliography` | 导出 `.bib` 到 `data/exports/<kb>/`，同时返回内容 |
| `larder_sync_library` | 触发 Zotero 文献库后台同步 |
| `larder_library_status` | 查看同步任务进度与成功/失败数量 |
| `larder_ingest_file` | 把本地文件解析、切块、向量化后加入知识库 |
| `larder_ingest_url` | 抓取网页或在线文件并入库 |
| `larder_list_documents` | 列出知识库中的文档 |
| `larder_get_document` | 查看文档元信息 |
| `larder_delete_document` | 删除文档及其切块与向量 |

`larder_search` 返回：

- 第一行的 Markdown 右对齐知识库小标（表格形式，兼容 Cherry Studio 等客户端）；
- 每条命中片段的文档名、文件、章节、得分、原文；
- 结果最上方先给出「正文引用」表，每篇论文一行，直接给出可原样复制的 `cite_link`；长结果被客户端截断时也不会丢掉链接。
- 每条结果附论文级 `cite_link`（悬浮模式为 `[<sup>[n]</sup>](url "悬停内容")`，纯文本模式为 `[n]` / `(作者, 年)`）。
- 论文级 `references`：`ref_no`、`cite_link`、`reference`、`title`；悬浮模式不再重复输出同值的 `cite_mark`/`ref_mark`/`cite_id`，避免结果过大。
- 可直接复制到回答末尾的 `reference_block`。

首次检索时，如果存在多个文献库且用户尚未选择，支持 MCP elicitation 的客户端会**弹出可点击的文献库选项**：ZCode 等新协议客户端走 MRTR/InputRequests，Cherry Studio 等旧协议客户端走 elicitation/create；用户点击后，`larder_search` 自动继续检索。不支持 elicitation 的客户端会返回 `selection_required=true` 和库列表，agent 应把选项展示给用户，再调用 `larder_select_library`。
后续检索建议同时带 `kb=<id>`，以兼容无会话状态的客户端。

> 写入类操作（入库、删除）必须指定知识库；检索类操作在只有一个库时自动选择，多个库时先选择。

**如何触发让用户选库：**

1. 至少创建两个文献库/知识库（只有一个库时会自动选择，不会弹选项）。
2. 新建一个 MCP 会话，调用 `larder_search` 且不带 `kb`；支持 elicitation 的客户端会直接弹出选项。
3. 也可以主动调用 `larder_select_library` 且不传 `library`，同样会弹出选项。
4. 旧客户端会收到 `selection_required=true` 和库列表；agent 把列表展示给用户，用户再调用 `larder_select_library(library="<id>")`。

---

## Zotero 文献库

Larder 可以把 Zotero 的一个集合（文件夹）同步成独立知识库：

1. 在 Zotero **设置 → 高级** 勾选「允许其他应用与 Zotero 通信」（推荐；未开启时自动回退到 `zotero.sqlite` 只读快照）。
2. 打开 Larder 的「文献库」页。数据目录会**自动从 Zotero 的 `prefs.js` 读取并填入输入框**；读取不到时点「选择…」浏览文件夹或手动填写，然后点「检测集合」。
3. 选择集合 → 创建文献库。Larder 会在后台逐篇读取元数据与全文，完成解析、切块、嵌入，并写入引用信息。
4. 同步完成后可直接检索；MCP 首次检索会弹出文献库选项（旧客户端回退为列表）。
5. Zotero 新增文献/附件后，Larder 默认监听 `zotero.sqlite`/`storage` 变化并自动增量同步。

**PDF 不重复复制**：Zotero 文献库的 PDF 仍留在 Zotero，Larder 只保存解析后的 Markdown、切块和向量；
`documents.origin_path` 指向原始 PDF，因此「打开 PDF」「重新处理」仍然可用。普通知识库上传的 PDF
则复制到 `data/<知识库名>/pdf/`，其它源文件放在 `files/`。

### 解析后的 Markdown

`[zotero].md_mode` 控制解析 Markdown 的落盘位置：

| 模式 | 位置 | Zotero 词条下 |
| --- | --- | --- |
| `item`（默认） | `storage/<attachmentKey>/<论文标题>.md`，与 PDF 同目录 | 会创建 Markdown 子附件 |
| `larder` | `<Zotero 数据目录>/larder/<集合名>/<标题> [itemKey].md` | 不创建 |
| `off` | 不写文件，只保存在知识库数据库中 | 不创建 |

- `item` 模式需要 **Zotero 10+** 并开启本地 API；在「文献库」页点「授权写入 Zotero」，
  在 Zotero 弹窗选择「始终允许」。选“允许一次”只能写一次，批量挂载前会自动要求重新授权。Larder 会用 `linked_file` 把 Markdown 挂到对应词条下，
  不复制文件；如果当前 Zotero 不支持 `linked_file`，自动回退 `linked_url`。
- 授权成功后会自动把已有解析文件补挂到词条下；也可在文献库详情页点「修复词条附件」手动执行。该操作同时清理
  `data/<知识库>/image/` 中与 Zotero sidecar `image/` 重复的图片。
- 图片放在 Markdown 同目录的 `image/` 下；Markdown 中的图片引用为 `image/xxx`，可直接脱离 Larder 打开。
  旧版 sidecar 的 `images/` 目录会在下次同步时自动改名为 `image/`，引用同步改写。
- 如果解析文件已存在且不比 PDF 旧，同步会直接复用，不再调用版面解析；点「重新处理」或 PDF 更新后才重新解析。

`[zotero].parse_mode` 控制正文来源：

| 模式 | 行为 |
| --- | --- |
| `auto`（默认） | 优先用 PDF 版面解析（MinerU/Docling），保留标题层级、版式、表格与图片；失败回退 Zotero 全文 |
| `layout` | 强制 PDF 版面解析；失败即报错 |
| `zotero` | 只用 Zotero 全文缓存，速度快，但为纯文本、无图片 |

> 图片需要外部版面解析器（如 MinerU）。MinerU 结果里的 `images/` 会保存到
> 普通知识库的 `data/<知识库名>/image/<docID>/`；Zotero `item`/`larder` 模式的图片直接使用
> Markdown 同目录的 `image/`，预览与 ZIP 导出会自动回退到该目录，不再复制到知识库 `image/`。
> 只用 Zotero 全文缓存时只有纯文本，没有版式和图片。

### 引用与参考文献

- 参考文献格式：`gb-t-7714-numeric`（默认）、`gb-t-7714-author-date`、`apa`、`ieee`、`vancouver`、`mla`，在「设置 → 引用格式」按客户端 / agent 分别配置。
- 文内标记由参考文献格式决定：GB/T、IEEE 用 `[1]`，Vancouver 用 `(1)`，APA/MLA 用作者-年份；悬浮和纯文本使用同一套标准文内标记。
- 引用显示方式在「设置 → 引用格式」按客户端配置：
  - 悬浮链接：正文显示标准文内标记；链接可选且固定第一项，悬停按勾选顺序显示悬浮内容；
  - 纯文本：正文只显示标准文内标记，不附加链接、文献词条、文件名或原文；文末输出完整参考文献；
  - 两种模式都会在回答末尾输出参考文献列表，或调用工具生成 `.bib`。
- 悬浮内容字段：`link`（链接）、`reference`（文献词条）、`file`（文献文件名）、`text`（原文）。仅悬浮模式需要设置；链接固定第一项，其余可排序。
- 设置页预览：悬浮模式显示「正文预览 / 悬浮预览 / 文末参考文献预览」；纯文本模式只显示「正文预览 / 文末参考文献预览」。
- 「参考文献格式」已放到「设置 → 引用格式」，每个客户端 / agent 独立保存；切换平台不会覆盖其他平台的格式。默认格式只保存在 `[citation.profiles.default].style`；`[citation]` 下不再有 `version` 或全局 `style`，旧版这两个键会被读取器忽略。
- `larder_search` 会把同一篇论文的多个 chunk 去重成一个编号，并返回 `reference_block`。
- 悬浮模式正文保持内联 `[<sup>[n]</sup>](url "悬停内容")`，并压缩悬停内容长度；这样流式输出中途停止也能立即悬浮，短回答也不会因长 URL 超出输出预算而退化成纯 `[n]`。
- `larder_get_references` 可按样式重新生成参考文献与 `cite_link`；需要 BibTeX 时传 `include_bibtex=true`，或用 `larder_export_bibliography` 导出 `.bib`。
- `larder_export_bibliography` 写入 `data/exports/<kb>/references.bib`；也可直接访问
  `GET /api/kbs/<kb>/bibliography.bib` 下载。
- Markdown 客户端里的 `[1]` 链接指向 `/r/<kb>/<doc>` 引用卡片；卡片里的「在 Zotero 中打开」「打开 PDF」
  使用 `zotero://select/...` 与 `zotero://open-pdf/...?page=N` 深链。

### 常用接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET/PUT` | `/api/zotero/settings` | 读取/保存 Zotero 联动设置（密钥只返回掩码） |
| `POST` | `/api/zotero/authorize-write` | 授权 Zotero 10+ 本地写入，用于词条子附件 |
| `DELETE` | `/api/zotero/authorize-write` | 取消本地写入授权 |
| `GET` | `/api/zotero/collections` | 读取集合树（`data_dir` / `api_base` 可覆盖） |
| `POST` | `/api/zotero/libraries` | 从集合创建文献库（可 `sync_now`） |
| `GET` | `/api/kbs/{kb}/zotero/status` | 同步状态、成功/失败数量、最近任务 |
| `POST` | `/api/kbs/{kb}/zotero/sync` | 触发后台同步 |
| `POST` | `/api/kbs/{kb}/zotero/repair-attachments` | 把已有解析 Markdown 补挂到 Zotero 词条下 |
| `GET` | `/api/kbs/{kb}/library-items` | 文献元数据列表 |
| `GET` | `/api/kbs/{kb}/references?doc_ids=...&style=...` | 参考文献 Markdown + BibTeX |
| `GET` | `/api/kbs/{kb}/bibliography.bib` | 下载 `.bib` |
| `GET` | `/api/kbs/{kb}/documents/{id}/export.zip` | 导出解析 Markdown + `image/` 图片 |
| `POST` | `/api/kbs/{kb}/documents/{id}/open-folder` | 在资源管理器中定位 PDF/解析 Markdown |
| `GET` | `/r/{kb}/{doc}` | 引用卡片（悬浮链接点击进入） |
| `GET` | `/z/{kb}/{doc}` | 302 → `zotero://select/...` |
| `GET` | `/p/{kb}/{doc}?page=N` | 302 → `zotero://open-pdf/...?page=N` |

### 同步说明

- 默认按 Zotero `dateModified` 增量同步；内容未变会跳过。
- 默认监听 `zotero.sqlite` / `-wal` / `storage` 的修改时间：Zotero 拉入新文献或附件后，Larder 会自动触发同步（`watch = true`，默认 5 秒检测一次）。
- 启动时会先做一次增量同步，覆盖 Larder 关闭期间新增的文献。
- 已删除/移出集合的条目暂不自动删除索引，避免误删；可在文档页手动删除。
- 首次同步大量论文会消耗 embedding Token；建议先在设置里确认向量服务，或先用一个小集合试跑。
- 换 embedding 模型会触发维度冲突；需要新建文献库或重建向量索引。

---

## 引用格式

「设置 → 引用格式」先选择客户端 / agent，再分别设置该平台的参考文献格式和显示方式。

### 悬浮链接

正文显示**参考文献标准文内标记**：

```text
[1]       或       (Smith, 2020)
```

链接是可选字段，固定为第一项且不可调序；取消勾选“链接”后只显示文内标记，不再生成超链接，因此也没有悬停内容。
悬停内容按其余勾选字段的顺序显示（用户可勾选并上下排序）：

```text
【文献词条】Smith, J. ... [J]. Journal, 2020.
【文献文件名】paper.pdf
【原文】……
```

点击打开 `/r/<kb>/<doc>` 引用卡片。设置页同时显示正文预览、悬浮预览和文末参考文献预览。

### 纯文本

正文只显示参考文献标准规定的文内标记：

```text
[1]       或       (Smith, 2020)       或       (1)
```

纯文本模式不提供悬浮内容设置，也不会在行内附加链接、文献词条、文件名或原文；完整参考文献统一输出在文末。设置页只显示正文预览和文末参考文献预览。

ZCode 固定使用纯文本模式（客户端会移除 Markdown 链接），正文同样使用标准文内标记。

### 共同规则

- **两种模式都会在回答末尾输出完整参考文献列表**，或调用 `larder_export_bibliography` 生成 `.bib` 文件。
- 悬浮内容字段：`link`（链接）、`reference`（文献词条）、`file`（文献文件名）、`text`（原文）。仅悬浮模式需要设置。
- 文内标记只由参考文献格式决定：GB/T、IEEE 用 `[1]`，Vancouver 用 `(1)`，APA/MLA 用作者-年份；两种模式一致。
- 修改后立即自动保存并在服务端生效，同一 MCP 会话下一次工具调用即可使用新设置，无需新开对话；工具结果也会带上当前模式/格式/字段，避免模型沿用旧会话提示。首次为某个平台接入时，需要在该客户端重新注入一次 MCP 地址，使 URL 带上 `?client=<平台>`。

### ZCode 特别说明

ZCode 会移除 Markdown 链接，因此固定使用纯文本引用（数字格式仍为 `[1]`）。更新 Larder 后，请**新建一个 ZCode 会话**（或重启 ZCode）再测试，避免旧会话缓存旧的工具描述。

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

1. 使用当前版本（1.1.1+）；
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
│   ├── zotero/                 # Zotero 本地 API + zotero.sqlite 只读快照
│   ├── library/                # Zotero 集合→知识库同步、MD sidecar、同步任务
│   ├── cite/                   # GB/T 7714、APA、IEEE、Vancouver、BibTeX
│   ├── mcp/                    # MCP 工具、选库、论文级引用、日志短链
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
