package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/pinealctx/mattermost-mcp/internal/mattermost"
	"github.com/spf13/cobra"
)

func (a *app) authCommand() *cobra.Command {
	group := &cobra.Command{Use: "auth", Short: "Validate, export or end a login session"}
	var tokenOnly bool
	login := &cobra.Command{Use: "login", Short: "Validate password login; optionally export its session token", Args: cobra.NoArgs,
		Long: "Without --token-only, validate login and immediately log out. With --token-only, print only the new session token and leave the session active for an external credential manager. No token is stored by this tool.",
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			cfg, err := a.configuration(cmd, true)
			if err != nil {
				return err
			}
			if cfg.Token != "" {
				return errors.New("auth login requires password credentials; unset MATTERMOST_TOKEN or use auth status")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Timeout)
			defer cancel()
			auth, err := mattermost.Authenticate(ctx, cfg)
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, auth.Close()) }()
			if tokenOnly {
				// Transfer ownership only after output has succeeded.
				return exportSession(cmd, auth)
			}
			return a.write(cmd, map[string]any{"authenticated": true, "user": auth.User, "session": "temporary; logged out on exit"})
		},
	}
	login.Flags().BoolVar(&tokenOnly, "token-only", false, "Print the session secret to stdout for a credential manager; caller must manage logout/expiry")
	status := &cobra.Command{Use: "status", Short: "Check credentials without displaying secrets", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			cfg, err := a.configuration(cmd, true)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Timeout)
			defer cancel()
			auth, err := mattermost.Authenticate(ctx, cfg)
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, auth.Close()) }()
			user, err := auth.Me(ctx)
			if err != nil {
				return err
			}
			return a.write(cmd, map[string]any{"authenticated": true, "user": user})
		},
	}
	logout := &cobra.Command{Use: "logout", Short: "End the supplied login session (does not revoke a PAT)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.configuration(cmd, false)
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return errors.New("auth logout requires MATTERMOST_TOKEN or --token with the session to end")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Timeout)
			defer cancel()
			auth, err := mattermost.Authenticate(ctx, cfg)
			if err != nil {
				return err
			}
			if err := auth.Logout(ctx); err != nil {
				return err
			}
			return a.write(cmd, map[string]bool{"logged_out": true})
		},
	}
	group.AddCommand(login, status, logout)
	return group
}

func exportSession(cmd *cobra.Command, auth *mattermost.Auth) error {
	return auth.WriteSessionToken(func(token string) error { _, err := fmt.Fprintln(cmd.OutOrStdout(), token); return err })
}
