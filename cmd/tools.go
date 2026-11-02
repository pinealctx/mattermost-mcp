package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/pinealctx/mattermost-mcp/internal/mattermost"
	"github.com/pinealctx/mattermost-mcp/internal/native"
	"github.com/spf13/cobra"
)

func (a *app) toolsCommand() *cobra.Command {
	group := &cobra.Command{Use: "tool", Aliases: []string{"tools"}, Short: "Discover and call any tool provided by the official MCP server"}
	list := &cobra.Command{Use: "list", Short: "List the complete upstream tool catalog", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.withClient(cmd, func(ctx context.Context, client *mattermost.Client) error {
				tools, err := client.Tools(ctx)
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.write(cmd, tools)
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				for _, tool := range tools {
					description := strings.SplitN(tool.Description, "\n", 2)[0]
					if _, err := fmt.Fprintf(w, "%s\t%s\n", tool.Name, description); err != nil {
						return err
					}
				}
				return w.Flush()
			})
		},
	}
	describe := &cobra.Command{Use: "describe NAME", Short: "Show the live input schema and annotations", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.withClient(cmd, func(ctx context.Context, client *mattermost.Client) error {
				tools, err := client.Tools(ctx)
				if err != nil {
					return err
				}
				for _, tool := range tools {
					if tool.Name == args[0] {
						return a.write(cmd, tool)
					}
				}
				return fmt.Errorf("tool %q is not available upstream", args[0])
			})
		},
	}
	var arguments, file string
	call := &cobra.Command{Use: "call NAME", Short: "Call a tool once; tool-level failures return a nonzero exit status", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file != "" && cmd.Flags().Changed("args") {
				return errors.New("use either --args or --args-file")
			}
			if file == "-" && a.passwordStdin {
				return errors.New("stdin cannot supply both password and tool arguments")
			}
			var data []byte
			var err error
			if file != "" {
				data, err = readFile(cmd, file)
			} else {
				data = []byte(arguments)
			}
			if err != nil {
				return err
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(data, &object); err != nil || object == nil {
				return errors.New("tool arguments must be a JSON object")
			}
			return a.call(cmd, args[0], json.RawMessage(data))
		},
	}
	call.Flags().StringVar(&arguments, "args", "{}", "JSON object of tool arguments")
	call.Flags().StringVar(&file, "args-file", "", "Read arguments from a JSON file, or - for stdin")
	group.AddCommand(list, describe, call)
	for name, spec := range native.Index {
		group.AddCommand(a.nativeCommand(name, spec))
	}
	return group
}

func readFile(cmd *cobra.Command, name string) ([]byte, error) {
	r := cmd.InOrStdin()
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, errors.New("input exceeds 4 MiB")
	}
	return data, nil
}

func (a *app) connectionCommand() *cobra.Command {
	group := &cobra.Command{Use: "connection", Short: "Check the official MCP connection"}
	group.AddCommand(&cobra.Command{Use: "test", Args: cobra.NoArgs, Short: "Verify MCP initialization, tool discovery and get_me",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.withClient(cmd, func(ctx context.Context, client *mattermost.Client) error {
				tools, err := client.LiveTools(ctx)
				if err != nil {
					return err
				}
				me, err := client.Call(ctx, "get_me", map[string]any{})
				if err != nil {
					return err
				}
				if me.IsError {
					return errors.New("MCP get_me verification failed")
				}
				init := client.Session.InitializeResult()
				return a.write(cmd, map[string]any{"connected": true, "server": init.ServerInfo, "protocol_version": init.ProtocolVersion, "tool_count": len(tools), "read_verified": true})
			})
		},
	})
	return group
}
