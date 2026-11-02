package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/gofrs/flock"
	"os"
	"time"
)

type Job struct {
	Scope   string `json:"scope"`
	PID     int    `json:"pid"`
	State   string `json:"state"`
	Updated int64  `json:"updated_ms"`
	Detail  string `json:"detail,omitempty"`
}

func (s *Store) Jobs(ctx context.Context, scope string) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT scope,pid,status,updated,detail FROM workers WHERE scope=? OR ?='' ORDER BY scope", scope, scope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Job{}
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.Scope, &j.PID, &j.State, &j.Updated, &j.Detail); err != nil {
			return nil, err
		}
		if j.State == "running" {
			l := flock.New(s.path + "." + j.Scope + ".worker")
			acquired, lockErr := l.TryLock()
			if lockErr != nil {
				return nil, lockErr
			}
			if acquired {
				j.State = "interrupted"
				j.Detail = "worker no longer holds the lock; resume from the saved checkpoint"
				if err := l.Unlock(); err != nil {
					return nil, err
				}
			}
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Store) Stop(ctx context.Context, scope string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE workers SET stop=1 WHERE scope=?", scope)
	return err
}

// Registration and retirement share the store lock. A concurrent start either
// adds work to the existing process or becomes the next owner.
func (e *Sync) RunJob(ctx context.Context, scope string, ids []string, ready func() error) (err error) {
	lock := flock.New(e.Store.path + "." + scope + ".worker")
	owned := false
	defer func() {
		if owned {
			err = errors.Join(err, lock.Unlock())
		}
	}()
	err = e.Store.locked(ctx, func() error {
		var lockErr error
		owned, lockErr = lock.TryLock()
		if lockErr != nil {
			return lockErr
		}
		return e.Store.write(ctx, func(tx *sql.Tx) error {
			for _, id := range ids {
				if _, err := tx.Exec("INSERT OR IGNORE INTO pending VALUES(?,?)", scope, id); err != nil {
					return err
				}
			}
			if owned {
				_, err := tx.Exec("INSERT OR REPLACE INTO workers VALUES(?,?,?,?,?,0)", scope, os.Getpid(), "running", time.Now().UnixMilli(), "")
				return err
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	if ready != nil {
		if err = ready(); err != nil {
			return err
		}
	}
	if !owned {
		if ready != nil {
			return nil
		}
		// A foreground resume waits for the process which accepted the request.
		ok, err := lock.TryLockContext(ctx, 100*time.Millisecond)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("worker lock unavailable")
		}
		defer func() { _ = lock.Unlock() }()
		var state, detail string
		if err := e.Store.db.QueryRowContext(ctx, "SELECT status,detail FROM workers WHERE scope=?", scope).Scan(&state, &detail); err != nil {
			return err
		}
		if state != "complete" {
			return fmt.Errorf("worker %s: %s", state, detail)
		}
		return nil
	}
	for {
		var batch []string
		err = e.Store.locked(ctx, func() error {
			rows, err := e.Store.db.QueryContext(ctx, "SELECT channel FROM pending WHERE scope=? ORDER BY channel", scope)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					_ = rows.Close()
					return err
				}
				batch = append(batch, id)
			}
			err = rows.Err()
			if closeErr := rows.Close(); err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
			}
			if len(batch) > 0 {
				_, err = e.Store.db.ExecContext(ctx, "DELETE FROM pending WHERE scope=?", scope)
				return err
			}
			if _, err = e.Store.db.ExecContext(ctx, "UPDATE workers SET status='complete',detail='',updated=? WHERE scope=?", time.Now().UnixMilli(), scope); err != nil {
				return err
			}
			if err = lock.Unlock(); err == nil {
				owned = false
			}
			return err
		})
		if err != nil || len(batch) == 0 {
			break
		}
		err = e.Backfill(ctx, batch, func() bool {
			var stop bool
			fetch := e.Store.db.QueryRowContext(ctx, "SELECT stop FROM workers WHERE scope=?", scope).Scan(&stop)
			return fetch != nil || stop
		})
		if err != nil {
			break
		}
	}
	if err != nil {
		_, writeErr := e.Store.db.Exec("UPDATE workers SET status='interrupted',detail=?,updated=? WHERE scope=?", err.Error(), time.Now().UnixMilli(), scope)
		return errors.Join(err, writeErr)
	}
	return nil
}
