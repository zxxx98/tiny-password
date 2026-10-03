package vault

import (
	"container/heap"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tiny-password/tiny-password/internal/auth"
)

// BrowseInput combines filters with ordering over encrypted titles. Plaintext
// titles and tags stay in request memory; no searchable index is persisted.
type BrowseInput struct {
	Query    string `json:"query"`
	Scope    string `json:"scope"`
	Type     string `json:"type"`
	Tag      string `json:"tag"`
	Favorite *bool  `json:"favorite,omitempty"`
	Sort     string `json:"sort"`
}

type BrowseItem struct {
	Meta
	Tags []string `json:"tags"`
}

func BrowseKey(item BrowseItem, order string) string {
	switch order {
	case "title_asc", "title_desc":
		return strings.ToLower(item.Title)
	case "created_desc":
		return item.CreatedAt
	default:
		return item.UpdatedAt
	}
}

func browseLess(key, id, otherKey, otherID, order string) bool {
	if key == otherKey {
		return id < otherID
	}
	if order == "title_asc" || order == "updated_asc" {
		return key < otherKey
	}
	return key > otherKey
}

// browseWhere only admits authorized rows before ciphertext is read.
func browseWhere(actor *auth.Principal, input BrowseInput) (string, []any, error) {
	if actor == nil {
		return "", nil, ErrForbidden
	}
	if input.Scope != "" && !Scopes[input.Scope] {
		return "", nil, ErrInvalidScope
	}
	if input.Type != "" && !ItemTypes[input.Type] {
		return "", nil, ErrInvalidItemType
	}
	switch input.Sort {
	case "", "updated_desc", "updated_asc", "created_desc", "title_asc", "title_desc":
	default:
		return "", nil, fmt.Errorf("%w: unknown sort order", ErrPayloadInvalid)
	}
	where := `deleted_at IS NULL AND ((vault_scope='personal' AND owner_user_id=?) OR vault_scope='shared')`
	args := []any{actor.UserID}
	if input.Scope != "" {
		where += ` AND vault_scope=?`
		args = append(args, input.Scope)
	}
	if input.Type != "" {
		where += ` AND item_type=?`
		args = append(args, input.Type)
	}
	if input.Favorite != nil {
		where += ` AND favorite=?`
		args = append(args, *input.Favorite)
	}
	return where, args, nil
}

