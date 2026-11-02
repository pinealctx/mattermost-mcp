// Package cmd provides the CLI and local MCP bridge entrypoints.
package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pinealctx/mattermost-mcp/internal/config"
	"github.com/pinealctx/mattermost-mcp/internal/mattermost"
	"github.com/pinealctx/mattermost-mcp/pkg/server"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type app struct {
	version                              string
	url, mcpURL, token, username, output string
	timeout                              time.Duration
	passwordStdin                        bool
	cacheMode                            string
	cacheMessages                        bool
}

func NewRoot(version string) *cobra.Command {
	a := &app{version: version}
	root := &cobra.Command{
		Use: "mattermost-mcp", Short: "Mattermost CLI and local MCP bridge", Version: version,
		Long:          "Use the official Mattermost MCP endpoint from the terminal or a local stdio MCP client.\nConfiguration and credentials come only from flags, environment variables or terminal prompts.\nNo subcommand starts the local stdio bridge. No credentials are stored on disk.",
		SilenceErrors: true, SilenceUsage: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.runBridge(cmd, "") },
	}
	f := root.PersistentFlags()
	f.StringVar(&a.cacheMode, "cache", "", "Read policy: prefer, refresh or off (MATTERMOST_CACHE)")
	f.BoolVar(&a.cacheMessages, "cache-messages", false, "Allow short-lived message and search snapshots (MATTERMOST_CACHE_MESSAGES)")
	f.StringVar(&a.url, "url", "", "Mattermost base URL (MATTERMOST_URL)")
	f.StringVar(&a.mcpURL, "mcp-url", "", "Official MCP endpoint override, same origin only (MATTERMOST_MCP_URL)")
	f.StringVar(&a.token, "token", "", "PAT or session token (prefer MATTERMOST_TOKEN to keep it out of process arguments)")
	f.StringVar(&a.username, "username", "", "Username, email or LDAP login ID (MATTERMOST_USERNAME)")
	f.BoolVar(&a.passwordStdin, "password-stdin", false, "Read the login password from stdin instead of MATTERMOST_PASSWORD")
	f.DurationVar(&a.timeout, "timeout", 0, "Operation timeout (default 30s or MATTERMOST_TIMEOUT)")
	f.StringVarP(&a.output, "output", "o", "text", "Output format: text or json")
	root.AddCommand(a.cacheCommand(), a.authCommand(), a.toolsCommand(), a.connectionCommand())
	root.AddCommand(a.domainCommands()...)
	var enabled string
	bridge := &cobra.Command{Use: "mcp", Short: "Bridge official MCP tools over local stdio", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.runBridge(cmd, enabled) },
	}
	bridge.Flags().StringVar(&enabled, "enabled-tools", "", "Comma-separated tool allowlist (MATTERMOST_ENABLED_TOOLS)")
	root.AddCommand(bridge)
	return root
}

func (a *app) configuration(cmd *cobra.Command, prompt bool) (config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return cfg, err
	}
	if a.cacheMode != "" {
		cfg.CacheMode = a.cacheMode
	}
	if cmd.Flags().Changed("cache-messages") {
		cfg.CacheMessages = a.cacheMessages
	}
	if a.url != "" {
		cfg.URL = a.url
	}
	if a.mcpURL != "" {
		cfg.MCPURL = a.mcpURL
	}
	if a.token != "" {
		cfg.Token = a.token
	}
	if a.username != "" {
		cfg.Username = a.username
	}
	if a.timeout != 0 {
		cfg.Timeout = a.timeout
	}
	if a.output != "text" && a.output != "json" {
		return cfg, errors.New("output must be text or json")
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	if a.passwordStdin {
		if !prompt {
			return cfg, errors.New("--password-stdin cannot be used in MCP stdio mode")
		}
		if cfg.Token != "" {
			return cfg, errors.New("--password-stdin cannot be combined with a token")
		}
		line, err := bufio.NewReader(io.LimitReader(cmd.InOrStdin(), 65537)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return cfg, errors.New("cannot read password from stdin")
		}
		if len(line) > 65536 {
			return cfg, errors.New("password input too large")
		}
		cfg.Password = strings.TrimRight(line, "\r\n")
	}
	if prompt && cfg.Token == "" && cfg.Username != "" && cfg.Password == "" {
		in, ok := cmd.InOrStdin().(*os.File)
		if !ok || !term.IsTerminal(int(in.Fd())) {
			return cfg, errors.New("set MATTERMOST_PASSWORD or use --password-stdin in non-interactive mode")
		}
		fmt.Fprint(cmd.ErrOrStderr(), "Password: ")
		secret, err := term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return cfg, errors.New("cannot read password")
		}
		cfg.Password = string(secret)
		clear(secret)
		if cfg.MFAToken == "" {
			fmt.Fprint(cmd.ErrOrStderr(), "MFA code (Enter if not enabled): ")
			code, err := term.ReadPassword(int(in.Fd()))
			fmt.Fprintln(cmd.ErrOrStderr())
			if err != nil {
				return cfg, errors.New("cannot read MFA code")
			}
			cfg.MFAToken = strings.TrimSpace(string(code))
			clear(code)
		}
	}
	return cfg, nil
}

func (a *app) withClient(cmd *cobra.Command, fn func(context.Context, *mattermost.Client) error) (err error) {
	cfg, err := a.configuration(cmd, true)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Timeout)
	defer cancel()
	client, err := mattermost.Open(ctx, cfg, a.version)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, client.Close()) }()
	return fn(ctx, client)
}

func (a *app) runBridge(cmd *cobra.Command, enabled string) (err error) {
	cfg, err := a.configuration(cmd, false)
	if err != nil {
		return err
	}
	if enabled == "" {
		enabled = cfg.EnabledTools
	}
	client, err := mattermost.Connect(cmd.Context(), cfg, a.version)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, client.Close()) }()
	ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Timeout)
	srv, err := server.New(ctx, client, a.version, enabled, cfg.Timeout)
	cancel()
	if err != nil {
		return err
	}
	reader, ok := cmd.InOrStdin().(io.ReadCloser)
	if !ok {
		reader = io.NopCloser(cmd.InOrStdin())
	}
	return srv.Run(cmd.Context(), &mcp.IOTransport{Reader: reader, Writer: writeCloser{cmd.OutOrStdout()}})
}

type writeCloser struct{ io.Writer }

func (writeCloser) Close() error { return nil }

func (a *app) write(cmd *cobra.Command, value any) error {
	if a.output == "text" {
		if result, ok := value.(*mcp.CallToolResult); ok {
			for _, part := range result.Content {
				if text, ok := part.(*mcp.TextContent); ok {
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), text.Text); err != nil {
						return err
					}
				} else if err := json.NewEncoder(cmd.OutOrStdout()).Encode(part); err != nil {
					return err
				}
			}
			if len(result.Content) == 0 && result.StructuredContent != nil {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result.StructuredContent)
			}
			return nil
		}
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func (a *app) call(cmd *cobra.Command, name string, args any) error {
	return a.withClient(cmd, func(ctx context.Context, client *mattermost.Client) error {
		result, err := client.Call(ctx, name, args)
		return a.callResult(cmd, name, result, err)
	})
}

func (a *app) callResult(cmd *cobra.Command, name string, result *mcp.CallToolResult, err error) error {
	if err != nil {
		return err
	}
	if err := a.write(cmd, result); err != nil {
		return err
	}
	if result.IsError {
		return fmt.Errorf("upstream tool %q reported an error", name)
	}
	return nil
}
