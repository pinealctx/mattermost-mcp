package cmd

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pinealctx/mattermost-mcp/internal/testutil"
)

func TestWarmReadsIsolationAndInvalidation(t *testing.T) {
	environment(t)
	f := testutil.New(t, true)
	t.Setenv("MATTERMOST_URL", f.HTTP.URL)
	t.Setenv("MATTERMOST_TOKEN", testutil.PAT)
	require := func(args ...string) {
		t.Helper()
		_, _, err := execute(args...)
		if err != nil {
			t.Fatal(err)
		}
	}
	read := func() { require("tool", "get-team-info", "--team-name", "example") }
	read()
	first := f.MCPRequests()
	require("tool", "call", "get_team_info", "--args", `{ "team_name": "example" }`)
	if f.MCPRequests() != first {
		t.Fatal("warm read opened an upstream MCP session")
	}
	require("tool", "get-team-info", "--team-name", "example", "--cache", "refresh")
	if f.MCPRequests() == first {
		t.Fatal("refresh served a snapshot")
	}
	for _, token := range []string{testutil.OtherPAT, testutil.ScopedPAT} {
		t.Setenv("MATTERMOST_TOKEN", token)
		before := f.MCPRequests()
		read()
		if f.MCPRequests() == before {
			t.Fatal("snapshot crossed an account or credential boundary")
		}
	}
	// A mutation by another credential for account one must invalidate its original snapshot.
	require("tool", "create-post", "--channel-id", "example", "--message", "fixture")
	t.Setenv("MATTERMOST_TOKEN", testutil.PAT)
	before := f.MCPRequests()
	read()
	if f.MCPRequests() == before {
		t.Fatal("stale snapshot after write")
	}
	f.RevokePAT()
	out, _, err := execute("tool", "get-team-info", "--team-name", "example")
	if err == nil || out != "" {
		t.Fatal("revoked credential served cached data")
	}
}

func TestNativeHelpAndServerAvailability(t *testing.T) {
	environment(t)
	out, _, err := execute("tool", "get-user-channels", "--help")
	if err != nil || !strings.Contains(out, "--team-id") {
		t.Fatal("fixed signature unavailable offline")
	}
	f := testutil.New(t, true)
	t.Setenv("MATTERMOST_URL", f.HTTP.URL)
	t.Setenv("MATTERMOST_TOKEN", testutil.PAT)
	_, _, err = execute("tool", "get-user-channels", "--team-id", "fixture")
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatal("absent server tool was offered as executable")
	}
	_, _, calls := f.Counts()
	if calls != 0 {
		t.Fatal("called a tool missing from the catalog")
	}
}

func TestMessageSnapshotsRequireOptInAndRespectRefreshFailure(t *testing.T) {
	environment(t)
	f := testutil.New(t, true)
	t.Setenv("MATTERMOST_URL", f.HTTP.URL)
	t.Setenv("MATTERMOST_TOKEN", testutil.PAT)
	var calls atomic.Int32
	var denied atomic.Bool
	f.MCP.AddTool(&mcp.Tool{Name: "read_channel", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture response"}}, IsError: denied.Load()}, nil
	})
	read := func(extra ...string) error {
		_, _, err := execute(append([]string{"channel", "read", "fixture"}, extra...)...)
		return err
	}
	for _, opt := range [][]string{nil, nil, {"--cache-messages"}, {"--cache-messages"}} {
		if err := read(opt...); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("unexpected default or opt-in message reuse")
	}
	denied.Store(true)
	if err := read("--cache-messages", "--cache", "refresh"); err == nil {
		t.Fatal("permission failure was hidden")
	}
	if err := read("--cache-messages"); err == nil {
		t.Fatal("served stale message after live denial")
	}
}
