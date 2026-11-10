# Larder 架构设计

本文描述 Larder 的数据模型、入库/检索流水线、分层职责与扩展点。

---

## 1. 总体结构

```
                 ┌──────────────────────────────────────────────┐
                 │                  main.go (根目录)             │
                 │  解析参数 → config.Load → app.New → server.Run │
                 └───────────────┬──────────────────────────────┘
                                 │
                        ┌────────▼─────────┐
                        │   route.Handler   │  组合根：MCP + REST
                        └───┬──────────┬───┘
                 ┌──────────▼──┐   ┌───▼─────────┐
                 │  mcp.Server │   │  api.Handler │
                 │ (工具协议)   │   │ (REST)       │
                 └──────┬──────┘   └──────┬──────┘
                        └───────┬─────────┘
                        ┌───────▼────────┐
                        │      app.App    │  持有全部依赖
                        └───┬────────┬────┘
                  ┌─────────▼──┐  ┌──▼──────────┐
                  │ ingest     │  │  search      │
                  │ 编排入库    │  │  编排检索     │
                  └──┬──┬──┬───┘  └──┬──────┬────┘
                     │  │  │         │      │
        ┌────────────▼┐ │ ┌▼───────┐ │   ┌──▼────────┐
        │  parse      │ │ │ embed  │ │   │ rerank     │
        └─────────────┘ │ └────────┘ │   └───────────┘
                        │            │
                     ┌──▼────────────▼──┐
                     │      store       │  SQLite + sqlite-vec
                     └──────────────────┘
```

依赖方向单向向下：`cmd → route → {api, mcp} → app → {ingest, search} → {store, provider/*} → model`。
`ingest` / `search` 只依赖接口，不依赖具体实现，方便替换与测试。

---

## 2. 数据模型

单文件 SQLite，包含三类对象：

### 普通表 `documents`

| 列 | 含义 |
| --- | --- |
| `id` | UUID 主键 |
| `source` | 来源（文件绝对路径或 URL），判重维度之一 |
| `title` | 标题（可用入参覆盖，否则从内容/文件名推断） |
| `mime` | 解析后 MIME |
| `hash` | 原始字节 sha256，判重维度之一 |
| `chunks` | 切块数 |
| `created_at` / `updated_at` | 入库时间 / 最近处理时间 |
| `parse_status` / `parse_method` / `parse_note` | 解析状态、实际解析器、页数或失败原因 |
| `embed_status` / `embed_model` / `embed_dim` / `embed_note` | 向量化状态、模型、维度、失败原因 |
| `rerank_status` | 入库时重排是否启用（检索时实际应用） |
| `origin_path` / `origin_type` | 原始文件绝对路径、URL 或上传缓存路径；`file`/`url`/`upload` |
| `markdown` | 解析后的完整 Markdown，供界面预览原文；旧文档为空时由切块重组 |

### 普通表 `chunks`

| 列 | 含义 |
| --- | --- |
| `id` | 自增主键 |
| `doc_id` | 外键 → documents.id |
| `ordinal` | 块在文档内的序号（0 起） |
| `heading` | 所属标题路径，如 `安装 > Windows` |
| `text` | 块正文（Markdown） |

### 虚拟表 `vec_chunks`（sqlite-vec `vec0`）

| 列 | 含义 |
| --- | --- |
| `chunk_id` | 对应 chunks.id |
| `embedding` | `float[dim]` 向量 |

**维度协商**：`meta.vec_dim` 记录首次入库时探测到的维度。`store.EnsureDim(dim)`
在维度为 0 时以该维度建虚拟表；若已有维度与请求不符则返回明确错误，防止脏写。

**外键级联**：删除文档时一并清理其 `chunks` 与 `vec_chunks` 行。

---

## 3. 入库流水线（`internal/ingest`）

```
来源(文件/URL/字节)
   │
   ├─(1) 空内容校验；计算原始字节 sha256；上传内容缓存到 data/.uploads/<hash><ext>
   │
   ├─(2) 判重：FindDocByHash(source, hash) 命中且解析/嵌入都成功且非 refresh → Skipped
   │        上次失败的记录会复用，避免重复堆积
   │
   ├─(3) 先落一条 pending 文档（parse_status=running, embed_status=pending）
   │        让前端立刻看到文件与环节状态
   │
   ├─(4) 解析：parse.Parser.Parse → Markdown + 标题
   │        plain：本地文本/代码/文字版 PDF/DOCX/PPTX/XLSX/EPUB/HTML
   │        mineru/docling/tika：Hybrid 本地优先，本地失败才上传外部 API
   │        失败：记录 parse_status=error + parse_note，返回 doc_id（HTTP 422）
   │
   ├─(5) 切块：chunk.Split(Markdown)
   │        按标题层级分节，超长按 rune 上限二次切分并带 overlap
   │        代码围栏整体保留，不会被切开
   │
   ├─(6) 向量化：embed.Embedder.Embed(texts)
   │        文本 = heading + "\n\n" + text（把标题路径拼进正文提升命中）
   │        失败：记录 embed_status=error + embed_note
   │
   ├─(7) 维度：store.EnsureDim(len(vec[0]))（首次建表）
   │
   └─(8) 落库（事务）：ReplaceChunks(chunks + vectors) + UpsertDocument 最终状态
```

