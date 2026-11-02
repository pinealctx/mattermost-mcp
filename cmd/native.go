package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pinealctx/mattermost-mcp/internal/mattermost"
	"github.com/pinealctx/mattermost-mcp/internal/native"
	"github.com/spf13/cobra"
)

func (a *app) nativeCommand(name string, spec native.Spec) *cobra.Command {
	use := strings.ReplaceAll(name, "_", "-")
	cmd := &cobra.Command{Use: use, Short: "Call the built-in " + name + " tool", Args: cobra.NoArgs}
	if use != name {
		cmd.Aliases = []string{name}
	}
	flags := cmd.Flags()
	var file string
	flags.StringVar(&file, "args-file", "", "Supply the full argument object from a JSON file or - for stdin")
	fields := make(map[string]string, len(spec.Fields))
	for field, kind := range spec.Fields {
		// Use --args-file for secret-bearing fields.
		if field == "password" {
			continue
		}
		flag := strings.ReplaceAll(field, "_", "-")
		// Avoid shadowing global authentication and output flags.
		switch flag {
		case "username", "token", "url", "mcp-url", "timeout", "output", "password-stdin", "cache", "cache-messages":
			flag = "arg-" + flag
		}
		fields[field] = flag
		switch kind {
		case "bool":
			flags.Bool(flag, false, field+" (boolean)")
		case "int", "int64":
			flags.Int64(flag, 0, field+" (integer)")
		default:
			flags.String(flag, "", field+" ("+kind+")")
		}
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		args := map[string]any{}
		for field, flag := range fields {
			if !flags.Changed(flag) {
				continue
			}
			switch spec.Fields[field] {
			case "bool":
				args[field], _ = flags.GetBool(flag)
			case "int", "int64":
				args[field], _ = flags.GetInt64(flag)
			case "json":
				raw, _ := flags.GetString(flag)
				if !json.Valid([]byte(raw)) {
					return fmt.Errorf("--%s requires JSON", flag)
				}
				args[field] = json.RawMessage(raw)
			default:
				args[field], _ = flags.GetString(flag)
			}
		}
		var input any = args
		if flags.Changed("args-file") {
			if len(args) != 0 {
				return fmt.Errorf("use --args-file or individual argument flags")
			}
			if file == "-" && a.passwordStdin {
				return fmt.Errorf("stdin cannot supply both password and tool arguments")
			}
			data, err := readFile(cmd, file)
			if err != nil {
				return err
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(data, &object); err != nil || object == nil {
				return fmt.Errorf("tool arguments must be a JSON object")
			}
			input = json.RawMessage(data)
		}
		return a.withClient(cmd, func(ctx context.Context, c *mattermost.Client) error {
			result, err := c.CallNative(ctx, name, input)
			return a.callResult(cmd, name, result, err)
		})
	}
	return cmd
}
