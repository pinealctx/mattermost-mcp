// Package mattermost implements session authentication and an official MCP client.
package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pinealctx/mattermost-mcp/internal/config"
)

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// Auth owns only sessions created by this process. Injected tokens are never revoked implicitly.
type Auth struct {
	cfg    config.Config
	client *http.Client
	token  string
	owned  bool
	User   User
}

type APIError struct {
	Status int
	ID     string
}

func (e *APIError) Error() string {
	if e.Status == 401 {
		return fmt.Sprintf("Mattermost authentication failed (HTTP 401, %s); check credentials, session expiry and MFA", e.ID)
	}
	return fmt.Sprintf("Mattermost request failed (HTTP %d, %s)", e.Status, e.ID)
}

type bearerTransport struct {
	base   http.RoundTripper
	origin *url.URL
	token  string
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !config.SameOrigin(t.origin, req.URL) {
		return nil, errors.New("refusing to send credentials to another origin")
	}
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}

func newHTTPClient(cfg config.Config, token string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = cfg.Timeout
	var rt http.RoundTripper = transport
	if token != "" {
		origin, _ := url.Parse(cfg.URL)
		rt = bearerTransport{base: transport, origin: origin, token: token}
	}
	return &http.Client{Transport: rt, Timeout: cfg.Timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func Authenticate(ctx context.Context, cfg config.Config) (*Auth, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	a := &Auth{cfg: cfg, token: cfg.Token}
	if a.token != "" {
		a.client = newHTTPClient(cfg, a.token)
		return a, nil
	}
	if cfg.Username == "" || cfg.Password == "" {
		return nil, errors.New("set MATTERMOST_TOKEN, or MATTERMOST_USERNAME and MATTERMOST_PASSWORD (MFA: MATTERMOST_MFA_TOKEN)")
	}
	client := newHTTPClient(cfg, "")
	payload := map[string]string{"login_id": cfg.Username, "password": cfg.Password}
	if cfg.MFAToken != "" {
		payload["token"] = cfg.MFAToken
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL+"/api/v4/users/login", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("mattermost login request failed; check connectivity and TLS")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp)
	}
	a.token = resp.Header.Get("Token")
	if a.token == "" {
		return nil, errors.New("login returned no session token")
	}
	a.owned = true
	a.client = newHTTPClient(cfg, a.token)
	// The token is sufficient; a malformed user response must not leak a newly created session.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&a.User); err != nil {
		cleanupErr := a.Close()
		return nil, errors.Join(errors.New("invalid login response"), cleanupErr)
	}
	a.cfg.Password, a.cfg.MFAToken = "", ""
	return a, nil
}

func apiError(resp *http.Response) error {
	var body struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body)
	// IDs are machine identifiers, never echo arbitrary upstream messages or bodies.
	if len(body.ID) > 160 || strings.ContainsAny(body.ID, " \r\n\t") {
		body.ID = ""
	}
	return &APIError{Status: resp.StatusCode, ID: body.ID}
}

func (a *Auth) Me(ctx context.Context) (User, error) {
	var user User
	err := a.request(ctx, http.MethodGet, "/api/v4/users/me", nil, &user)
	return user, err
}

func (a *Auth) request(ctx context.Context, method, path string, body io.Reader, dest any) error {
	req, err := http.NewRequestWithContext(ctx, method, a.cfg.URL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return errors.New("mattermost request failed; check connectivity, TLS or timeout")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if dest != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(dest)
	}
	return nil
}

// WriteSessionToken transfers session lifetime management only after a successful export.
func (a *Auth) WriteSessionToken(write func(string) error) error {
	if err := write(a.token); err != nil {
		return err
	}
	a.owned = false
	return nil
}

func (a *Auth) Logout(ctx context.Context) error {
	if err := a.request(ctx, http.MethodPost, "/api/v4/users/logout", strings.NewReader("{}"), nil); err != nil {
		return err
	}
	a.owned = false
	return nil
}

func (a *Auth) Close() error {
	if !a.owned {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), min(a.cfg.Timeout, 10*time.Second))
	defer cancel()
	if err := a.Logout(ctx); err != nil {
		return fmt.Errorf("temporary login session cleanup failed; end it in account security settings: %w", err)
	}
	return nil
}

func (a *Auth) redact(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	for _, secret := range []string{a.token, a.cfg.Password, a.cfg.MFAToken} {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[REDACTED]")
		}
	}
	return errors.New(s)
}
