// Package history maintains a resumable SQLite mirror of readable Mattermost posts.
package history

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gofrs/flock"
	"github.com/pinealctx/mattermost-mcp/internal/mattermost"
	_ "modernc.org/sqlite"
)

type Store struct {
	db       *sql.DB
	path     string
	audience string
}

func Open(root, server, uid, audience string) (*Store, error) {
	if uid == "" || server == "" {
		return nil, errors.New("verified server and user are required")
	}
	if root == "" {
		p, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(p, "mattermost-mcp", "history")
	}
	if !filepath.IsAbs(root) {
		return nil, errors.New("history directory must be absolute")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || runtime.GOOS != "windows" && st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("history directory must be private (0700)")
	}
	path := filepath.Join(root, fmt.Sprintf("%x.sqlite", sha256.Sum256([]byte(server+"\x00"+uid))))
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("unsafe history file")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=15000; PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA secure_delete=ON;
 CREATE TABLE IF NOT EXISTS channels (
 id TEXT PRIMARY KEY, phase TEXT NOT NULL DEFAULT 'new', gen INTEGER NOT NULL DEFAULT 0,
 anchor TEXT NOT NULL DEFAULT '', page INTEGER NOT NULL DEFAULT 0, previous TEXT NOT NULL DEFAULT '',
 watermark INTEGER NOT NULL DEFAULT 0, floor INTEGER NOT NULL DEFAULT 1,
 checked INTEGER NOT NULL DEFAULT 0, repair INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS posts(
 id TEXT PRIMARY KEY, channel TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
 created INTEGER NOT NULL, version INTEGER NOT NULL, deleted INTEGER NOT NULL, data BLOB NOT NULL, seen INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS timeline ON posts(channel,deleted,created DESC,id DESC);
 CREATE INDEX IF NOT EXISTS audit ON posts(channel,seen);
 CREATE TABLE IF NOT EXISTS pending(scope TEXT,channel TEXT,PRIMARY KEY(scope,channel));
 CREATE TABLE IF NOT EXISTS workers(scope TEXT PRIMARY KEY,pid INTEGER,status TEXT,updated INTEGER,detail TEXT, stop INTEGER DEFAULT 0);`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, path: path, audience: audience}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) locked(ctx context.Context, fn func() error) (err error) {
	l := flock.New(s.path + ".lock")
	ok, err := l.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("history lock unavailable")
	}
	defer func() { err = errors.Join(err, l.Unlock()) }()
	return fn()
}
func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

type State struct {
	Channel   string `json:"channel_id"`
	Phase     string `json:"phase"`
	Gen       int64  `json:"-"`
	Anchor    string `json:"-"`
	Page      int    `json:"next_page"`
	Previous  string `json:"-"`
	Watermark int64  `json:"watermark_ms"`
	Floor     int64  `json:"-"`
	Checked   int64  `json:"last_complete_ms"`
	Repair    bool   `json:"reconciliation_required"`
	Count     int    `json:"visible_posts"`
}

func (s *Store) State(ctx context.Context, id string) (State, error) {
	x, err := s.checkpoint(ctx, id)
	if err != nil {
		return x, err
	}
	err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM posts WHERE channel=? AND deleted=0", id).Scan(&x.Count)
	return x, err
}
func (s *Store) checkpoint(ctx context.Context, id string) (State, error) {
	var x State
	err := s.db.QueryRowContext(ctx, `SELECT id,phase,gen,anchor,page,previous,watermark,floor,checked,repair FROM channels WHERE id=?`, id).Scan(&x.Channel, &x.Phase, &x.Gen, &x.Anchor, &x.Page, &x.Previous, &x.Watermark, &x.Floor, &x.Checked, &x.Repair)
	return x, err
}
func saveState(tx *sql.Tx, s State) error {
	_, err := tx.Exec(`UPDATE channels SET phase=?,gen=?,anchor=?,page=?,previous=?,watermark=?,floor=?,checked=?,repair=? WHERE id=?`, s.Phase, s.Gen, s.Anchor, s.Page, s.Previous, s.Watermark, s.Floor, s.Checked, s.Repair, s.Channel)
	return err
}
func savePost(tx *sql.Tx, channel string, p mattermost.HistoryPost, gen int64, scope string) error {
	if p.ID == "" || p.ChannelID != channel || p.CreateAt <= 0 {
		return errors.New("invalid post for history partition")
	}
	// Disappearing messages are never retained in the mirror.
	if p.Type == "custom_burn_on_read" || p.Type == "burn_on_read" {
		_, err := tx.Exec("DELETE FROM posts WHERE channel=? AND id=?", channel, p.ID)
		return err
	}
	raw, err := json.Marshal(record{HistoryPost: p, Source: scope})
	if err != nil {
		return err
	}
	if p.DeleteAt > 0 {
		raw = []byte("{}")
	}
	version := max(p.CreateAt, p.UpdateAt, p.DeleteAt)
	_, err = tx.Exec(`INSERT INTO posts(id,channel,created,version,deleted,data,seen) VALUES(?,?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET version=max(posts.version,excluded.version), seen=max(posts.seen,excluded.seen),
 data=CASE WHEN excluded.version>=posts.version AND (posts.deleted=0 OR excluded.deleted>0) THEN excluded.data ELSE posts.data END,
 deleted=max(posts.deleted,excluded.deleted) WHERE posts.channel=excluded.channel`, p.ID, channel, p.CreateAt, version, p.DeleteAt, raw, gen)
	return err
}

type record struct {
	mattermost.HistoryPost
	Source string `json:"_source,omitempty"`
}

func (s *Store) messages(ctx context.Context, id string, n int) ([]mattermost.HistoryPost, error) {
	rows, err := s.records(ctx, id, n)
	if err != nil {
		return nil, err
	}
	out := make([]mattermost.HistoryPost, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.HistoryPost)
	}
	return out, nil
}
func (s *Store) records(ctx context.Context, id string, n int) ([]record, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM posts WHERE channel=? AND deleted=0 ORDER BY created DESC,id DESC LIMIT ?", id, n)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []record{}
	for rows.Next() {
		var raw []byte
		var p record
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) purge(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM channels WHERE id=?", id)
	return err
}
