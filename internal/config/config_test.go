package config

import (
	"testing"
	"time"
)

func TestURLs(t *testing.T) {
	for _, tc := range []struct {
		name, base, mcp string
		valid           bool
	}{
		{"https", "https://chat.example.com", "", true},
		{"subpath", "https://chat.example.com/chat/", "", true},
		{"loopback", "http://127.0.0.1:8080", "", true},
		{"ipv6", "http://[::1]:8080", "", true},
		{"remote_http", "http://chat.example.com", "", false},
		{"userinfo", "https://user:password@chat.example.com", "", false},
		{"query", "https://chat.example.com?token=sensitive", "", false},
		{"foreign_mcp", "https://chat.example.com", "https://other.example.com/mcp", false},
		{"downgrade", "https://localhost", "http://localhost/mcp", false},
		{"default_port", "https://chat.example.com", "https://chat.example.com:443/mcp", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{URL: tc.base, MCPURL: tc.mcp, Timeout: time.Second}
			if err := cfg.Validate(); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if tc.valid && tc.mcp == "" && cfg.MCPURL != cfg.URL+"/plugins/mattermost-ai/mcp-server/mcp" {
				t.Fatal("incorrect endpoint")
			}
		})
	}
}

func TestTimeoutEnvironment(t *testing.T) {
	t.Setenv("MATTERMOST_TIMEOUT", "invalid")
	if _, err := Load(); err == nil {
		t.Fatal("invalid timeout accepted")
	}
	t.Setenv("MATTERMOST_TIMEOUT", "2m")
	cfg, err := Load()
	if err != nil || cfg.Timeout != 2*time.Minute {
		t.Fatalf("config=%+v error=%v", cfg, err)
	}
}