要点：

- **判重在解析前**：用原始字节哈希，命中缓存时连解析都省掉。
- **失败也落库**：解析/嵌入失败不会“消失”，知识库与文档列表能看到原因，配置好服务后
  `POST /api/kbs/{id}/documents/{docId}/reprocess` 用已保存的原文件/URL 重试。
- **切块与标题路径**：`heading` 既用于入库文本，也用于检索结果引用。
- **模型路径**：`embedText` 把标题树拼进被向量化的文本，是提升召回质量的低成本手段。

---

## 4. 检索流水线（`internal/search`）

```
query
  │
  ├─(1) Embed(query) → 查询向量
  │
  ├─(2) store.Search(vec, CandidateK) → 向量 KNN 召回候选（按距离升序）
  │
  ├─(3) 若启用 rerank：
  │        rerank.Rerank(query, docs) 交叉打分
  │        score = rerank 分；按分数降序 → TopK
  │        重排失败：退回向量结果，返回 rerank_error，不让检索整体不可用
  │     否则：
  │        score = 1 / (1 + distance)（0–1，距离越小分越高，天然有序）→ TopK
  │
  └─(4) 返回 Result{ Hits, Reranked, RerankError }；每个命中带
                        chunk_id, doc_id, doc_title, source, heading, text,
                        score, vector_distance, rerank_score
```

- **两段式**：向量召回保证效率，重排保证精度；`CandidateK` 一般取 `TopK` 的若干倍。
- **无重排降级**：未配置重排时用负距离作为得分，直接截断，不做额外排序。
- **引用可追溯**：每个命中都带 `doc_title / source / heading`，满足“可溯源”要求。

---

## 5. MCP 与 REST

### MCP（`internal/mcp`）

- 使用官方 `modelcontextprotocol/go-sdk`，以 **Streamable HTTP**、`Stateless` 模式挂载在
  `server.mcp_path`（默认 `/mcp`）。
- 工具通过泛型 `mcp.AddTool[In, Out]` 注册，`In/Out` 用 `jsonschema` tag 自动生成 schema。
- 每个工具既返回人类可读文本（`Content`），也返回结构化 `Out`，便于客户端程序化使用。
- 工具集：`larder_list_kbs` / `larder_search` / `larder_ingest_file` / `larder_ingest_url` /
  `larder_list_documents` / `larder_get_document` / `larder_delete_document`。

### REST（`internal/api`）

- Go 1.22+ 的方法+路径路由（`GET /api/documents/{id}`、`PathValue`）。
- 上传走 `multipart/form-data`，其余为 JSON（`DisallowUnknownFields`）。
- 统一错误响应 `{"error": "..."}`；入库失败额外返回已落库的 `document` 与 `doc_id`（HTTP 422）。
- 知识库级设置：`GET/PUT /api/kbs/{id}/settings`；重新处理：`POST /api/kbs/{id}/documents/{docId}/reprocess`；原文/Chunks：`GET /api/kbs/{id}/documents/{docId}/content`；原始文件：`GET /api/kbs/{id}/documents/{docId}/file`。

`internal/route` 是唯一的组合根：把 `/mcp` 交给 MCP 处理器，其余路径交给 REST。

---

## 6. 分层职责

| 层 | 职责 | 约束 |
| --- | --- | --- |
| `main.go`（根目录） | 读配置、装配依赖、启动、等信号 | 不写业务逻辑 |
| `route` | 组合 REST 与 MCP | 只做装配与中间件 |
| `api` / `mcp` | 协议接入：解析请求、调用服务、编码响应 | 不写 SQL、不写业务规则 |
| `app` | 依赖容器与生命周期 | 由配置构造具体实现 |
| `ingest` / `search` | 业务编排（流水线） | 只依赖接口 |
| `store` | 持久化：SQLite + 向量 | 实现 `Store` 接口 |
| `provider/*` | 外部能力适配（解析/向量/重排） | 实现各自接口 |
| `model` | 纯数据结构 | 不依赖内部包 |

---

## 7. 扩展点

- **换向量模型**：实现 `embed.Embedder`，或直接用 OpenAI 兼容服务改配置。换维度需新建知识库。
- **加解析器**：在 `internal/provider/parse` 增加实现并在 `services.BuildParser` 注册分支。
- **加重排**：实现 `rerank.Reranker`，配置 `rerank.enabled=true`。
- **换存储**：实现 `store.Store` 接口即可替换 SQLite（例如 PostgreSQL + pgvector）。
- **加工具**：在 `internal/mcp` 用 `AddTool` 注册即可，无需改动路由。

---

## 8. 配置与维度一致性

- 配置优先级：**默认值 < TOML < 环境变量**（`internal/config`）。
- 程序优先读取 exe 同级 `larder.toml`，首次运行自动生成；数据默认在 exe 同级 `data/`。
- `embed.dim` 是提示值；每次 embedding 成功后以服务实际返回的维度为准。
- 首次调用 embedding 后写入 `meta.vec_dim`；后续模型维度与库内维度不一致时 `EnsureDim` 报错。
- 判重键为 `(source, hash)`：同一文件内容不变则跳过；内容变化且旧记录成功时，成功入库后会清理
  同一 `origin_path` 的旧版本。
