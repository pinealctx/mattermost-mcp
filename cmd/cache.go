package cmd

import (
	"context"
	"github.com/pinealctx/mattermost-mcp/internal/mattermost"
	"github.com/spf13/cobra"
)

func (a *app) cacheCommand() *cobra.Command {
	group := &cobra.Command{Use: "cache", Short: "Manage snapshots for the authenticated account"}
	group.AddCommand(&cobra.Command{Use: "clear", Short: "Invalidate all local snapshots for this account and endpoint", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return a.withClient(cmd, func(ctx context.Context, c *mattermost.Client) error { return c.ClearCache(ctx) })
	}})
	return group
}
