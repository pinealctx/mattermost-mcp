package cmd

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pinealctx/mattermost-mcp/internal/config"
	"github.com/pinealctx/mattermost-mcp/internal/history"
	"github.com/pinealctx/mattermost-mcp/internal/mattermost"
	"github.com/spf13/cobra"
)

func historyFor(ctx context.Context, cfg config.Config, auth *mattermost.Auth, dir string) (*history.Sync, error) {
	u, err := auth.VerifiedUser(ctx)
	if err != nil {
		return nil, err
	}
	store, err := history.Open(dir, strings.TrimRight(cfg.URL, "/"), u.ID, auth.HistoryScope())
	if err != nil {
		return nil, err
	}
	return &history.Sync{Store: store, Remote: auth}, nil
}
func (a *app) withHistory(cmd *cobra.Command, fn func(config.Config, *mattermost.Auth, *history.Sync) error) (err error) {
	cfg, err := a.configuration(cmd, true)
	if err != nil {
		return err
	}
	auth, err := mattermost.Authenticate(cmd.Context(), cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, auth.Close()) }()
	syncer, err := historyFor(cmd.Context(), cfg, auth, os.Getenv("MATTERMOST_HISTORY_DIR"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, syncer.Store.Close()) }()
	return fn(cfg, auth, syncer)
}
func scopeName(id string) string {
	if id == "" {
		return "all"
	}
	return id
}

type workerInput struct {
	URL, Token, User, Dir, Scope string
	IDs                          []string
	Timeout                      time.Duration
	Owns                         bool
}

func spawn(ctx context.Context, cfg config.Config, auth *mattermost.Auth, scope string, ids []string, retain bool) error {
	if len(ids) == 0 {
		return nil
	}
	return auth.WithWorkerSession(retain, func(token string, owns bool) error {
		handoff := workerInput{URL: cfg.URL, Token: token, User: auth.User.ID, Dir: os.Getenv("MATTERMOST_HISTORY_DIR"), Scope: scopeName(scope), IDs: ids, Timeout: cfg.Timeout, Owns: owns}
		return launchWorker(ctx, handoff)
	})
}

