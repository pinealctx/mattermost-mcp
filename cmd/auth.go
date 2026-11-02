package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/pinealctx/mattermost-mcp/internal/mattermost"
	"github.com/spf13/cobra"
)

func (a *app) authCommand() *cobra.Command {
	group := &cobra.Command{Use: "auth", Short: "Validate, export or end a login session"}
	var tokenOnly, noSync bool
	var only string
	login := &cobra.Command{Use: "login", Short: "Validate password login; optionally export its session token", Args: cobra.NoArgs,
		Long: "Synchronize the most recent page in each joined conversation, then launch a resumable background backfill. A temporary login session is logged out when its worker finishes. Use --no-sync for validation only. With --token-only, export a new password session for an external credential manager.",
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			cfg, err := a.configuration(cmd, true)
			if err != nil {
				return err
			}
			if cfg.Token != "" && tokenOnly {
				return errors.New("auth login requires password credentials; unset MATTERMOST_TOKEN or use auth status")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Timeout)
			defer cancel()
			auth, err := mattermost.Authenticate(ctx, cfg)
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, auth.Close()) }()
			if !noSync {
				e, openErr := historyFor(cmd.Context(), cfg, auth, os.Getenv("MATTERMOST_HISTORY_DIR"))
				if openErr != nil {
					return openErr
				}
				defer func() { err = errors.Join(err, e.Store.Close()) }()
				ids, initErr := e.Recent(cmd.Context(), only)
				if initErr != nil {
					if len(ids) == 0 {
						return initErr
					}
					fmt.Fprintln(cmd.ErrOrStderr(), "Some recent pages could not be initialized; resume with sync start:", initErr)
				}
				if launchErr := spawn(cmd.Context(), cfg, auth, only, ids, tokenOnly); launchErr != nil {
					return launchErr
				}
			} else if cfg.Token != "" {
				if _, err := auth.VerifiedUser(cmd.Context()); err != nil {
					return err
				}
			}
			if tokenOnly {
				// Transfer ownership only after output has succeeded.
				return exportSession(cmd, auth)
			}
			return a.write(cmd, map[string]any{"authenticated": true, "user": auth.User, "history_sync": !noSync})
		},
	}
	login.Flags().BoolVar(&noSync, "no-sync", false, "Validate login without initializing message history")
	login.Flags().StringVar(&only, "sync-channel", "", "Limit login synchronization to one channel")
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
