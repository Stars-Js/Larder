package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"larder/internal/model"
)

// libraryItemColumns 是 library_items 表的完整列清单。
const libraryItemColumns = `item_key, library_id, collection_key, item_type, title, year,
	date, doi, url, authors, journal, volume, issue, pages, publisher, language,
	abstract, cite_key, bibtex, attachment_key, attachment_path, fulltext_source,
	item_version, date_modified, doc_id, sync_status, sync_note, updated_at`

// UpsertLibraryItem 写入或更新文献条目。
func (s *SQLite) UpsertLibraryItem(ctx context.Context, item model.LibraryItem) error {
	authors, err := json.Marshal(item.Authors)
	if err != nil {
		return fmt.Errorf("序列化作者列表失败: %w", err)
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = time.Now().UTC()
	}
	_, err = s.db.ExecContext(ctx, `insert into library_items(`+libraryItemColumns+`)
		values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(item_key) do update set
			library_id=excluded.library_id, collection_key=excluded.collection_key,
			item_type=excluded.item_type, title=excluded.title, year=excluded.year,
			date=excluded.date, doi=excluded.doi, url=excluded.url, authors=excluded.authors,
			journal=excluded.journal, volume=excluded.volume, issue=excluded.issue,
			pages=excluded.pages, publisher=excluded.publisher, language=excluded.language,
			abstract=excluded.abstract, cite_key=excluded.cite_key, bibtex=excluded.bibtex,
			attachment_key=excluded.attachment_key, attachment_path=excluded.attachment_path,
			fulltext_source=excluded.fulltext_source, item_version=excluded.item_version,
			date_modified=excluded.date_modified, doc_id=excluded.doc_id,
			sync_status=excluded.sync_status, sync_note=excluded.sync_note,
			updated_at=excluded.updated_at`,
		item.ItemKey, item.LibraryID, item.CollectionKey, item.ItemType, item.Title, item.Year,
		item.Date, item.DOI, item.URL, string(authors), item.Journal, item.Volume, item.Issue,
		item.Pages, item.Publisher, item.Language, item.Abstract, item.CiteKey, item.BibTeX,
		item.AttachmentKey, item.AttachmentPath, item.FulltextSource, item.ItemVersion,
		item.DateModified, item.DocID, item.SyncStatus, item.SyncNote,
		item.UpdatedAt.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("写入文献条目失败: %w", err)
	}
	return nil
}

// GetLibraryItem 按 item key 取文献条目。
func (s *SQLite) GetLibraryItem(ctx context.Context, itemKey string) (model.LibraryItem, bool, error) {
	row := s.db.QueryRowContext(ctx, `select `+libraryItemColumns+`
		from library_items where item_key = ?`, strings.TrimSpace(itemKey))
	return scanLibraryItem(row)
}

// GetLibraryItemByDocID 按文档 ID 取文献条目。
func (s *SQLite) GetLibraryItemByDocID(ctx context.Context, docID string) (model.LibraryItem, bool, error) {
	row := s.db.QueryRowContext(ctx, `select `+libraryItemColumns+`
		from library_items where doc_id = ? order by updated_at desc limit 1`, strings.TrimSpace(docID))
	return scanLibraryItem(row)
}

// ListLibraryItems 列出全部文献条目（最近更新的在前）。
func (s *SQLite) ListLibraryItems(ctx context.Context) ([]model.LibraryItem, error) {
	rows, err := s.db.QueryContext(ctx, `select `+libraryItemColumns+`
		from library_items order by updated_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.LibraryItem, 0)
	for rows.Next() {
		item, _, err := scanLibraryItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetLibraryItemsByDocIDs 批量按文档 ID 取文献条目。
func (s *SQLite) GetLibraryItemsByDocIDs(ctx context.Context, docIDs []string) (map[string]model.LibraryItem, error) {
	out := make(map[string]model.LibraryItem, len(docIDs))
	if len(docIDs) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(docIDs)), ",")
	args := make([]any, len(docIDs))
	for i, id := range docIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `select `+libraryItemColumns+`
		from library_items where doc_id in (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		item, ok, err := scanLibraryItem(rows)
		if err != nil {
			return nil, err
		}
		if ok && item.DocID != "" {
			out[item.DocID] = item
		}
	}
	return out, rows.Err()
}

// SetLibraryItemStatus 只更新同步状态，保留元数据。
func (s *SQLite) SetLibraryItemStatus(ctx context.Context, itemKey, status, note string) error {
	_, err := s.db.ExecContext(ctx, `update library_items
		set sync_status=?, sync_note=?, updated_at=? where item_key=?`,
		status, note, time.Now().UTC().Format(time.RFC3339), itemKey)
	if err != nil {
		return fmt.Errorf("更新文献条目状态失败: %w", err)
	}
	return nil
}

// DeleteLibraryItem 删除文献条目映射。
func (s *SQLite) DeleteLibraryItem(ctx context.Context, itemKey string) error {
	_, err := s.db.ExecContext(ctx, `delete from library_items where item_key = ?`, itemKey)
	return err
}

func scanLibraryItem(r rowScanner) (model.LibraryItem, bool, error) {
	var item model.LibraryItem
	var authors, updated string
	err := r.Scan(
		&item.ItemKey, &item.LibraryID, &item.CollectionKey, &item.ItemType, &item.Title, &item.Year,
		&item.Date, &item.DOI, &item.URL, &authors, &item.Journal, &item.Volume, &item.Issue,
		&item.Pages, &item.Publisher, &item.Language, &item.Abstract, &item.CiteKey, &item.BibTeX,
		&item.AttachmentKey, &item.AttachmentPath, &item.FulltextSource, &item.ItemVersion,
		&item.DateModified, &item.DocID, &item.SyncStatus, &item.SyncNote, &updated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return model.LibraryItem{}, false, nil
	}
	if err != nil {
		return model.LibraryItem{}, false, err
	}
	_ = json.Unmarshal([]byte(authors), &item.Authors)
	if t, err := time.Parse(time.RFC3339, updated); err == nil {
		item.UpdatedAt = t
	}
	return item, true, nil
}
