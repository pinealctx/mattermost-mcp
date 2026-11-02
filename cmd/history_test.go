package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the same private pipe / detached subprocess boundary as the binary,
// without building or contacting a live server.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "sync" && os.Args[2] == "_worker" {
		if err := NewRoot("fixture").ExecuteContext(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestLoginSeedsAllConversationsAndTransfersOwnedWorker(t *testing.T) {
	environment(t)
	t.Setenv("MATTERMOST_HISTORY_DIR", filepath.Join(t.TempDir(), "history"))
	const user = "fixture-user"
	ids := []string{"aaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbb"}
	var seeded atomic.Uint32
	logout := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := func(x any) { _ = json.NewEncoder(w).Encode(x) }
		path := strings.TrimPrefix(r.URL.Path, "/api/v4/")
		switch path {
		case "users/login":
			w.Header().Set("Token", "fixture-session")
			send(map[string]string{"id": user, "username": user})
			return
		case "users/logout":
			logout <- struct{}{}
			send(map[string]string{"status": "OK"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-session" {
			w.WriteHeader(401)
			return
		}
		switch path {
		case "users/me":
			send(map[string]string{"id": user, "username": user})
			return
		case "users/me/channels":
			send([]map[string]string{{"id": ids[0]}, {"id": ids[1]}})
			return
		}
		for i, id := range ids {
			switch path {
			case "channels/" + id + "/members/me":
				send(map[string]string{"channel_id": id, "user_id": user})
				return
			case "channels/" + id + "/posts":
				page := r.URL.Query().Get("page")
				if page == "0" {
					seeded.Or(1 << i)
				}
				if page == "1" && seeded.Load() != 3 {
					t.Error("history read before every recent page was initialized")
				}
				send(map[string]any{"order": []string{}, "posts": map[string]any{}})
				return
			}
		}
		t.Errorf("unexpected request: %s %s", r.Method, path)
		w.WriteHeader(404)
	}))
	defer server.Close()
	t.Setenv("MATTERMOST_URL", server.URL)
	t.Setenv("MATTERMOST_USERNAME", user)
	t.Setenv("MATTERMOST_PASSWORD", "fixture-password")
	out, _, err := execute("auth", "login")
	if err != nil || seeded.Load() != 3 || !strings.Contains(out, "history_sync") {
		t.Fatalf("login: %v %s", err, out)
	}
	select {
	case <-logout:
	case <-time.After(25 * time.Second):
		t.Fatal("background worker did not release temporary session")
	}
}
