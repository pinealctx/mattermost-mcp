// Package localcache stores bounded snapshots scoped to a verified user and deployment.
package localcache

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	bolt "go.etcd.io/bbolt"
)

const maxEntries = 128
const maxEntryBytes = 1 << 20

var entries = []byte("entries-v1")
var revision = []byte("revision-v1")

type Store struct{ path, scope string }

func hash(parts ...string) string {
	b, _ := json.Marshal(parts)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func New(root, deployment, endpoint, userID, credential string) (*Store, error) {
	if userID == "" {
		return nil, fmt.Errorf("cache requires a verified user ID")
	}
	if root == "" {
		path, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(path, "mattermost-mcp")
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("cache directory must be absolute")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("invalid cache directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("cache directory must be private (0700)")
	}
	path := filepath.Join(root, hash(deployment, endpoint, userID)+".db")
	return &Store{path: path, scope: hash(credential)}, nil
}

func (s *Store) transaction(write bool, fn func(*bolt.Tx) error) (err error) {
	if fi, err := os.Lstat(s.path); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("invalid cache file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	db, err := bolt.Open(s.path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	if write {
		return db.Update(fn)
	}
	return db.View(fn)
}

func generation(tx *bolt.Tx) uint64 {
	b := tx.Bucket(revision)
	if b == nil {
		return 0
	}
	return b.Sequence()
}

func (s *Store) Get(key string, dest any) (gen uint64, hit bool, err error) {
	err = s.transaction(false, func(tx *bolt.Tx) error {
		gen = generation(tx)
		b := tx.Bucket(entries)
		if b == nil {
			return nil
		}
		v := b.Get([]byte(hash(s.scope, key)))
		if len(v) > 8 && int64(binary.BigEndian.Uint64(v[:8])) > time.Now().UnixNano() {
			hit = json.Unmarshal(v[8:], dest) == nil
		}
		return nil
	})
	return
}

func (s *Store) Put(key string, gen uint64, ttl time.Duration, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.transaction(true, func(tx *bolt.Tx) error {
		if generation(tx) != gen {
			return nil
		} // A concurrent write invalidated this in-flight read.
		b, err := tx.CreateBucketIfNotExists(entries)
		if err != nil {
			return err
		}
		id := []byte(hash(s.scope, key))
		if len(raw) > maxEntryBytes {
			return b.Delete(id)
		}
		now := time.Now()
		// Keys viewed in a bbolt cursor must not be used after deleting entries.
		var expired [][]byte
		count := 0
		if err := b.ForEach(func(k, v []byte) error {
			if len(v) < 8 || int64(binary.BigEndian.Uint64(v[:8])) <= now.UnixNano() {
				expired = append(expired, bytes.Clone(k))
			} else {
				count++
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range expired {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		if count >= maxEntries && b.Get(id) == nil {
			first, _ := b.Cursor().First()
			if err := b.Delete(first); err != nil {
				return err
			}
		}
		data := binary.BigEndian.AppendUint64(nil, uint64(now.Add(ttl).UnixNano()))
		return b.Put(id, append(data, raw...))
	})
}

func (s *Store) Clear() error {
	return s.transaction(true, func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(revision)
		if err != nil {
			return err
		}
		if _, err = b.NextSequence(); err != nil {
			return err
		}
		if tx.Bucket(entries) != nil {
			return tx.DeleteBucket(entries)
		}
		return nil
	})
}
