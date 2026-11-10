package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	vec "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	_ "github.com/ncruces/go-sqlite3/driver"

	"larder/internal/model"
)

// docColumns 是 documents 表的完整列清单，供各查询复用。
const docColumns = `id, source, title, mime, hash, chunks, created_at,
	parse_status, parse_method, parse_note, embed_status, embed_model, embed_dim, rerank_status,
	origin_path, origin_type, embed_note, updated_at`

// SQLite 是 Store 的单文件实现。
type SQLite struct {
	db  *sql.DB
	dim int
}

// Open 打开（或创建）数据库并完成建表。
func Open(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	// 单文件库，串行写入足够，避免并发写锁。
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	s := &SQLite{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.loadDim(); err != nil {
		db.Close()
		return nil, err
	}
	// 旧库没有用量历史：按文档/切块估算一次，之后由真实请求累积。
	_ = s.backfillUsage()
	return s, nil
}

func (s *SQLite) migrate() error {
	stmts := []string{
		`create table if not exists meta(
			key text primary key,
			value text not null)`,
		`create table if not exists documents(
			id text primary key,
			source text not null,
			title text not null,
			mime text not null,
			hash text not null,
			chunks integer not null default 0,
			created_at text not null,
			parse_status text not null default '',
			parse_method text not null default '',
			parse_note text not null default '',
			embed_status text not null default '',
			embed_model text not null default '',
			embed_dim integer not null default 0,
			rerank_status text not null default '',
			origin_path text not null default '',
			origin_type text not null default '',
			embed_note text not null default '',
			updated_at text not null default '',
			markdown text not null default '')`,
		`create index if not exists idx_documents_source_hash on documents(source, hash)`,
		`create table if not exists chunks(
			id integer primary key autoincrement,
			doc_id text not null,
			ordinal integer not null,
			heading text not null default '',
			text text not null)`,
		`create index if not exists idx_chunks_doc on chunks(doc_id)`,
		`create table if not exists usage_events(
			id integer primary key autoincrement,
			ts text not null,
			kind text not null,
			model text not null default '',
			tokens integer not null default 0,
			chars integer not null default 0,
			status text not null default 'ok')`,
		`create index if not exists idx_usage_ts on usage_events(ts)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("建表失败: %w\nSQL: %s", err, q)
		}
	}
	// 兼容旧库：为已存在的 documents 表补齐状态列。
	alters := []struct{ col, def string }{
		{"parse_status", "''"}, {"parse_method", "''"}, {"parse_note", "''"},
		{"embed_status", "''"}, {"embed_model", "''"}, {"embed_dim", "0"}, {"rerank_status", "''"},
		{"origin_path", "''"}, {"origin_type", "''"}, {"embed_note", "''"}, {"updated_at", "''"}, {"markdown", "''"},
	}
	for _, a := range alters {
		if err := s.addColumnIfMissing("documents", a.col, "text", a.def); err != nil {
			return err
		}
	}
	return nil
}

// addColumnIfMissing 在列不存在时执行 ALTER TABLE ADD COLUMN。
func (s *SQLite) addColumnIfMissing(table, col, typ, def string) error {
	var n int
	err := s.db.QueryRow(`select count(*) from pragma_table_info(?) where name = ?`, table, col).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if _, err := s.db.Exec(fmt.Sprintf(`alter table %s add column %s %s not null default %s`, table, col, typ, def)); err != nil {
		return fmt.Errorf("为 %s 添加列 %s 失败: %w", table, col, err)
	}
	return nil
}

func (s *SQLite) loadDim() error {
	var v string
	err := s.db.QueryRow(`select value from meta where key = 'vec_dim'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	dim, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("meta.vec_dim 非法: %w", err)
	}
	s.dim = dim
	return nil
}

// Dim 返回当前向量维度。
func (s *SQLite) Dim() int { return s.dim }

// EnsureDim 确保向量表存在且维度一致。
func (s *SQLite) EnsureDim(dim int) error {
	if dim <= 0 {
		return errors.New("维度必须为正")
	}
	if s.dim == dim {
		// 表已就绪。
		return s.ensureVecTable(dim, false)
	}
	if s.dim != 0 && s.dim != dim {
		return fmt.Errorf("向量维度冲突：库中为 %d，当前模型为 %d；请更换数据文件或清空索引后重试", s.dim, dim)
	}

	// 首次初始化。
	if err := s.ensureVecTable(dim, true); err != nil {
		return err
	}
	if _, err := s.db.Exec(`insert into meta(key, value) values('vec_dim', ?)
		on conflict(key) do update set value = excluded.value`, strconv.Itoa(dim)); err != nil {
		return err
	}
	s.dim = dim
	return nil
}

func (s *SQLite) ensureVecTable(dim int, create bool) error {
	if !create {
		return nil
	}
	q := fmt.Sprintf(`create virtual table if not exists vec_chunks using vec0(
		chunk_id integer primary key,
		embedding float[%d])`, dim)
	if _, err := s.db.Exec(q); err != nil {
		return fmt.Errorf("创建向量表失败: %w", err)
	}
	return nil
}

// FindDocByHash 按来源与正文哈希判重。
func (s *SQLite) FindDocByHash(ctx context.Context, source, hash string) (model.Document, bool, error) {
	row := s.db.QueryRowContext(ctx, `select `+docColumns+`
		from documents where source = ? and hash = ? order by created_at desc limit 1`, source, hash)
	return scanDoc(row)
}

// FindDocBySource 返回同一来源最近更新的一条文档。
func (s *SQLite) FindDocBySource(ctx context.Context, source string) (model.Document, bool, error) {
	row := s.db.QueryRowContext(ctx, `select `+docColumns+`
		from documents where source = ? order by updated_at desc, created_at desc limit 1`, source)
	return scanDoc(row)
}

// UpsertDocument 写入或更新文档记录。
func (s *SQLite) UpsertDocument(ctx context.Context, doc Document) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `insert into documents(id, source, title, mime, hash, chunks, created_at,
			parse_status, parse_method, parse_note, embed_status, embed_model, embed_dim, rerank_status,
			origin_path, origin_type, embed_note, updated_at, markdown)
		values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(id) do update set
			source=excluded.source, title=excluded.title, mime=excluded.mime,
			hash=excluded.hash, chunks=excluded.chunks,
			parse_status=excluded.parse_status, parse_method=excluded.parse_method, parse_note=excluded.parse_note,
			embed_status=excluded.embed_status, embed_model=excluded.embed_model, embed_dim=excluded.embed_dim,
			rerank_status=excluded.rerank_status, origin_path=excluded.origin_path, origin_type=excluded.origin_type,
			embed_note=excluded.embed_note, updated_at=excluded.updated_at,
			markdown=case when excluded.markdown <> '' then excluded.markdown else documents.markdown end`,
		doc.ID, doc.Source, doc.Title, doc.MIME, doc.Hash, doc.Chunks, now,
		doc.ParseStatus, doc.ParseMethod, doc.ParseNote, doc.EmbedStatus, doc.EmbedModel, doc.EmbedDim, doc.RerankStatus,
		doc.OriginPath, doc.OriginType, doc.EmbedNote, now, doc.Markdown)
	if err != nil {
		return fmt.Errorf("写入文档失败: %w", err)
	}
	return nil
}

