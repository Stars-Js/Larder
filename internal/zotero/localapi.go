package zotero

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// apiPage 是 Zotero 本地 API 的一页结果。
type apiPage struct {
	Items []map[string]any
	Total int
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) (http.Header, error) {
	u := c.cfg.APIBase + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Zotero-API-Version", "3")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return resp.Header, fmt.Errorf("Zotero 本地 API 未开启（403）：请在 Zotero 设置 → 高级中勾选“允许其他应用与 Zotero 通信”")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.Header, fmt.Errorf("Zotero 本地 API %s 返回 %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 64<<20))
	if err := dec.Decode(out); err != nil {
		return resp.Header, fmt.Errorf("解析 Zotero 响应失败: %w", err)
	}
	return resp.Header, nil
}

func (c *Client) getPage(ctx context.Context, path string, start int) (apiPage, error) {
	q := url.Values{}
	q.Set("limit", "100")
	q.Set("start", strconv.Itoa(start))
	var rows []map[string]any
	h, err := c.getJSON(ctx, path, q, &rows)
	if err != nil {
		return apiPage{}, err
	}
	page := apiPage{Items: rows}
	if v := h.Get("Total-Results"); v != "" {
		page.Total, _ = strconv.Atoi(v)
	}
	return page, nil
}

// getAll 按 Zotero 的 limit/start 分页取完。
func (c *Client) getAll(ctx context.Context, path string) ([]map[string]any, error) {
	var out []map[string]any
	start := 0
	for {
		page, err := c.getPage(ctx, path, start)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if len(page.Items) < 100 {
			break
		}
		if page.Total > 0 && len(out) >= page.Total {
			break
		}
		start += len(page.Items)
		if start > 100000 {
			break
		}
	}
	return out, nil
}

func (c *Client) collectionsAPI(ctx context.Context) ([]Collection, error) {
	rows, err := c.getAll(ctx, c.libraryPrefix()+"/collections")
	if err != nil {
		return nil, err
	}
	out := make([]Collection, 0, len(rows))
	for _, row := range rows {
		data := mapValue(row, "data")
		key := firstString(row, "key", "data.key")
		name := firstString(data, "name", "collectionName")
		if key == "" || name == "" {
			continue
		}
		parent := firstString(data, "parentCollection")
		if parent == "false" {
			parent = ""
		}
		out = append(out, Collection{
			Key:       key,
			Name:      name,
			ParentKey: parent,
			LibraryID: c.cfg.LibraryID,
			ItemCount: intValue(mapValue(row, "meta"), "numItems"),
		})
	}
	return out, nil
}

func (c *Client) collectionItemsAPI(ctx context.Context, collectionKey string, includeSub bool) ([]Item, error) {
	keys := []string{collectionKey}
	if includeSub {
		cols, err := c.collectionsAPI(ctx)
		if err == nil {
			keys = collectionDescendants(cols, collectionKey)
		}
	}
	seen := map[string]bool{}
	var base []Item
	for _, key := range keys {
		path := c.libraryPrefix() + "/collections/" + url.PathEscape(key) + "/items/top"
		rows, err := c.getAll(ctx, path)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			item := itemFromAPI(row)
			if item.Key == "" || seen[item.Key] {
				continue
			}
			seen[item.Key] = true
			if isNonRegularItem(item.ItemType) {
				continue
			}
			base = append(base, item)
		}
	}

	// 每个条目的 children 是一次独立 HTTP 请求；本地 API 无限流，可以并发取。
	const workers = 8
	out := make([]Item, len(base))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range base {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				out[i] = base[i]
				return
			}
			item := base[i]
			if atts, err := c.childrenAPI(ctx, item.Key); err == nil {
				item.Attachments = atts
			}
			out[i] = item
		}(i)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) childrenAPI(ctx context.Context, itemKey string) ([]Attachment, error) {
	rows, err := c.getAll(ctx, c.libraryPrefix()+"/items/"+url.PathEscape(itemKey)+"/children")
	if err != nil {
		return nil, err
	}
	out := make([]Attachment, 0, len(rows))
	for _, row := range rows {
		data := mapValue(row, "data")
		if firstString(data, "itemType") != "attachment" {
			continue
		}
		out = append(out, Attachment{
			Key:         firstString(row, "key", "data.key"),
			ParentKey:   itemKey,
			Title:       firstString(data, "title"),
			FileName:    firstString(data, "filename"),
			ContentType: firstString(data, "contentType"),
			Path:        firstString(data, "path"),
			LinkMode:    firstString(data, "linkMode"),
			MD5:         firstString(data, "md5"),
		})
	}
	return out, nil
}

