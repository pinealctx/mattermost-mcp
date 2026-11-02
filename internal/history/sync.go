package history

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pinealctx/mattermost-mcp/internal/mattermost"
)

type Remote interface {
	Channels(context.Context) ([]string, error)
	Member(context.Context, string) (int64, error)
	Posts(context.Context, string, url.Values) (mattermost.HistoryPage, error)
	Lookup(context.Context, []string) ([]mattermost.HistoryPost, error)
}
type Sync struct {
	Store  *Store
	Remote Remote
}

const pageSize = 60

func historyQuery(anchor string, page int) url.Values {
	q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
	if anchor != "" {
		q.Set("before", anchor)
	}
	return q
}
func signature(p mattermost.HistoryPage) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(p.Order, "/"))))
}
func (e *Sync) savePage(tx *sql.Tx, s State, p mattermost.HistoryPage) error {
	for _, post := range p.Posts {
		if err := savePost(tx, s.Channel, post, s.Gen, e.Store.audience); err != nil {
			return err
		}
	}
	return saveState(tx, s)
}
func startScan(s *State, head mattermost.HistoryPage, floor int64) {
	s.Gen++
	s.Floor = floor
	s.Phase = "backfill"
	s.Anchor = ""
	s.Page = 1
	s.Previous = signature(head)
	if len(head.Order) == 0 {
		s.Phase = "audit"
		return
	}
	// Anchor below the newest timestamp only if the head covers its full tie group.
	first, last := head.Posts[head.Order[0]], head.Posts[head.Order[len(head.Order)-1]]
	if first.CreateAt > last.CreateAt {
		s.Anchor = first.ID
		s.Page = 0
		s.Previous = ""
	}
}
func (e *Sync) guard(ctx context.Context, id string, fn func(State, int64) error) error {
	return e.Store.locked(ctx, func() error {
		floor, err := e.Remote.Member(ctx, id)
		if mattermost.Inaccessible(err) {
			return errors.Join(err, e.Store.purge(ctx, id))
		}
		if err != nil {
			return err
		}
		if _, err = e.Store.db.ExecContext(ctx, "INSERT INTO channels(id) VALUES(?) ON CONFLICT DO NOTHING", id); err != nil {
			return err
		}
		s, err := e.Store.checkpoint(ctx, id)
		if err != nil {
			return err
		}
		err = fn(s, floor)
		if mattermost.Inaccessible(err) {
			return errors.Join(err, e.Store.purge(ctx, id))
		}
		return err
	})
}

// auditIDs checks actual readable posts. Absence from a page or a truncated delta is never a deletion signal.
func (e *Sync) auditIDs(ctx context.Context, s State, ids []string, readable map[string]mattermost.HistoryPost) error {
	if len(ids) == 0 {
		return nil
	}
	posts, err := e.Remote.Lookup(ctx, ids)
	if err != nil {
		return err
	}
	missing := make(map[string]bool, len(ids))
	for _, id := range ids {
		missing[id] = true
	}
	return e.Store.write(ctx, func(tx *sql.Tx) error {
		for _, p := range posts {
			if !missing[p.ID] {
				return errors.New("unexpected post in batch lookup")
			}
			delete(missing, p.ID)
			if readable != nil && p.DeleteAt == 0 {
				readable[p.ID] = p
			}
			if err := savePost(tx, s.Channel, p, s.Gen, e.Store.audience); err != nil {
				return err
			}
		}
		for id := range missing {
			if _, err := tx.Exec("DELETE FROM posts WHERE id=? AND channel=?", id, s.Channel); err != nil {
				return err
			}
		}
		return nil
	})
}