// listBrowseRows uses the same ID-ascending tie break as browseLess, including
// for descending timestamps. column/direction come exclusively from this switch.
func (repository) listBrowseRows(ctx context.Context, q Queryer, where string, args []any, order, afterKey, afterID string, limit int) ([]itemRow, error) {
	column, direction, comparison := "updated_at", "DESC", "<"
	switch order {
	case "updated_asc":
		direction, comparison = "ASC", ">"
	case "created_desc":
		column = "created_at"
	}
	query := `SELECT ` + itemColumns + ` FROM vault_items WHERE ` + where
	// Do not mutate the caller's predicate arguments when adding a cursor.
	params := append([]any{}, args...)
	if afterID != "" {
		query += ` AND (` + column + ` ` + comparison + ` ? OR (` + column + ` = ? AND id > ?))`
		params = append(params, afterKey, afterKey, afterID)
	}
	query += ` ORDER BY ` + column + ` ` + direction + `, id ASC LIMIT ?`
	params = append(params, limit)
	rows, err := q.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []itemRow{}
	for rows.Next() {
		row, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// browseHeap keeps the worst retained match at the root. Its size is bounded
// by the page size (plus the caller's one-row next-page lookahead).
type browseHeap struct {
	items []BrowseItem
	order string
}

func (h browseHeap) Len() int { return len(h.items) }
func (h browseHeap) Less(i, j int) bool {
	return browseLess(BrowseKey(h.items[j], h.order), h.items[j].ID, BrowseKey(h.items[i], h.order), h.items[i].ID, h.order)
}
func (h browseHeap) Swap(i, j int) { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *browseHeap) Push(x any)   { h.items = append(h.items, x.(BrowseItem)) }
func (h *browseHeap) Pop() any {
	last := len(h.items) - 1
	item := h.items[last]
	h.items[last] = BrowseItem{}
	h.items = h.items[:last]
	return item
}

const browseBatchSize = 64

// Browse pages timestamps in SQL, stopping after limit matches. Name ordering
// must scan encrypted candidates globally, but retains at most limit metadata
// records and one bounded ciphertext batch. No credential payload is retained.
func (s *Service) Browse(ctx context.Context, actor *auth.Principal, input BrowseInput, afterKey, afterID string, limit int) ([]BrowseItem, error) {
	where, args, err := browseWhere(actor, input)
	if err != nil {
		return nil, err
	}
	if limit < 1 {
		return nil, fmt.Errorf("%w: invalid limit", ErrPayloadInvalid)
	}
	byTitle := input.Sort == "title_asc" || input.Sort == "title_desc"
	candidates := &browseHeap{items: []BrowseItem{}, order: input.Sort}
	match := matchQuery(input.Query, input.Tag)
	scanKey, scanID, scanOrder := afterKey, afterID, input.Sort
	if byTitle {
		scanKey, scanID, scanOrder = "", "", "updated_desc"
	}
	batchSize := browseBatchSize
	if !byTitle && input.Query == "" && input.Tag == "" {
		batchSize = limit
	}
	for {
		rows, err := s.repo.listBrowseRows(ctx, s.db, where, args, scanOrder, scanKey, scanID, batchSize)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			_, typed, envelope, err := s.decryptRow(row)
			if err != nil {
				return nil, err
			}
			scanID = row.ID
			scanKey = row.UpdatedAt
			if scanOrder == "created_desc" {
				scanKey = row.CreatedAt
			}
			if !match(row, typed, envelope.Tags) {
				continue
			}
			meta := row.toMeta()
			meta.Title = TitleOf(typed)
			item := BrowseItem{Meta: meta, Tags: append([]string{}, envelope.Tags...)}
			if byTitle {
				if afterID != "" && !browseLess(afterKey, afterID, BrowseKey(item, input.Sort), item.ID, input.Sort) {
					continue
				}
				if candidates.Len() < limit {
					heap.Push(candidates, item)
				} else {
					worst := candidates.items[0]
					if browseLess(BrowseKey(item, input.Sort), item.ID, BrowseKey(worst, input.Sort), worst.ID, input.Sort) {
						candidates.items[0] = item
						heap.Fix(candidates, 0)
					}
				}
			} else {
				candidates.items = append(candidates.items, item)
				if candidates.Len() == limit {
					break
				}
			}
		}
		if (!byTitle && candidates.Len() == limit) || len(rows) < batchSize {
			break
		}
	}
	items := candidates.items
	if byTitle {
		sort.Slice(items, func(i, j int) bool {
			return browseLess(BrowseKey(items[i], input.Sort), items[i].ID, BrowseKey(items[j], input.Sort), items[j].ID, input.Sort)
		})
	}
	metas := make([]Meta, len(items))
	for i := range items {
		metas[i] = items[i].Meta
	}
	s.attachCreatorNames(ctx, s.db, metas)
	for i := range items {
		items[i].Meta = metas[i]
	}
	return items, nil
}

// BrowseTags is an independent, explicitly requested scan. Only distinct tag
// strings are retained; query and selected tag do not narrow available options.
func (s *Service) BrowseTags(ctx context.Context, actor *auth.Principal, input BrowseInput) ([]string, error) {
	where, args, err := browseWhere(actor, input)
	if err != nil {
		return nil, err
	}
	tags := map[string]bool{}
	var scanKey, scanID string
	for {
		rows, err := s.repo.listBrowseRows(ctx, s.db, where, args, "updated_desc", scanKey, scanID, browseBatchSize)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			_, _, envelope, err := s.decryptRow(row)
			if err != nil {
				return nil, err
			}
			for _, tag := range envelope.Tags {
				tags[tag] = true
			}
			scanKey, scanID = row.UpdatedAt, row.ID
		}
		if len(rows) < browseBatchSize {
			break
		}
	}
	out := make([]string, 0, len(tags))
	for tag := range tags {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out, nil
}
