package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pinealctx/mattermost-mcp/internal/localcache"
	"github.com/pinealctx/mattermost-mcp/internal/native"
)

func (c *Client) trace(source, name string) {
	if c.CacheTrace != nil {
		_, _ = fmt.Fprintf(c.CacheTrace, "cache %s %q\n", source, name)
	}
}

const catalogTTL = 5 * time.Minute

func (c *Client) openStore(ctx context.Context) error {
	a := c.Auth
	if a.User.ID == "" {
		u, err := a.Me(ctx)
		if err != nil {
			return err
		}
		a.User = u
	}
	// Sessions created by password login have the same authority across commands.
	// Caller-supplied bearers can have distinct scopes even for the same account.
	credential := "password-session"
	if a.cfg.Token != "" {
		credential = "bearer:" + a.cfg.Token
	}
	store, err := localcache.New(a.cfg.CacheDir, a.cfg.URL, a.cfg.MCPURL, a.User.ID, credential)
	if err != nil {
		return fmt.Errorf("open account cache (use --cache off to bypass): %w", err)
	}
	c.store = store
	return nil
}

func (c *Client) ClearCache(ctx context.Context) error {
	if c.store == nil {
		if err := c.openStore(ctx); err != nil {
			return err
		}
	}
	return c.store.Clear()
}

// Long-running bridge sessions must authenticate again before serving disk snapshots.
func (c *Client) checkHit(ctx context.Context) error {
	if !c.recheck {
		return nil
	}
	u, err := c.Auth.Me(ctx)
	if err != nil {
		return err
	}
	if u.ID != c.Auth.User.ID {
		return errors.New("authenticated account changed; restart the bridge")
	}
	return nil
}

func (c *Client) Tools(ctx context.Context) ([]*mcp.Tool, error) {
	return c.catalog(ctx, c.Auth.cfg.CacheMode == "prefer")
}

func (c *Client) LiveTools(ctx context.Context) ([]*mcp.Tool, error) { return c.catalog(ctx, false) }

func (c *Client) catalog(ctx context.Context, prefer bool) ([]*mcp.Tool, error) {
	if c.store == nil {
		return c.fetchTools(ctx)
	}
	var tools []*mcp.Tool
	gen, hit, err := c.store.Get("catalog", &tools)
	if err != nil {
		return nil, err
	}
	if prefer && hit {
		if err := c.checkHit(ctx); err != nil {
			return nil, err
		}
		c.trace("hit", "catalog")
		return tools, nil
	}
	tools, err = c.fetchTools(ctx)
	if err != nil {
		return nil, errors.Join(err, c.store.Clear())
	}
	c.trace("live", "catalog")
	return tools, c.store.Put("catalog", gen, catalogTTL, tools)
}

func (c *Client) availableTool(ctx context.Context, name string) (*mcp.Tool, error) {
	tools, err := c.Tools(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range tools {
		if t.Name == name {
			return t, nil
		}
	}
	return nil, fmt.Errorf("tool %q is unavailable in the server catalog (use --cache refresh to rediscover)", name)
}

func dataTTL(name string) time.Duration {
	switch name {
	case "get_user_teams", "get_team_info", "get_channel_info", "get_user_channels", "list_team_channels", "get_user", "get_user_by_username":
		return 2 * time.Minute
	}
	return 0
}

func cacheKey(name string, args any) (string, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var object map[string]any
	if err := dec.Decode(&object); err != nil {
		return "", err
	}
	if object == nil {
		object = map[string]any{}
	}
	raw, err = json.Marshal(object)
	return name + "\x00" + string(raw), err
}

func (c *Client) Call(ctx context.Context, name string, args any) (*mcp.CallToolResult, error) {
	return c.call(ctx, name, args, nil)
}

func (c *Client) CallNative(ctx context.Context, name string, args any) (*mcp.CallToolResult, error) {
	tool, err := c.availableTool(ctx, name)
	if err != nil {
		return nil, err
	}
	return c.call(ctx, name, args, tool)
}

func (c *Client) call(ctx context.Context, name string, args any, tool *mcp.Tool) (*mcp.CallToolResult, error) {
	if c.store == nil {
		return c.liveCall(ctx, name, args)
	}
	ttl := dataTTL(name)
	if tool == nil && ttl != 0 {
		var err error
		tool, err = c.availableTool(ctx, name)
		if err != nil {
			return nil, err
		}
	}
	if !native.Index[name].Read || (tool != nil && tool.Annotations != nil && !tool.Annotations.ReadOnlyHint) {
		// Unknown tools are conservatively handled as writes. Both sides of the
		// call invalidate in-flight reads, including ambiguous network failures.
		if err := c.store.Clear(); err != nil {
			return nil, err
		}
		result, err := c.liveCall(ctx, name, args)
		if cleanup := c.store.Clear(); cleanup != nil {
			return nil, errors.Join(err, fmt.Errorf("call attempted; check server before retrying: %w", cleanup))
		}
		return result, err
	}
	if ttl == 0 {
		return c.liveCall(ctx, name, args)
	}
	key, err := cacheKey(name, args)
	if err != nil {
		return nil, err
	}
	var cached mcp.CallToolResult
	gen, hit, err := c.store.Get(key, &cached)
	if err != nil {
		return nil, err
	}
	if hit && c.Auth.cfg.CacheMode == "prefer" {
		c.trace("hit", name)
		return &cached, nil
	}
	result, err := c.liveCall(ctx, name, args)
	if err != nil || result.IsError {
		if cleanup := c.store.Clear(); cleanup != nil {
			return nil, errors.Join(err, cleanup)
		}
		return result, err
	}
	c.trace("live", name)
	return result, c.store.Put(key, gen, ttl, result)
}