// A private pipe passes the bearer to a detached one-shot worker. Never put it in argv, a job file or logs.
func launchWorker(ctx context.Context, in workerInput) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	input, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer input.Close()
	defer writer.Close()
	reader, ack, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	defer ack.Close()
	child := exec.Command(exe, "sync", "_worker")
	child.Stdin = input
	child.Stdout = ack
	for _, s := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(s), "MATTERMOST_") {
			child.Env = append(child.Env, s)
		}
	}
	detach(child)
	if err = child.Start(); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	_ = input.Close()
	_ = ack.Close()
	if err = json.NewEncoder(writer).Encode(in); err != nil {
		return err
	}
	_ = writer.Close()
	ready := make(chan bool, 1)
	go func() {
		line, readErr := bufio.NewReader(reader).ReadString('\n')
		ready <- readErr == nil && line == "ready\n"
	}()
	select {
	case ok := <-ready:
		if !ok {
			return errors.New("history worker failed to initialize; recent pages are durable")
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(in.Timeout + 10*time.Second):
		return errors.New("history worker startup timed out")
	}
	return child.Process.Release()
}
func worker(cmd *cobra.Command, _ []string) (err error) {
	var in workerInput
	if err = json.NewDecoder(cmd.InOrStdin()).Decode(&in); err != nil {
		return errors.New("invalid history worker handoff")
	}
	if in.Scope != "all" {
		if err = mattermost.ValidateID(in.Scope); err != nil {
			return err
		}
	}
	for _, id := range in.IDs {
		if err = mattermost.ValidateID(id); err != nil {
			return err
		}
		if in.Scope != "all" && id != in.Scope {
			return errors.New("invalid worker scope")
		}
	}
	cfg := config.Config{URL: in.URL, Token: in.Token, Timeout: in.Timeout}
	auth, err := mattermost.Authenticate(cmd.Context(), cfg)
	if err != nil {
		return err
	}
	e, err := historyFor(cmd.Context(), cfg, auth, in.Dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, e.Store.Close()) }()
	if auth.User.ID != in.User {
		return errors.New("worker account changed")
	}
	acknowledged := false
	defer func() {
		if acknowledged && in.Owns {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = errors.Join(err, auth.Logout(ctx))
		}
	}()
	return e.RunJob(cmd.Context(), in.Scope, in.IDs, func() error {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "ready")
		acknowledged = err == nil
		return err
	})
}
func (a *app) syncCommand() *cobra.Command {
	group := &cobra.Command{Use: "sync", Short: "Synchronize persistent, account-isolated SQLite message history"}
	var only string
	group.PersistentFlags().StringVar(&only, "channel", "", "Limit synchronization to one joined channel")
	start := &cobra.Command{Use: "start", Aliases: []string{"resume"}, Short: "Fetch recent pages, then resume historical backfill in the background", Args: cobra.NoArgs}
	var foreground bool
	start.Flags().BoolVar(&foreground, "foreground", false, "Stay attached until backfill is complete")
	start.RunE = func(cmd *cobra.Command, _ []string) error {
		return a.withHistory(cmd, func(cfg config.Config, auth *mattermost.Auth, e *history.Sync) error {
			ids, recentErr := e.Recent(cmd.Context(), only)
			var err error
			if foreground {
				err = e.RunJob(cmd.Context(), scopeName(only), ids, nil)
			} else {
				err = spawn(cmd.Context(), cfg, auth, only, ids, false)
			}
			if err = errors.Join(recentErr, err); err != nil {
				return err
			}
			return a.write(cmd, map[string]any{"initialized_channels": len(ids), "scope": scopeName(only), "background": !foreground && len(ids) > 0})
		})
	}
	status := &cobra.Command{Use: "status", Short: "Show sync checkpoints and last worker status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return a.withHistory(cmd, func(_ config.Config, _ *mattermost.Auth, e *history.Sync) error {
			ids, err := e.Discover(cmd.Context(), only)
			if err != nil {
				return err
			}
			states := []history.State{}
			for _, id := range ids {
				if only != "" {
					if _, err := e.Remote.Member(cmd.Context(), id); err != nil {
						return err
					}
				}
				s, err := e.Store.State(cmd.Context(), id)
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				states = append(states, s)
			}
			jobs, err := e.Store.Jobs(cmd.Context(), only)
			if err != nil {
				return err
			}
			return a.write(cmd, map[string]any{"channels": states, "jobs": jobs})
		})
	}}
	stop := &cobra.Command{Use: "stop", Short: "Stop the matching background job at a page boundary", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return a.withHistory(cmd, func(_ config.Config, _ *mattermost.Auth, e *history.Sync) error {
			return e.Store.Stop(cmd.Context(), scopeName(only))
		})
	}}
	group.AddCommand(start, status, stop, &cobra.Command{Use: "_worker", Hidden: true, Args: cobra.NoArgs, RunE: worker})
	return group
}
func (a *app) openChannel() *cobra.Command {
	var limit int
	cmd := &cobra.Command{Use: "open CHANNEL_ID", Short: "Show the local SQLite timeline, then its reconciled view", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if limit < 1 || limit > 100 {
			return errors.New("limit must be between 1 and 100")
		}
		return a.withHistory(cmd, func(cfg config.Config, auth *mattermost.Auth, e *history.Sync) error {
			id := args[0]
			emit := func(phase string, posts []mattermost.HistoryPost) error {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"phase": phase, "channel_id": id, "messages": posts})
			}
			posts, err := e.Refresh(cmd.Context(), id, limit, func(local []mattermost.HistoryPost) error { return emit("local", local) })
			if err != nil {
				return err
			}
			if err = emit("reconciled", posts); err != nil {
				return err
			}
			s, err := e.Store.State(cmd.Context(), id)
			if err != nil {
				return err
			}
			if s.Phase != "complete" {
				return spawn(cmd.Context(), cfg, auth, id, []string{id}, false)
			}
			return nil
		})
	}}
	cmd.Flags().IntVar(&limit, "limit", 30, "Visible messages per view (1-100)")
	return cmd
}
