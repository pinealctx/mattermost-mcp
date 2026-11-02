package mattermost

import (
	"context"
	"errors"
	"fmt"
	"github.com/pinealctx/mattermost-mcp/internal/localcache"
	"io"
	"sort"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pinealctx/mattermost-mcp/internal/config"
)

type Client struct {
	Auth    *Auth
	Session *mcp.ClientSession
	mu      sync.Mutex
	version string
	store   *localcache.Store
	recheck bool
	// CacheTrace optionally receives cache source diagnostics, never arguments or responses.
	CacheTrace io.Writer
}

// Open authenticates immediately but defers MCP negotiation until the first network operation.
func Open(ctx context.Context, cfg config.Config, version string) (*Client, error) {
	a, err := Authenticate(ctx, cfg)
	if err != nil {
		return nil, err
	}
	c := &Client{Auth: a, version: version}
	if cfg.CacheMode != "" && cfg.CacheMode != "off" {
		if err := c.openStore(ctx); err != nil {
			return nil, errors.Join(err, a.Close())
		}
	}
	return c, nil
}

func Connect(ctx context.Context, cfg config.Config, version string) (*Client, error) {
	c, err := Open(ctx, cfg, version)
	if err != nil {
		return nil, err
	}
	c.recheck = true
	if err := c.ensureSession(ctx); err != nil {
		return nil, errors.Join(err, c.Close())
	}
	return c, nil
}

func (c *Client) ensureSession(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Session != nil {
		return nil
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "mattermost-mcp", Version: c.version}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: c.Auth.cfg.MCPURL, HTTPClient: c.Auth.client, MaxRetries: -1, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return fmt.Errorf("connect to official MCP: %w", c.Auth.redact(err))
	}
	c.Session = session
	return nil
}

func (c *Client) Close() error {
	var err error
	if c.Session != nil {
		err = c.Session.Close()
	}
	return errors.Join(c.Auth.redact(err), c.Auth.Close())
}

func (c *Client) fetchTools(ctx context.Context) ([]*mcp.Tool, error) {
	if err := c.ensureSession(ctx); err != nil {
		return nil, err
	}
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

func (c *Client) liveCall(ctx context.Context, name string, args any) (*mcp.CallToolResult, error) {
	if err := c.ensureSession(ctx); err != nil {
		return nil, err
	}
	result, err := c.Session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	return result, c.Auth.redact(err)
}