// Refresh reads the local timeline first. Before emitting a preview it checks
// current read permission and revalidates rows originally read using a different bearer.
func (e *Sync) Refresh(ctx context.Context, id string, limit int, preview func([]mattermost.HistoryPost) error) ([]mattermost.HistoryPost, error) {
	var final []mattermost.HistoryPost
	err := e.guard(ctx, id, func(s State, floor int64) error {
		var previewIDs []string
		if preview != nil {
			local, err := e.Store.records(ctx, id, limit)
			if err != nil {
				return err
			}
			var unknown []string
			for _, row := range local {
				previewIDs = append(previewIDs, row.ID)
				if row.Source != e.Store.audience || row.Source == "" {
					unknown = append(unknown, row.ID)
				}
			}
			verified := map[string]mattermost.HistoryPost{}
			if err := e.auditIDs(ctx, s, unknown, verified); err != nil {
				return err
			}
			visible := []mattermost.HistoryPost{}
			for _, row := range local {
				if row.Source != "" && row.Source == e.Store.audience {
					visible = append(visible, row.HistoryPost)
				} else if current, ok := verified[row.ID]; ok {
					visible = append(visible, current)
				}
			}
			if err := preview(visible); err != nil {
				return err
			}
		}
		head, err := e.Remote.Posts(ctx, id, historyQuery("", 0))
		if err != nil {
			return err
		}
		var delta mattermost.HistoryPage
		if s.Watermark > 0 {
			delta, err = e.Remote.Posts(ctx, id, url.Values{"since": {strconv.FormatInt(max(int64(1), s.Watermark-1000), 10)}})
			if err != nil {
				return err
			}
		}
		overflow := len(delta.Order) >= 1000 || len(delta.Posts) >= 1000
		if s.Phase == "new" || (overflow && !s.Repair) || (s.Phase == "complete" && time.Since(time.UnixMilli(s.Checked)) >= 24*time.Hour) {
			startScan(&s, head, floor)
		}
		if overflow {
			s.Repair = true
		} else if !s.Repair {
			s.Watermark = max(s.Watermark, floor)
		}
		err = e.Store.write(ctx, func(tx *sql.Tx) error {
			if err := e.savePage(tx, s, head); err != nil {
				return err
			}
			return e.savePage(tx, s, delta)
		})
		if err != nil {
			return err
		}
		if limit > 0 {
			rows, err := e.Store.messages(ctx, id, limit)
			if err != nil {
				return err
			}
			// The tail can include older posts outside the head or an omitted deletion.
			// Validate those candidates too before exposing the reconciled timeline.
			verify := []string{}
			seen := map[string]bool{}
			for _, id := range previewIDs {
				if _, ok := head.Posts[id]; !ok {
					verify = append(verify, id)
					seen[id] = true
				}
			}
			for _, p := range rows {
				if _, ok := head.Posts[p.ID]; !ok && !seen[p.ID] {
					verify = append(verify, p.ID)
				}
			}
			visible := map[string]mattermost.HistoryPost{}
			for _, p := range rows {
				if current, ok := head.Posts[p.ID]; ok {
					if change, ok := delta.Posts[p.ID]; ok && max(change.UpdateAt, change.DeleteAt) >= max(current.UpdateAt, current.DeleteAt) {
						current = change
					}
					if current.DeleteAt == 0 {
						visible[p.ID] = current
					}
				}
			}
			if err := e.auditIDs(ctx, s, verify, visible); err != nil {
				return err
			}
			rows, err = e.Store.messages(ctx, id, limit)
			if err != nil {
				return err
			}
			final = []mattermost.HistoryPost{}
			for _, p := range rows {
				if current, ok := visible[p.ID]; ok {
					final = append(final, current)
				}
			}
		}
		return nil
	})
	return final, err
}

func (e *Sync) auditStep(ctx context.Context, s State) error {
	rows, err := e.Store.db.QueryContext(ctx, "SELECT id FROM posts WHERE channel=? AND seen<? LIMIT 100", s.Channel, s.Gen)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if len(ids) > 0 {
		return e.auditIDs(ctx, s, ids, nil)
	}
	s.Phase = "complete"
	s.Checked = time.Now().UnixMilli()
	s.Watermark = max(s.Watermark, s.Floor)
	s.Repair = false
	return e.Store.write(ctx, func(tx *sql.Tx) error { return saveState(tx, s) })
}

