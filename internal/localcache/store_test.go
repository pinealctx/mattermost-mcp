package localcache

import (
	"path/filepath"
	"testing"
	"time"
)

func TestScopedSnapshotLifetime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	open := func(server, user, cred string) *Store {
		t.Helper()
		s, err := New(root, server, server+"/mcp", user, cred)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	base := open("server-one", "user-one", "credential")
	if err := base.Put("k", 0, time.Minute, "sample"); err != nil {
		t.Fatal(err)
	}
	fetch := func(s *Store, expected bool) uint64 {
		t.Helper()
		var value string
		gen, hit, err := s.Get("k", &value)
		if err != nil || hit != expected || (hit && value != "sample") {
			t.Fatalf("hit=%t value=%q err=%v", hit, value, err)
		}
		return gen
	}
	fetch(base, true)
	fetch(open("server-two", "user-one", "credential"), false)
	fetch(open("server-one", "user-two", "credential"), false)
	fetch(open("server-one", "user-one", "other-credential"), false)
	if err := base.Clear(); err != nil {
		t.Fatal(err)
	}
	if err := base.Put("k", 0, time.Minute, "sample"); err != nil {
		t.Fatal(err)
	}
	gen := fetch(base, false)
	if err := base.Put("k", gen, -time.Second, "sample"); err != nil {
		t.Fatal(err)
	}
	fetch(base, false)
}
