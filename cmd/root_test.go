package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pinealctx/mattermost-mcp/internal/testutil"
)

func environment(t *testing.T) {
	t.Helper()
	t.Setenv("MATTERMOST_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("MATTERMOST_CACHE", "prefer")
	t.Setenv("MATTERMOST_CACHE_MESSAGES", "false")
	for _, key := range []string{"URL", "MCP_URL", "TOKEN", "USERNAME", "PASSWORD", "MFA_TOKEN", "TIMEOUT", "ENABLED_TOOLS"} {
		t.Setenv("MATTERMOST_"+key, "")
	}
}

func execute(args ...string) (string, string, error) {
	cmd := NewRoot("test")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func TestHelpWithoutCredentials(t *testing.T) {
	environment(t)
	for _, args := range [][]string{{"--help"}, {"--version"}, {"tool", "call", "--help"}} {
		if _, _, err := execute(args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCLIConnectionAndToolErrors(t *testing.T) {
	environment(t)
	f := testutil.New(t, true)
	t.Setenv("MATTERMOST_URL", f.HTTP.URL)
	t.Setenv("MATTERMOST_USERNAME", "test-user")
	t.Setenv("MATTERMOST_PASSWORD", "test-password")
	t.Setenv("MATTERMOST_MFA_TOKEN", "123456")
	out, _, err := execute("connection", "test", "-o", "json")
	if err != nil || !strings.Contains(out, `"connected": true`) {
		t.Fatalf("%s %v", out, err)
	}
	_, logout, _ := f.Counts()
	if logout != 1 {
		t.Fatal("CLI did not clean up password session")
	}
	out, _, err = execute("tool", "call", "tool_failure", "-o", "json")
	if err == nil || !strings.Contains(out, `"isError": true`) {
		t.Fatalf("tool error lost: %s %v", out, err)
	}
	_, _, err = execute("tool", "call", "get_me", "--args", "[]")
	if err == nil {
		t.Fatal("non-object arguments accepted")
	}
}

func TestGenericArgumentsAndMessagePreserved(t *testing.T) {
	environment(t)
	f := testutil.New(t, true)
	t.Setenv("MATTERMOST_URL", f.HTTP.URL)
	t.Setenv("MATTERMOST_TOKEN", testutil.PAT)
	_, _, err := execute("tool", "call", "get_team_info", "--args", `{"large":9007199254740993}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(f.LastArgs), "9007199254740993") {
		t.Fatal("integer precision lost")
	}
	message := "first line\n`literal` $value\n最后一行"
	_, _, err = execute("post", "create", "fixture-channel", "--message", message, "--root-id", "fixture-root", "--channel-display-name", "Test channel", "--team-display-name", "Test team")
	if err != nil {
		t.Fatal(err)
	}
	var args map[string]string
	if err := json.Unmarshal(f.LastArgs, &args); err != nil {
		t.Fatal(err)
	}
	if args["message"] != message || args["root_id"] != "fixture-root" || args["channel_display_name"] != "Test channel" || args["team_display_name"] != "Test team" {
		t.Fatal("message changed")
	}
	_, logout, calls := f.Counts()
	if logout != 0 || calls != 2 {
		t.Fatal("unexpected retries or logout")
	}
}

func TestAuthExportAndStdioInputConflict(t *testing.T) {
	environment(t)
	f := testutil.New(t, true)
	t.Setenv("MATTERMOST_URL", f.HTTP.URL)
	t.Setenv("MATTERMOST_USERNAME", "test-user")
	t.Setenv("MATTERMOST_PASSWORD", "test-password")
	t.Setenv("MATTERMOST_MFA_TOKEN", "123456")
	out, _, err := execute("auth", "login", "--no-sync", "--token-only")
	if err != nil || strings.TrimSpace(out) != testutil.Session {
		t.Fatalf("export failed: %v", err)
	}
	_, logout, _ := f.Counts()
	if logout != 0 {
		t.Fatal("exported session logged out")
	}
	t.Setenv("MATTERMOST_TOKEN", testutil.Session)
	if _, _, err = execute("auth", "logout"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = execute("mcp", "--password-stdin"); err == nil {
		t.Fatal("stdio password input accepted")
	}
	if _, _, err = execute("tool", "call", "get_me", "--args-file", "-", "--password-stdin"); err == nil {
		t.Fatal("stdin collision accepted")
	}
}
