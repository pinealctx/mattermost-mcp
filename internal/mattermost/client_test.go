package mattermost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pinealctx/mattermost-mcp/internal/config"
	"github.com/pinealctx/mattermost-mcp/internal/testutil"
)

func TestPasswordMFAAndMCP(t *testing.T) {
	for _, jsonResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "SSE", true: "JSON"}[jsonResponse], func(t *testing.T) {
			f := testutil.New(t, jsonResponse)
			c, err := Connect(context.Background(), f.Config(), "test")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			tools, err := c.Tools(context.Background())
			if err != nil || len(tools) != 4 {
				t.Fatalf("tools=%d error=%v", len(tools), err)
			}
			result, err := c.Call(context.Background(), "get_me", map[string]any{})
			if err != nil || result.IsError {
				t.Fatalf("read failed: %v", err)
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			logins, logouts, calls := f.Counts()
			if logins != 1 || logouts != 1 || calls != 1 {
				t.Fatalf("counts=%d %d %d", logins, logouts, calls)
			}
			if _, err := c.Auth.Me(context.Background()); err == nil {
				t.Fatal("logged-out token still works")
			}
		})
	}
}

func TestInjectedTokenIsNotRevoked(t *testing.T) {
	f := testutil.New(t, true)
	cfg := f.Config()
	cfg.Token = testutil.PAT
	c, err := Connect(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	logins, logouts, _ := f.Counts()
	if logins != 0 || logouts != 0 {
		t.Fatal("injected token caused login/logout")
	}
}

func TestLoginFailureIsNotRetriedOrLeaked(t *testing.T) {
	f := testutil.New(t, true)
	cfg := f.Config()
	cfg.MFAToken = ""
	_, err := Authenticate(context.Background(), cfg)
	if err == nil || strings.Contains(err.Error(), "DO_NOT_PRINT_PASSWORD") {
		t.Fatalf("unexpected error: %v", err)
	}
	logins, _, _ := f.Counts()
	if logins != 1 {
		t.Fatal("login retried")
	}
}

func TestCleanupOnMCPFailureAndExport(t *testing.T) {
	f := testutil.New(t, true)
	cfg := f.Config()
	cfg.MCPURL = cfg.URL + "/missing"
	if _, err := Connect(context.Background(), cfg, "test"); err == nil {
		t.Fatal("missing endpoint accepted")
	}
	_, logouts, _ := f.Counts()
	if logouts != 1 {
		t.Fatal("failed connection leaked login session")
	}
	a, err := Authenticate(context.Background(), f.Config())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteSessionToken(func(string) error { return errors.New("broken output") }); err == nil {
		t.Fatal("expected write error")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Authenticate(context.Background(), f.Config())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteSessionToken(func(token string) error {
		if token != testutil.Session {
			t.Fatal("unexpected exported token")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Me(context.Background()); err != nil {
		t.Fatal("exported session was revoked")
	}
	if err := a.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNeverForwardCredentialsOnRedirect(t *testing.T) {
	var received atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { received.Add(1); w.WriteHeader(200) }))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	cfg := config.Config{URL: origin.URL, Token: "test-token", Timeout: time.Second}
	a, err := Authenticate(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Me(context.Background()); err == nil {
		t.Fatal("redirect accepted")
	}
	if response, err := a.client.Get(other.URL); err == nil {
		response.Body.Close()
		t.Fatal("foreign origin accepted")
	}
	if received.Load() != 0 {
		t.Fatal("credentials may have reached foreign origin")
	}
}
