// Package server provides a local stdio bridge to the official Mattermost MCP server.
package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pinealctx/mattermost-mcp/internal/mattermost"
)

// New discovers upstream tools and preserves their schemas, annotations and results.
// The catalog is a startup snapshot. Restart to discover server-side tool changes.
func New(ctx context.Context, client *mattermost.Client, version, enabled string, timeout time.Duration) (*mcp.Server, error) {
	tools, err := client.Tools(ctx)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	if enabled != "" {
		for _, name := range strings.Split(enabled, ",") {
			if name = strings.TrimSpace(name); name != "" {
				wanted[name] = false
			}
		}
		if len(wanted) == 0 {
			return nil, fmt.Errorf("enabled-tools must contain at least one tool name")
		}
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "mattermost-mcp", Version: version}, nil)
	for _, tool := range tools {
		if len(wanted) != 0 {
			if _, ok := wanted[tool.Name]; !ok {
				continue
			}
			wanted[tool.Name] = true
		}
		name := tool.Name
		s.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return client.Call(ctx, name, req.Params.Arguments)
		})
	}
	for name, found := range wanted {
		if !found {
			return nil, fmt.Errorf("enabled tool %q is not available upstream", name)
		}
	}
	return s, nil
}
