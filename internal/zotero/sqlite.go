package zotero

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	// 使用带 sqlite-vec 的 SQLite 构建，避免与知识库存储包的驱动注册顺序冲突。
	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	_ "github.com/ncruces/go-sqlite3/driver"
)

// ensureSQLite 打开 zotero.sqlite 的只读快照。
// Zotero 运行时数据库处于 WAL 模式并带锁，直接读容易 database is locked；
// 复制 db + wal + shm 到临时目录再读，既能拿到未 checkpoint 的帧，也不影响 Zotero。
func (c *Client) ensureSQLite() error {
	if c.db != nil {
		return nil
	}
	dbPath := filepath.Join(c.cfg.DataDir, "zotero.sqlite")
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("找不到 Zotero 数据库 %s：请先在设置中填写正确的 Zotero 数据目录", dbPath)
	}
	tmp, err := os.MkdirTemp("", "larder-zotero-*")
	if err != nil {
		return err
	}
	c.tempDir = tmp
	dst := filepath.Join(tmp, "zotero.sqlite")
	if err := copyFile(dbPath, dst); err != nil {
		return fmt.Errorf("复制 Zotero 数据库失败: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		src := dbPath + suffix
		if _, err := os.Stat(src); err == nil {
			if err := copyFile(src, dst+suffix); err != nil {
				return fmt.Errorf("复制 Zotero %s 失败: %w", suffix, err)
			}
		}
	}
	db, err := sql.Open("sqlite3", dst)
	if err != nil {
		return fmt.Errorf("打开 Zotero 数据库失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`pragma query_only = on`); err != nil {
		_ = db.Close()
		return fmt.Errorf("设置只读模式失败: %w", err)
	}
	c.db = db
	c.openedAt = time.Now()
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func (c *Client) dbLibraryID() int {
	id := strings.TrimSpace(c.cfg.LibraryID)
	if id == "" || id == "0" {
		return 1 // Zotero 数据库里个人库通常是 1，本地 API 里是 users/0
	}
	n, _ := strconv.Atoi(id)
	if n <= 0 {
		return 1
	}
	return n
}

func (c *Client) collectionsSQLite(ctx context.Context) ([]Collection, error) {
	if err := c.ensureSQLite(); err != nil {
		return nil, err
	}
	rows, err := c.db.QueryContext(ctx, `
		select c.key, c.collectionName, coalesce(p.key, ''), c.libraryID
		from collections c
		left join collections p on p.collectionID = c.parentCollectionID
		where c.libraryID = ?
		order by c.collectionName`, c.dbLibraryID())
	if err != nil {
		return nil, fmt.Errorf("读取 Zotero 集合失败: %w", err)
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		var col Collection
		var libID int
		if err := rows.Scan(&col.Key, &col.Name, &col.ParentKey, &libID); err != nil {
			return nil, err
		}
		col.LibraryID = strconv.Itoa(libID)
		out = append(out, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	// 个别 Zotero 版本的个人库 libraryID 不是 1：退化为不过滤库。
	if len(out) == 0 {
		rows2, err := c.db.QueryContext(ctx, `
			select c.key, c.collectionName, coalesce(p.key, ''), c.libraryID
			from collections c
			left join collections p on p.collectionID = c.parentCollectionID
			order by c.collectionName`)
		if err != nil {
			return nil, err
		}
		defer rows2.Close()
		for rows2.Next() {
			var col Collection
			var libID int
			if err := rows2.Scan(&col.Key, &col.Name, &col.ParentKey, &libID); err != nil {
				return nil, err
			}
			col.LibraryID = strconv.Itoa(libID)
			out = append(out, col)
		}
		if err := rows2.Err(); err != nil {
			return nil, err
		}
	}
	// 数量统计（失败不影响集合列表）。
	for i := range out {
		_ = c.db.QueryRowContext(ctx, `
			select count(distinct ci.itemID)
			from collectionItems ci
			join items i on i.itemID = ci.itemID
			join itemTypes it on it.itemTypeID = i.itemTypeID
			where ci.collectionID = (select collectionID from collections where key = ?)
			  and it.typeName not in ('attachment','note','annotation')`,
			out[i].Key).Scan(&out[i].ItemCount)
	}
	return out, nil
}

type sqliteItemRow struct {
	itemID       int64
	key          string
	version      int
	dateModified string
	itemType     string
}

func (c *Client) collectionItemsSQLite(ctx context.Context, collectionKey string, includeSub bool) ([]Item, error) {
	if err := c.ensureSQLite(); err != nil {
		return nil, err
	}
	prefix := ""
	where := `ci.collectionID = (select collectionID from collections where key = ?)`
	args := []any{collectionKey}
	if includeSub {
		prefix = `with recursive sub(collectionID) as (
			select collectionID from collections where key = ?
			union all
			select c.collectionID from collections c join sub on c.parentCollectionID = sub.collectionID
		) `
		where = `ci.collectionID in (select collectionID from sub)`
	}
	q := prefix + `
		select distinct i.itemID, i.key, i.version, i.dateModified, it.typeName
		from collectionItems ci
		join items i on i.itemID = ci.itemID
		join itemTypes it on it.itemTypeID = i.itemTypeID
		where ` + where + `
		  and it.typeName not in ('attachment','note','annotation')
		  and not exists (select 1 from deletedItems d where d.itemID = i.itemID)
		order by i.dateModified desc`
	rows, err := c.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("读取 Zotero 集合条目失败: %w", err)
	}
	defer rows.Close()
	var raw []sqliteItemRow
	var ids []int64
	for rows.Next() {
		var r sqliteItemRow
		if err := rows.Scan(&r.itemID, &r.key, &r.version, &r.dateModified, &r.itemType); err != nil {
			return nil, err
		}
		raw = append(raw, r)
		ids = append(ids, r.itemID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return []Item{}, nil
	}
	fields := c.loadFields(ctx, ids)
	creators := c.loadCreators(ctx, ids)
	atts := c.loadAttachments(ctx, ids)
	out := make([]Item, 0, len(raw))
	for _, r := range raw {
		f := fields[r.itemID]
		it := Item{
			Key:          r.key,
			Version:      r.version,
			ItemType:     r.itemType,
			Title:        f["title"],
			Date:         f["date"],
			DOI:          f["DOI"],
			URL:          f["url"],
			Journal:      firstNonEmpty(f["publicationTitle"], f["bookTitle"], f["proceedingsTitle"], f["conferenceName"], f["websiteTitle"], f["repository"]),
			Volume:       f["volume"],
			Issue:        f["issue"],
			Pages:        f["pages"],
			Publisher:    firstNonEmpty(f["publisher"], f["university"], f["institution"]),
			Place:        f["place"],
			Language:     f["language"],
			Abstract:     f["abstractNote"],
			Extra:        f["extra"],
			DateModified: r.dateModified,
			Creators:     creators[r.itemID],
			Attachments:  atts[r.itemID],
		}
		if it.Title == "" {
			it.Title = f["caseName"]
		}
		out = append(out, it)
	}
	return out, nil
}

func (c *Client) loadFields(ctx context.Context, ids []int64) map[int64]map[string]string {
	out := map[int64]map[string]string{}
	for _, chunk := range chunkInt64(ids, 400) {
		ph, args := placeholders(chunk)
		rows, err := c.db.QueryContext(ctx, `
			select id.itemID, f.fieldName, v.value
			from itemData id
			join fields f on f.fieldID = id.fieldID
			join itemDataValues v on v.valueID = id.valueID
			where id.itemID in (`+ph+`)`, args...)
		if err != nil {
			continue
		}
		for rows.Next() {
			var itemID int64
			var name, value string
			if err := rows.Scan(&itemID, &name, &value); err != nil {
				continue
			}
			if out[itemID] == nil {
				out[itemID] = map[string]string{}
			}
			out[itemID][name] = value
		}
		rows.Close()
	}
	return out
}

func (c *Client) loadCreators(ctx context.Context, ids []int64) map[int64][]Creator {
	out := map[int64][]Creator{}
	for _, chunk := range chunkInt64(ids, 400) {
		ph, args := placeholders(chunk)
		rows, err := c.db.QueryContext(ctx, `
			select ic.itemID, coalesce(c.firstName,''), coalesce(c.lastName,''), coalesce(ct.creatorType,'author')
			from itemCreators ic
			join creators c on c.creatorID = ic.creatorID
			left join creatorTypes ct on ct.creatorTypeID = ic.creatorTypeID
			where ic.itemID in (`+ph+`)
			order by ic.itemID, ic.orderIndex`, args...)
		if err != nil {
			continue
		}
		for rows.Next() {
			var itemID int64
			var c Creator
			if err := rows.Scan(&itemID, &c.FirstName, &c.LastName, &c.CreatorType); err != nil {
				continue
			}
			if c.LastName == "" && c.FirstName != "" {
				c.Name = c.FirstName
			}
			out[itemID] = append(out[itemID], c)
		}
		rows.Close()
	}
	return out
}

func (c *Client) loadAttachments(ctx context.Context, ids []int64) map[int64][]Attachment {
	out := map[int64][]Attachment{}
	for _, chunk := range chunkInt64(ids, 400) {
		ph, args := placeholders(chunk)
		rows, err := c.db.QueryContext(ctx, `
			select ia.parentItemID, att.key, coalesce(ia.contentType,''), coalesce(ia.path,''), ia.linkMode
			from itemAttachments ia
			join items att on att.itemID = ia.itemID
			where ia.parentItemID in (`+ph+`)
			  and not exists (select 1 from deletedItems d where d.itemID = ia.itemID)`, args...)
		if err != nil {
			continue
		}
		for rows.Next() {
			var parent int64
			var a Attachment
			var linkMode int
			if err := rows.Scan(&parent, &a.Key, &a.ContentType, &a.Path, &linkMode); err != nil {
				continue
			}
			a.ParentKey = strconv.FormatInt(parent, 10)
			a.LinkMode = linkModeName(linkMode)
			if strings.HasPrefix(a.Path, "storage:") {
				a.FileName = filepath.Base(strings.TrimPrefix(a.Path, "storage:"))
			}
			out[parent] = append(out[parent], a)
		}
		rows.Close()
	}
	return out
}

func (c *Client) fulltextSQLite(attachmentKey string) (Fulltext, bool, error) {
	if err := c.ensureSQLite(); err != nil {
		return Fulltext{}, false, err
	}
	// .zotero-ft-cache 是 Zotero 自己维护的纯文本缓存，按附件 key 存放。
	p := filepath.Join(c.cfg.DataDir, "storage", attachmentKey, ".zotero-ft-cache")
	b, err := os.ReadFile(p)
	if err != nil {
		return Fulltext{}, false, nil
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return Fulltext{}, false, nil
	}
	// 页码信息在 DB 里；取不到也不影响正文。
	var indexed, total int
	_ = c.db.QueryRowContext(context.Background(), `
		select coalesce(fi.indexedPages,0), coalesce(fi.totalPages,0)
		from fulltextItems fi join items i on i.itemID = fi.itemID
		where i.key = ?`, attachmentKey).Scan(&indexed, &total)
	return Fulltext{Content: text, IndexedPages: indexed, TotalPages: total}, true, nil
}

func linkModeName(n int) string {
	switch n {
	case 0:
		return "imported_file"
	case 1:
		return "imported_url"
	case 2:
		return "linked_file"
	case 3:
		return "linked_url"
	case 4:
		return "embedded_image"
	}
	return strconv.Itoa(n)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func chunkInt64(ids []int64, size int) [][]int64 {
	if size <= 0 {
		size = 400
	}
	var out [][]int64
	for start := 0; start < len(ids); start += size {
		end := start + size
		if end > len(ids) {
			end = len(ids)
		}
		out = append(out, ids[start:end])
	}
	return out
}

func placeholders(ids []int64) (string, []any) {
	if len(ids) == 0 {
		return "", nil
	}
	parts := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		parts[i] = "?"
		args[i] = id
	}
	return strings.Join(parts, ","), args
}
