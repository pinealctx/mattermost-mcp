// Package config defines the external configuration contract. It does not read files or secret stores.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	URL, MCPURL, Token, Username, Password, MFAToken string
	Timeout                                          time.Duration
	EnabledTools                                     string
}

func Load() (Config, error) {
	c := Config{
		URL: os.Getenv("MATTERMOST_URL"), MCPURL: os.Getenv("MATTERMOST_MCP_URL"),
		Token: os.Getenv("MATTERMOST_TOKEN"), Username: os.Getenv("MATTERMOST_USERNAME"),
		Password: os.Getenv("MATTERMOST_PASSWORD"), MFAToken: os.Getenv("MATTERMOST_MFA_TOKEN"),
		EnabledTools: os.Getenv("MATTERMOST_ENABLED_TOOLS"), Timeout: 30 * time.Second,
	}
	if value := os.Getenv("MATTERMOST_TIMEOUT"); value != "" {
		var err error
		c.Timeout, err = time.ParseDuration(value)
		if err != nil {
			return c, fmt.Errorf("MATTERMOST_TIMEOUT must be a duration, for example 30s")
		}
	}
	return c, nil
}

func (c *Config) Validate() error {
	if c.URL == "" {
		return fmt.Errorf("set MATTERMOST_URL or --url")
	}
	u, err := parseURL(c.URL)
	if err != nil {
		return fmt.Errorf("invalid Mattermost URL: %w", err)
	}
	c.URL = strings.TrimRight(u.String(), "/")
	if c.MCPURL == "" {
		c.MCPURL = c.URL + "/plugins/mattermost-ai/mcp-server/mcp"
	}
	m, err := parseURL(c.MCPURL)
	if err != nil {
		return fmt.Errorf("invalid MCP URL: %w", err)
	}
	if !SameOrigin(u, m) {
		return fmt.Errorf("MCP URL must use the same origin as the Mattermost URL")
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	return nil
}

func parseURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("expected a URL without embedded credentials, query or fragment")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return nil, fmt.Errorf("HTTPS is required (HTTP is allowed only on loopback for local development)")
	}
	return u, nil
}

func SameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}
