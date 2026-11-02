// Package testutil hosts an isolated Mattermost/MCP fixture; no live service is used.
package testutil

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pinealctx/mattermost-mcp/internal/config"
)

const Session = "fixture-session"
const PAT = "fixture-personal-token"
const OtherPAT = "fixture-second-user"
const ScopedPAT = "fixture-scoped-token"

type Fixture struct {
	HTTP                   *httptest.Server
	MCP                    *mcp.Server
	mu                     sync.Mutex
	logins, logouts, calls int
	active                 bool
	revoked                bool
	mcpRequests            int
	LastArgs               json.RawMessage
}

func New(t *testing.T, jsonResponse bool) *Fixture {
	t.Helper()
	f := &Fixture{}
	f.MCP = mcp.NewServer(&mcp.Implementation{Name: "fixture-official-mcp", Version: "1.0"}, &mcp.ServerOptions{PageSize: 1})
	for _, name := range []string{"get_me", "get_team_info", "create_post", "tool_failure"} {
		toolName := name
		f.MCP.AddTool(&mcp.Tool{Name: name, Description: "Fixture tool", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			f.mu.Lock()
			f.calls++
			f.LastArgs = append(json.RawMessage(nil), req.Params.Arguments...)
			f.mu.Unlock()
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture result"}}, IsError: toolName == "tool_failure"}, nil
		})
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return f.MCP }, &mcp.StreamableHTTPOptions{JSONResponse: jsonResponse})
	f.HTTP = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/users/login" {
			f.mu.Lock()
			f.logins++
			f.mu.Unlock()
			var p map[string]string
			_ = json.NewDecoder(r.Body).Decode(&p)
			if p["login_id"] != "test-user" || p["password"] != "test-password" || p["token"] != "123456" {
				w.WriteHeader(401)
				_, _ = w.Write([]byte(`{"id":"login.mfa.app_error","message":"DO_NOT_PRINT_PASSWORD"}`))
				return
			}
			f.mu.Lock()
			f.active = true
			f.mu.Unlock()
			w.Header().Set("Token", Session)
			_, _ = w.Write([]byte(`{"id":"fixture-user-id","username":"test-user"}`))
			return
		}
		f.mu.Lock()
		bearer := r.Header.Get("Authorization")
		valid := (bearer == "Bearer "+PAT && !f.revoked) || bearer == "Bearer "+OtherPAT || bearer == "Bearer "+ScopedPAT || (f.active && bearer == "Bearer "+Session)
		f.mu.Unlock()
		if !valid {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v4/users/logout":
			f.mu.Lock()
			f.logouts++
			f.active = false
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"status":"OK"}`))
		case "/api/v4/users/me":
			if bearer == "Bearer "+OtherPAT {
				_, _ = w.Write([]byte(`{"id":"fixture-user-two","username":"second-user"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"fixture-user-id","username":"test-user"}`))
		case "/plugins/mattermost-ai/mcp-server/mcp":
			f.mu.Lock()
			f.mcpRequests++
			f.mu.Unlock()
			mcpHandler.ServeHTTP(w, r)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.HTTP.Close)
	return f
}

func (f *Fixture) Config() config.Config {
	return config.Config{URL: f.HTTP.URL, Username: "test-user", Password: "test-password", MFAToken: "123456", Timeout: 5 * time.Second}
}

func (f *Fixture) Counts() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins, f.logouts, f.calls
}

func (f *Fixture) MCPRequests() int { f.mu.Lock(); defer f.mu.Unlock(); return f.mcpRequests }
func (f *Fixture) RevokePAT()       { f.mu.Lock(); defer f.mu.Unlock(); f.revoked = true }
