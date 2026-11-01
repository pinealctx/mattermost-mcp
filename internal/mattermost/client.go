package mattermost

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pinealctx/mattermost-mcp/internal/config"
)

type Client struct {
	Auth    *Auth
	Session *mcp.ClientSession
}

func Connect(ctx context.Context, cfg config.Config, version string) (*Client, error) {
	a, err := Authenticate(ctx, cfg)
	if err != nil {
		return nil, err
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "mattermost-mcp", Version: version}, nil)
	session, err := c.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: a.cfg.MCPURL, HTTPClient: a.client, MaxRetries: -1,
	}, nil)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("connect to official MCP: %w", a.redact(err)), a.Close())
	}
	return &Client{Auth: a, Session: session}, nil
}

func (c *Client) Close() error {
	err := c.Session.Close()
	return errors.Join(c.Auth.redact(err), c.Auth.Close())
}

func (c *Client) Tools(ctx context.Context) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 100; page++ {
		result, err := c.Session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, c.Auth.redact(err)
		}
		tools = append(tools, result.Tools...)
		cursor = result.NextCursor
		if cursor == "" {
			sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
			return tools, nil
		}
		if seen[cursor] {
			return nil, errors.New("upstream repeated a tools pagination cursor")
		}
		seen[cursor] = true
	}
	return nil, errors.New("upstream tools pagination exceeded 100 pages")
}

func (c *Client) Call(ctx context.Context, name string, args any) (*mcp.CallToolResult, error) {
	result, err := c.Session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	return result, c.Auth.redact(err)
}