// SetDocumentStatus 只更新处理状态字段，保留来源、标题等元信息。
func (s *SQLite) SetDocumentStatus(ctx context.Context, docID string, st DocStatus) error {
	_, err := s.db.ExecContext(ctx, `update documents set
			parse_status=?, parse_method=?, parse_note=?,
			embed_status=?, embed_model=?, embed_note=?, embed_dim=?,
			rerank_status=?, chunks=?, updated_at=?
		where id=?`,
		st.ParseStatus, st.ParseMethod, st.ParseNote,
		st.EmbedStatus, st.EmbedModel, st.EmbedNote, st.EmbedDim,
		st.RerankStatus, st.Chunks, time.Now().UTC().Format(time.RFC3339), docID)
	if err != nil {
		return fmt.Errorf("更新文档状态失败: %w", err)
	}
	return nil
}

// DeleteDocumentsByOriginExcept 删除同一原始文件/URL 的其它旧版本，避免重复展示。
func (s *SQLite) DeleteDocumentsByOriginExcept(ctx context.Context, originPath, keepID string) error {
	if originPath == "" {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `select id from documents where origin_path = ? and id <> ?`, originPath, keepID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := s.DeleteDocument(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceChunks 在一个事务内替换文档的所有切块与向量。
func (s *SQLite) ReplaceChunks(ctx context.Context, docID string, chunks []model.Chunk, vectors [][]float32) error {
	if len(chunks) != len(vectors) {
		return fmt.Errorf("切块数(%d)与向量数(%d)不一致", len(chunks), len(vectors))
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 删除旧切块及其向量。
	if err := s.deleteChunksTx(ctx, tx, docID); err != nil {
		return err
	}

	insChunk, err := tx.PrepareContext(ctx, `insert into chunks(doc_id, ordinal, heading, text) values(?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insChunk.Close()

	insVec, err := tx.PrepareContext(ctx, `insert into vec_chunks(chunk_id, embedding) values(?, ?)`)
	if err != nil {
		return err
	}
	defer insVec.Close()

	for i, c := range chunks {
		res, err := insChunk.ExecContext(ctx, docID, c.Ordinal, c.Heading, c.Text)
		if err != nil {
			return fmt.Errorf("写入切块失败: %w", err)
		}
		chunkID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		blob, err := vec.SerializeFloat32(vectors[i])
		if err != nil {
			return fmt.Errorf("序列化向量失败: %w", err)
		}
		if _, err := insVec.ExecContext(ctx, chunkID, blob); err != nil {
			return fmt.Errorf("写入向量失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *SQLite) deleteChunksTx(ctx context.Context, tx *sql.Tx, docID string) error {
	rows, err := tx.QueryContext(ctx, `select id from chunks where doc_id = ?`, docID)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()

	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `delete from vec_chunks where chunk_id = ?`, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `delete from chunks where doc_id = ?`, docID); err != nil {
		return err
	}
	return nil
}

// DeleteDocument 删除文档、切块与向量。
func (s *SQLite) DeleteDocument(ctx context.Context, docID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := s.deleteChunksTx(ctx, tx, docID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from documents where id = ?`, docID); err != nil {
		return err
	}
	return tx.Commit()
}

// Search 做向量 KNN 召回。
func (s *SQLite) Search(ctx context.Context, vector []float32, k int) ([]Hit, error) {
	if s.dim == 0 {
		// 还没有任何向量：返回空结果，而不是让整次检索报错。
		return []Hit{}, nil
	}
	if len(vector) != s.dim {
		return nil, fmt.Errorf("查询向量维度 %d 与索引维度 %d 不一致", len(vector), s.dim)
	}
	if k <= 0 {
		return nil, nil
	}

	blob, err := vec.SerializeFloat32(vector)
	if err != nil {
		return nil, err
	}

	q := `select c.id, c.doc_id, d.title, d.source, c.ordinal, c.heading, c.text, v.distance
		from vec_chunks v
		join chunks c on c.id = v.chunk_id
		join documents d on d.id = c.doc_id
		where v.embedding match ? and k = ?
		order by v.distance`
	rows, err := s.db.QueryContext(ctx, q, blob, k)
	if err != nil {
		return nil, fmt.Errorf("向量检索失败: %w", err)
	}
	defer rows.Close()

	var hits []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ChunkID, &h.DocID, &h.DocTitle, &h.Source, &h.Ordinal, &h.Heading, &h.Text, &h.Distance); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// GetChunks 按 ID 批量取切块。
func (s *SQLite) GetChunks(ctx context.Context, ids []int64) (map[int64]model.Chunk, error) {
	out := make(map[int64]model.Chunk, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	q := `select id, doc_id, ordinal, heading, text from chunks where id in (` + placeholders + `)`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var c model.Chunk
		if err := rows.Scan(&c.ID, &c.DocID, &c.Ordinal, &c.Heading, &c.Text); err != nil {
			return nil, err
		}
		out[c.ID] = c
	}
	return out, rows.Err()
}

// ListDocuments 列出全部文档（最近处理的在前）。
func (s *SQLite) ListDocuments(ctx context.Context) ([]model.Document, error) {
	rows, err := s.db.QueryContext(ctx, `select `+docColumns+`
		from documents order by updated_at desc, created_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	docs := make([]model.Document, 0)
	for rows.Next() {
		doc, err := scanDocRow(rows)
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}

// GetDocument 按 ID 取文档。
func (s *SQLite) GetDocument(ctx context.Context, id string) (model.Document, bool, error) {
	row := s.db.QueryRowContext(ctx, `select `+docColumns+`
		from documents where id = ?`, id)
	return scanDoc(row)
}

// GetDocumentMarkdown 返回文档解析后的完整 Markdown。
func (s *SQLite) GetDocumentMarkdown(ctx context.Context, docID string) (string, error) {
	var md string
	err := s.db.QueryRowContext(ctx, `select markdown from documents where id = ?`, docID).Scan(&md)
	if err != nil {
		return "", err
	}
	return md, nil
}

// ListChunks 按序号返回某文档的全部切块。
func (s *SQLite) ListChunks(ctx context.Context, docID string) ([]model.Chunk, error) {
	rows, err := s.db.QueryContext(ctx, `select id, doc_id, ordinal, heading, text
		from chunks where doc_id = ? order by ordinal`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.Chunk, 0)
	for rows.Next() {
		var c model.Chunk
		if err := rows.Scan(&c.ID, &c.DocID, &c.Ordinal, &c.Heading, &c.Text); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountChunks 统计切块数。
func (s *SQLite) CountChunks(ctx context.Context, docID string) (int, error) {
	var n int
	var err error
	if docID == "" {
		err = s.db.QueryRowContext(ctx, `select count(*) from chunks`).Scan(&n)
	} else {
		err = s.db.QueryRowContext(ctx, `select count(*) from chunks where doc_id = ?`, docID).Scan(&n)
	}
	return n, err
}

// AddUsage 追加一条用量记录。
func (s *SQLite) AddUsage(ctx context.Context, ev UsageEvent) error {
	if ev.TS.IsZero() {
		ev.TS = time.Now().UTC()
	}
	if ev.Status == "" {
		ev.Status = "ok"
	}
	_, err := s.db.ExecContext(ctx, `insert into usage_events(ts, kind, model, tokens, chars, status)
		values(?, ?, ?, ?, ?, ?)`,
		ev.TS.UTC().Format(time.RFC3339), ev.Kind, ev.Model, ev.Tokens, ev.Chars, ev.Status)
	if err != nil {
		return fmt.Errorf("写入用量记录失败: %w", err)
	}
	return nil
}

// UsageSummary 汇总 since 之后的用量。
func (s *SQLite) UsageSummary(ctx context.Context, since time.Time) (UsageSummary, error) {
	q := `select ts, kind, model, tokens, chars, status from usage_events`
	args := []any{}
	if !since.IsZero() {
		q += ` where ts >= ?`
		args = append(args, since.UTC().Format(time.RFC3339))
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return UsageSummary{}, err
	}
	defer rows.Close()

	var out UsageSummary
	days := map[string]*UsageDay{}
	models := map[string]*UsageModel{}
	for rows.Next() {
		var ts, kind, model, status string
		var tokens, chars int64
		if err := rows.Scan(&ts, &kind, &model, &tokens, &chars, &status); err != nil {
			return UsageSummary{}, err
		}
		day := ts
		if len(day) > 10 {
			day = day[:10]
		}
		d := days[day]
		if d == nil {
			d = &UsageDay{Day: day}
			days[day] = d
		}
		switch kind {
		case "parse":
			out.ParseCount++
			if status == "error" {
				out.ParseFailed++
			}
			out.ParseChars += chars
			d.Parse++
		case "embed":
			out.EmbedCount++
			out.EmbedTokens += tokens
			d.EmbedTokens += tokens
		case "rerank":
			out.RerankCount++
			out.RerankTokens += tokens
			d.RerankTokens += tokens
		}
		d.TotalTokens = d.EmbedTokens + d.RerankTokens

		key := kind + "::" + model
		m := models[key]
		if m == nil {
			m = &UsageModel{Kind: kind, Model: model}
			models[key] = m
		}
		m.Count++
		m.Tokens += tokens
	}
	if err := rows.Err(); err != nil {
		return UsageSummary{}, err
	}
	out.TotalTokens = out.EmbedTokens + out.RerankTokens
	out.ByDay = make([]UsageDay, 0, len(days))
	for _, d := range days {
		out.ByDay = append(out.ByDay, *d)
	}
	sort.Slice(out.ByDay, func(i, j int) bool { return out.ByDay[i].Day < out.ByDay[j].Day })
	out.ByModel = make([]UsageModel, 0, len(models))
	for _, m := range models {
		out.ByModel = append(out.ByModel, *m)
	}
	sort.Slice(out.ByModel, func(i, j int) bool {
		if out.ByModel[i].Tokens != out.ByModel[j].Tokens {
			return out.ByModel[i].Tokens > out.ByModel[j].Tokens
		}
		return out.ByModel[i].Count > out.ByModel[j].Count
	})
	return out, nil
}

// backfillUsage 为旧库生成一次估算历史（每个库只做一次）。
func (s *SQLite) backfillUsage() error {
	ctx := context.Background()
	if v, ok, _ := s.GetMeta(ctx, "usage_backfilled"); ok && v == "1" {
		return nil
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `select count(*) from usage_events`).Scan(&n); err == nil && n == 0 {
		rows, err := s.db.QueryContext(ctx, `select id, created_at, parse_status, parse_method,
			embed_status, embed_model, markdown from documents`)
		if err == nil {
			type docUsage struct {
				id, created, parseStatus, parseMethod, embedStatus, embedModel, markdown string
			}
			var docs []docUsage
			for rows.Next() {
				var d docUsage
				if err := rows.Scan(&d.id, &d.created, &d.parseStatus, &d.parseMethod,
					&d.embedStatus, &d.embedModel, &d.markdown); err != nil {
					break
				}
				docs = append(docs, d)
			}
			rows.Close()
			for _, d := range docs {
				ts, err := time.Parse(time.RFC3339, d.created)
				if err != nil {
					ts = time.Now().UTC()
				}
				chars := int64(len([]rune(d.markdown)))
				if chars == 0 {
					_ = s.db.QueryRowContext(ctx, `select coalesce(sum(length(text)), 0) from chunks where doc_id = ?`, d.id).Scan(&chars)
				}
				if d.parseStatus == "ok" {
					_ = s.AddUsage(ctx, UsageEvent{TS: ts, Kind: "parse", Model: d.parseMethod, Chars: chars, Status: "ok"})
				} else if d.parseStatus == "error" {
					_ = s.AddUsage(ctx, UsageEvent{TS: ts, Kind: "parse", Model: d.parseMethod, Status: "error"})
				}
				tokens := chars / 3
				if tokens < 1 {
					tokens = 1
				}
				if d.embedStatus == "ok" {
					_ = s.AddUsage(ctx, UsageEvent{TS: ts, Kind: "embed", Model: d.embedModel, Tokens: tokens, Status: "ok"})
				} else if d.embedStatus == "error" {
					_ = s.AddUsage(ctx, UsageEvent{TS: ts, Kind: "embed", Model: d.embedModel, Status: "error"})
				}
			}
		}
	}
	return s.SetMeta(ctx, "usage_backfilled", "1")
}

// Close 关闭数据库。
func (s *SQLite) Close() error { return s.db.Close() }

// GetMeta 读取 meta 表中的键值。
func (s *SQLite) GetMeta(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `select value from meta where key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetMeta 写入 meta 表键值（存在则覆盖）。
func (s *SQLite) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `insert into meta(key, value) values(?, ?)
		on conflict(key) do update set value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("写入 meta 失败: %w", err)
	}
	return nil
}

// rowScanner 抽象 *sql.Row 与 *sql.Rows 的共同 Scan。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanDoc(r rowScanner) (model.Document, bool, error) {
	doc, err := scanDocRow(r)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Document{}, false, nil
	}
	if err != nil {
		return model.Document{}, false, err
	}
	return doc, true, nil
}

func scanDocRow(r rowScanner) (model.Document, error) {
	var doc model.Document
	var created, updated string
	if err := r.Scan(&doc.ID, &doc.Source, &doc.Title, &doc.MIME, &doc.Hash, &doc.Chunks, &created,
		&doc.ParseStatus, &doc.ParseMethod, &doc.ParseNote,
		&doc.EmbedStatus, &doc.EmbedModel, &doc.EmbedDim, &doc.RerankStatus,
		&doc.OriginPath, &doc.OriginType, &doc.EmbedNote, &updated); err != nil {
		return model.Document{}, err
	}
	if t, err := time.Parse(time.RFC3339, created); err == nil {
		doc.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339, updated); err == nil {
		doc.UpdatedAt = t
	}
	return doc, nil
}