func (e *Sync) step(ctx context.Context, id string) error {
	return e.guard(ctx, id, func(s State, floor int64) error {
		switch s.Phase {
		case "complete":
			return nil
		case "audit":
			return e.auditStep(ctx, s)
		case "backfill":
		default:
			return errors.New("recent page must be initialized first")
		}
		page, err := e.Remote.Posts(ctx, id, historyQuery(s.Anchor, s.Page))
		if err != nil {
			return err
		}
		restart := false
		if s.Page > 0 {
			prev, err := e.Remote.Posts(ctx, id, historyQuery(s.Anchor, s.Page-1))
			if err != nil {
				return err
			}
			restart = signature(prev) != s.Previous
		}
		if len(page.Order) == 0 && s.Anchor != "" {
			anchors, err := e.Remote.Lookup(ctx, []string{s.Anchor})
			if err != nil {
				return err
			}
			restart = restart || len(anchors) != 1 || anchors[0].ID != s.Anchor || anchors[0].DeleteAt != 0
		}
		if restart {
			head, err := e.Remote.Posts(ctx, id, historyQuery("", 0))
			if err != nil {
				return err
			}
			startScan(&s, head, floor)
			page = head
		} else if len(page.Order) == 0 {
			s.Phase = "audit"
		} else {
			if s.Page > 0 && signature(page) == s.Previous {
				return errors.New("server repeated history page; checkpoint retained")
			}
			s.Page++
			s.Previous = signature(page)
		}
		return e.Store.write(ctx, func(tx *sql.Tx) error { return e.savePage(tx, s, page) })
	})
}

// Discover reconciles the account's membership before a full-account run.
// A scoped run never fetches posts or purges local data for another channel.
func (e *Sync) Discover(ctx context.Context, only string) ([]string, error) {
	if only != "" {
		if err := mattermost.ValidateID(only); err != nil {
			return nil, err
		}
		return []string{only}, nil
	}
	var ids []string
	err := e.Store.locked(ctx, func() error {
		var err error
		ids, err = e.Remote.Channels(ctx)
		if err != nil {
			return err
		}
		keep := map[string]bool{}
		for _, id := range ids {
			keep[id] = true
		}
		return e.Store.write(ctx, func(tx *sql.Tx) error {
			rows, err := tx.Query("SELECT id FROM channels")
			if err != nil {
				return err
			}
			var gone []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					_ = rows.Close()
					return err
				}
				if !keep[id] {
					gone = append(gone, id)
				}
			}
			err = rows.Err()
			if closeErr := rows.Close(); err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
			}
			for _, id := range gone {
				if _, err := tx.Exec("DELETE FROM channels WHERE id=?", id); err != nil {
					return err
				}
			}
			return nil
		})
	})
	return ids, err
}

func pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(200 * time.Millisecond):
		return nil
	}
}
func (e *Sync) Recent(ctx context.Context, only string) ([]string, error) {
	ids, err := e.Discover(ctx, only)
	if err != nil {
		return nil, err
	}
	var failures []error
	ready := []string{}
	for _, id := range ids {
		if _, err = e.Refresh(ctx, id, 0, nil); err == nil {
			ready = append(ready, id)
		} else if only != "" || !mattermost.Inaccessible(err) {
			failures = append(failures, fmt.Errorf("channel %s: %w", id, err))
		}
		if err = pause(ctx); err != nil {
			return ready, err
		}
	}
	return ready, errors.Join(failures...)
}

// Backfill advances each channel by one durable step per round. Failed channels
// retain their checkpoint while other readable conversations continue.
func (e *Sync) Backfill(ctx context.Context, ids []string, stop func() bool) error {
	todo := append([]string(nil), ids...)
	var failures []error
	for len(todo) > 0 {
		next := []string{}
		for _, id := range todo {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if stop != nil && stop() {
				return errors.New("backfill stopped; checkpoints retained")
			}
			if err := e.step(ctx, id); err != nil {
				if !mattermost.Inaccessible(err) {
					failures = append(failures, fmt.Errorf("channel %s: %w", id, err))
				}
			} else {
				s, err := e.Store.checkpoint(ctx, id)
				if err != nil {
					return err
				}
				if s.Phase != "complete" {
					next = append(next, id)
				} else {
					// Catch changes which arrived during the historical sweep.
					if _, err := e.Refresh(ctx, id, 0, nil); err != nil {
						failures = append(failures, err)
					} else if s, err = e.Store.checkpoint(ctx, id); err != nil {
						return err
					} else if s.Phase != "complete" {
						next = append(next, id)
					}
				}
			}
			if err := pause(ctx); err != nil {
				return err
			}
		}
		todo = next
	}
	return errors.Join(failures...)
}
