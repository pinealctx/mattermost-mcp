package server

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pinealctx/mattermost-mcp/internal/mattermost"
	"github.com/pinealctx/mattermost-mcp/internal/testutil"
)

func TestBridgeForwardsToolsAndHonorsAllowlist(t *testing.T) {
	f := testutil.New(t, false)
	cfg := f.Config()
	cfg.Token = testutil.PAT
	upstream, err := mattermost.Connect(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	if _, err := New(context.Background(), upstream, "test", "unknown_tool", time.Second); err == nil {
		t.Fatal("unknown tool allowed")
	}
	bridge, err := New(context.Background(), upstream, "test", "get_me,tool_failure", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ss, err := bridge.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "fixture-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 2 {
		t.Fatalf("tools %v err %v", tools, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "tool_failure", Arguments: map[string]any{}})
	if err != nil || !result.IsError {
		t.Fatal("upstream tool error not preserved")
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "create_post", Arguments: map[string]any{}}); err == nil {
		t.Fatal("non-allowlisted tool exposed")
	}
}
