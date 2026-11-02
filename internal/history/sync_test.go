package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pinealctx/mattermost-mcp/internal/mattermost"
)

const cid = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
const cid2 = "bbbbbbbbbbbbbbbbbbbbbbbbbb"

type sim struct {
	all            map[string][]mattermost.HistoryPost
	now            int64
	failure        bool
	overflow       bool
	reads, lookups int
}

func (f *sim) Channels(context.Context) ([]string, error) { return []string{cid, cid2}, nil }
func (f *sim) Member(_ context.Context, id string) (int64, error) {
	if _, ok := f.all[id]; !ok {
		return 0, &mattermost.APIError{Status: 403}
	}
	return f.now, nil
}
func (f *sim) Lookup(_ context.Context, ids []string) ([]mattermost.HistoryPost, error) {
	f.lookups++
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := []mattermost.HistoryPost{}
	for _, rows := range f.all {
		for _, p := range rows {
			if want[p.ID] && p.DeleteAt == 0 {
				out = append(out, p)
			}
		}
	}
	return out, nil
}
func (f *sim) Posts(_ context.Context, id string, q url.Values) (mattermost.HistoryPage, error) {
	f.reads++
	empty := mattermost.HistoryPage{Posts: map[string]mattermost.HistoryPost{}, Order: []string{}}
	if f.failure && q.Get("before") != "" {
		return empty, errors.New("synthetic interruption")
	}
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	page, _ := strconv.Atoi(q.Get("page"))
	rows := append([]mattermost.HistoryPost{}, f.all[id]...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreateAt == rows[j].CreateAt {
			return rows[i].ID > rows[j].ID
		}
		return rows[i].CreateAt > rows[j].CreateAt
	})
	var bound int64
	for _, p := range rows {
		if p.ID == q.Get("before") {
			bound = p.CreateAt
		}
	}
	if q.Get("before") != "" && bound == 0 {
		return empty, nil
	}
	var eligible []mattermost.HistoryPost
	for _, p := range rows {
		if since > 0 {
			if p.UpdateAt > since {
				eligible = append(eligible, p)
			}
			continue
		}
		if p.DeleteAt == 0 && (bound == 0 || p.CreateAt < bound) {
			eligible = append(eligible, p)
		}
	}
	if since == 0 {
		low := min(page*pageSize, len(eligible))
		eligible = eligible[low:min(low+pageSize, len(eligible))]
	}
	if since != 0 && f.overflow {
		for len(eligible) < 1000 {
			p := post(id, 10000+len(eligible))
			eligible = append(eligible, p)
		}
	}
	for _, p := range eligible {
		empty.Order = append(empty.Order, p.ID)
		empty.Posts[p.ID] = p
	}
	return empty, nil
}
func post(channel string, i int) mattermost.HistoryPost {
	return mattermost.HistoryPost{ID: fmt.Sprintf("%026d", i), ChannelID: channel, CreateAt: int64(10000 + i/3), UpdateAt: int64(10000 + i/3), Message: fmt.Sprintf("post %d", i)}
}
func setup(t *testing.T, n int) (*Sync, *sim, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "history")
	db, err := Open(root, "https://fixture.example", "me", "test-scope")
	if err != nil {
		t.Fatal(err)
	}
	f := &sim{all: map[string][]mattermost.HistoryPost{cid: {}, cid2: {post(cid2, 9000)}}, now: 20000}
	for i := 1; i <= n; i++ {
		f.all[cid] = append(f.all[cid], post(cid, i))
	}
	e := &Sync{Store: db, Remote: f}
	t.Cleanup(func() { _ = e.Store.Close() })
	return e, f, root
}
func finish(t *testing.T, e *Sync, id string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		s, err := e.Store.State(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if s.Phase == "complete" {
			return
		}
		if err := e.step(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("backfill did not finish")
}
func TestRecentFirstResumeAndReconcile(t *testing.T) {
	ctx := context.Background()
	e, f, root := setup(t, 153)
	ids, err := e.Recent(ctx, "")
	if err != nil || len(ids) != 2 {
		t.Fatal(ids, err)
	}
	s, _ := e.Store.State(ctx, cid)
	s2, _ := e.Store.State(ctx, cid2)
	if s.Count != pageSize || s2.Count != 1 {
		t.Fatalf("recent pages: %+v %+v", s, s2)
	}
	if err := e.step(ctx, cid); err != nil {
		t.Fatal(err)
	}
	before, _ := e.Store.State(ctx, cid)
	f.failure = true
	if err := e.step(ctx, cid); err == nil {
		t.Fatal("missing simulated interruption")
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	e.Store, err = Open(root, "https://fixture.example", "me", "test-scope")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := e.Store.State(ctx, cid)
	if after != before {
		t.Fatal("failed page moved checkpoint")
	}
	f.failure = false
	finish(t, e, cid)
	full, _ := e.Store.State(ctx, cid)
	if full.Count != 153 {
		t.Fatal(full)
	}
	// The old edit and deletion are outside the recent page. Only the delta carries them.
	f.now += 4000
	f.all[cid][0].Message = "edited"
	f.all[cid][0].UpdateAt = f.now
	f.all[cid][1].UpdateAt = f.now
	f.all[cid][1].DeleteAt = f.now
	p := post(cid, 999)
	p.CreateAt = f.now
	p.UpdateAt = f.now
	f.all[cid] = append(f.all[cid], p)
	var local []mattermost.HistoryPost
	out, err := e.Refresh(ctx, cid, 5, func(rows []mattermost.HistoryPost) error { local = rows; return nil })
	if err != nil || len(local) == 0 || out[0].ID != p.ID || local[0].ID == p.ID {
		t.Fatalf("local-first failed: %v", err)
	}
	var data string
	var deleted int64
	if err = e.Store.db.QueryRow("SELECT data FROM posts WHERE id=?", f.all[cid][0].ID).Scan(&data); err != nil || !containsEdit(data) {
		t.Fatalf("old edit missing: %v", err)
	}
	if err = e.Store.db.QueryRow("SELECT deleted,data FROM posts WHERE id=?", f.all[cid][1].ID).Scan(&deleted, &data); err != nil || deleted == 0 || data != "{}" {
		t.Fatal("deleted body retained")
	}
	delete(f.all, cid)
	if _, err = e.Refresh(ctx, cid, 5, nil); !mattermost.Inaccessible(err) {
		t.Fatal(err)
	}
	if _, err = e.Store.State(ctx, cid); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("revoked channel retained")
	}
	for _, identity := range [][2]string{{"https://other.example", "me"}, {"https://fixture.example", "other"}} {
		s, err := Open(root, identity[0], identity[1], "test-scope")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.State(ctx, cid2); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("cross-account history")
		}
		_ = s.Close()
	}
}
func containsEdit(s string) bool { return strings.Contains(s, `"message":"edited"`) }
func TestOverflowRequiresSweepAndChecksInvisiblePosts(t *testing.T) {
	ctx := context.Background()
	e, f, _ := setup(t, 75)
	if _, err := e.Recent(ctx, cid); err != nil {
		t.Fatal(err)
	}
	finish(t, e, cid)
	old, _ := e.Store.State(ctx, cid)
	f.all[cid] = f.all[cid][1:]
	f.overflow = true
	f.now += 8000
	if _, err := e.Refresh(ctx, cid, 0, nil); err != nil {
		t.Fatal(err)
	}
	repair, _ := e.Store.State(ctx, cid)
	if !repair.Repair || repair.Watermark != old.Watermark || repair.Phase == "complete" {
		t.Fatal("overflow advanced checkpoint")
	}
	f.overflow = false
	finish(t, e, cid)
	_, err := e.Store.db.Exec("UPDATE channels SET checked=0 WHERE id=?", cid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Refresh(ctx, cid, 0, nil); err != nil {
		t.Fatal(err)
	}
	finish(t, e, cid)
	end, _ := e.Store.State(ctx, cid)
	if end.Count != 74 {
		t.Fatal(end)
	}
}

func TestLocalFramePrecedesPostFetchAndNewBearerRechecks(t *testing.T) {
	ctx := context.Background()
	e, f, _ := setup(t, 3)
	if _, err := e.Recent(ctx, cid); err != nil {
		t.Fatal(err)
	}
	original := f.all[cid][2]
	f.all[cid][2].Message = "new version"
	f.all[cid][2].UpdateAt = f.now + 1
	reads, lookups := f.reads, f.lookups
	final, err := e.Refresh(ctx, cid, 3, func(local []mattermost.HistoryPost) error {
		if reads != f.reads || lookups != f.lookups {
			t.Fatal("warm local preview fetched post bodies")
		}
		if len(local) != 3 || local[0].Message != original.Message {
			t.Fatal("preview did not come from SQLite")
		}
		return nil
	})
	if err != nil || final[0].Message != "new version" {
		t.Fatal("edit not reconciled", err)
	}
	e.Store.audience = "different-scope"
	f.all[cid] = f.all[cid][:2]
	lookups = f.lookups
	_, err = e.Refresh(ctx, cid, 3, func(local []mattermost.HistoryPost) error {
		if f.lookups == lookups || len(local) != 2 {
			t.Fatal("new credential saw stale local preview")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestMovingPaginationBoundaryRestartsWithoutLosingTies(t *testing.T) {
	ctx := context.Background()
	e, f, _ := setup(t, 132)
	for i := range f.all[cid] {
		f.all[cid][i].CreateAt = 10000
	}
	if _, err := e.Recent(ctx, cid); err != nil {
		t.Fatal(err)
	}
	if err := e.step(ctx, cid); err != nil {
		t.Fatal(err)
	}
	before, _ := e.Store.State(ctx, cid)
	f.all[cid] = f.all[cid][:131]
	if err := e.step(ctx, cid); err != nil {
		t.Fatal(err)
	}
	after, _ := e.Store.State(ctx, cid)
	if after.Gen <= before.Gen {
		t.Fatal("changed boundary advanced a stale cursor")
	}
	finish(t, e, cid)
	s, _ := e.Store.State(ctx, cid)
	if s.Count != 131 {
		t.Fatal(s)
	}
}