func (c *Client) fulltextAPI(ctx context.Context, attachmentKey string) (Fulltext, bool, error) {
	var resp struct {
		Content      string `json:"content"`
		IndexedPages int    `json:"indexedPages"`
		TotalPages   int    `json:"totalPages"`
	}
	if _, err := c.getJSON(ctx, c.libraryPrefix()+"/items/"+url.PathEscape(attachmentKey)+"/fulltext", nil, &resp); err != nil {
		return Fulltext{}, false, err
	}
	if strings.TrimSpace(resp.Content) == "" {
		return Fulltext{}, false, nil
	}
	return Fulltext{Content: resp.Content, IndexedPages: resp.IndexedPages, TotalPages: resp.TotalPages}, true, nil
}

func (c *Client) downloadAttachmentAPI(ctx context.Context, attachmentKey string) ([]byte, error) {
	u := c.cfg.APIBase + c.libraryPrefix() + "/items/" + url.PathEscape(attachmentKey) + "/file"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Zotero-API-Version", "3")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("下载 Zotero 附件失败：HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

func itemFromAPI(row map[string]any) Item {
	data := mapValue(row, "data")
	it := Item{
		Key:          firstString(row, "key", "data.key"),
		Version:      intValue(row, "version"),
		ItemType:     firstString(data, "itemType"),
		Title:        firstString(data, "title"),
		Date:         firstString(data, "date"),
		DOI:          firstString(data, "DOI"),
		URL:          firstString(data, "url"),
		Journal:      firstString(data, "publicationTitle", "bookTitle", "proceedingsTitle", "conferenceName", "websiteTitle", "repository"),
		Volume:       firstString(data, "volume"),
		Issue:        firstString(data, "issue"),
		Pages:        firstString(data, "pages"),
		Publisher:    firstString(data, "publisher", "university", "institution"),
		Place:        firstString(data, "place"),
		Language:     firstString(data, "language"),
		Abstract:     firstString(data, "abstractNote"),
		Extra:        firstString(data, "extra"),
		DateModified: firstString(data, "dateModified"),
	}
	if it.DateModified == "" {
		it.DateModified = firstString(row, "dateModified")
	}
	if creators, ok := data["creators"].([]any); ok {
		for _, raw := range creators {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			it.Creators = append(it.Creators, Creator{
				FirstName:   firstString(m, "firstName"),
				LastName:    firstString(m, "lastName"),
				Name:        firstString(m, "name"),
				CreatorType: firstString(m, "creatorType"),
			})
		}
	}
	return it
}

func isNonRegularItem(itemType string) bool {
	switch strings.ToLower(itemType) {
	case "attachment", "note", "annotation":
		return true
	}
	return false
}

func collectionDescendants(cols []Collection, root string) []string {
	byParent := map[string][]string{}
	for _, c := range cols {
		byParent[c.ParentKey] = append(byParent[c.ParentKey], c.Key)
	}
	out := []string{root}
	seen := map[string]bool{root: true}
	queue := []string{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, child := range byParent[cur] {
			if seen[child] {
				continue
			}
			seen[child] = true
			out = append(out, child)
			queue = append(queue, child)
		}
	}
	return out
}

func mapValue(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

func firstString(m map[string]any, paths ...string) string {
	for _, p := range paths {
		var cur any = m
		for _, part := range strings.Split(p, ".") {
			mm, ok := cur.(map[string]any)
			if !ok {
				cur = nil
				break
			}
			cur = mm[part]
		}
		switch v := cur.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			return strconv.FormatBool(v)
		}
	}
	return ""
}

func intValue(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}
