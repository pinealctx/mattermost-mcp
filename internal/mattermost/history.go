package mattermost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// HistoryPost is the portable timeline record. Attachment bodies and integration props are not mirrored.
type HistoryPost struct {
	ID        string   `json:"id"`
	ChannelID string   `json:"channel_id"`
	RootID    string   `json:"root_id"`
	UserID    string   `json:"user_id"`
	Type      string   `json:"type"`
	Message   string   `json:"message"`
	CreateAt  int64    `json:"create_at"`
	UpdateAt  int64    `json:"update_at"`
	DeleteAt  int64    `json:"delete_at,omitempty"`
	FileIDs   []string `json:"file_ids,omitempty"`
}
type HistoryPage struct {
	Order []string               `json:"order"`
	Posts map[string]HistoryPost `json:"posts"`
}

var objectID = regexp.MustCompile("^[a-z0-9]{26}$")

func ValidateID(id string) error {
	if !objectID.MatchString(id) {
		return errors.New("expected a Mattermost object ID")
	}
	return nil
}
func Inaccessible(err error) bool {
	var e *APIError
	return errors.As(err, &e) && (e.Status == 403 || e.Status == 404)
}

// History requests never modify server state. POST /posts/ids is a batch lookup.
func (a *Auth) historyJSON(ctx context.Context, path string, query url.Values, body []byte, dst any) (int64, error) {
	u := a.cfg.URL + "/api/v4/" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.readWithBackoff(ctx, req, body)
	if err != nil {
		return 0, fmt.Errorf("history request failed: %w", a.redact(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, apiError(resp)
	}
	const limit = 64 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return 0, err
	}
	if len(raw) > limit {
		return 0, errors.New("history response exceeds 64 MiB")
	}
	if err = json.Unmarshal(raw, dst); err != nil {
		return 0, fmt.Errorf("invalid history response: %w", err)
	}
	// Use the server clock, conservatively overlapping at least one minute.
	date, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		return 1, nil
	}
	return max(int64(1), date.Add(-time.Minute).UnixMilli()), nil
}

// Retry throttled reads only, respecting the server delay and caller cancellation.
func (a *Auth) readWithBackoff(ctx context.Context, req *http.Request, body []byte) (*http.Response, error) {
	for i := 0; ; i++ {
		copy := req.Clone(ctx)
		if body != nil {
			copy.Body = io.NopCloser(bytes.NewReader(body))
		}
		resp, err := a.client.Do(copy)
		if err != nil || i >= 2 || (resp.StatusCode != 429 && resp.StatusCode != 503) {
			return resp, err
		}
		delay := time.Duration(1<<i) * time.Second
		value := resp.Header.Get("Retry-After")
		if n, parseErr := strconv.Atoi(value); parseErr == nil && n >= 0 {
			if n > 60 {
				return resp, nil
			}
			delay = time.Duration(n) * time.Second
		} else if deadline, parseErr := http.ParseTime(value); parseErr == nil {
			delay = max(time.Duration(0), time.Until(deadline))
		}
		if delay > time.Minute {
			return resp, nil
		}
		_ = resp.Body.Close()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

func (a *Auth) Channels(ctx context.Context) ([]string, error) {
	var rows []struct {
		ID       string
		DeleteAt int64 `json:"delete_at"`
	}
	_, err := a.historyJSON(ctx, "users/me/channels", nil, nil, &rows)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("invalid membership list")
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if err := ValidateID(row.ID); err != nil {
			return nil, err
		}
		if row.DeleteAt == 0 {
			ids = append(ids, row.ID)
		}
	}
	return ids, nil
}
func (a *Auth) Member(ctx context.Context, id string) (int64, error) {
	if err := ValidateID(id); err != nil {
		return 0, err
	}
	var r struct {
		Channel string `json:"channel_id"`
		User    string `json:"user_id"`
	}
	stamp, err := a.historyJSON(ctx, "channels/"+id+"/members/me", nil, nil, &r)
	if err == nil && (r.Channel != id || r.User != a.User.ID) {
		return 0, errors.New("membership identity mismatch")
	}
	return stamp, err
}
func (a *Auth) Posts(ctx context.Context, id string, q url.Values) (HistoryPage, error) {
	var p HistoryPage
	if err := ValidateID(id); err != nil {
		return p, err
	}
	q.Set("collapsedThreads", "false")
	q.Set("skipFetchThreads", "true")
	_, err := a.historyJSON(ctx, "channels/"+id+"/posts", q, nil, &p)
	if err != nil {
		return p, err
	}
	if p.Order == nil || p.Posts == nil {
		return p, errors.New("invalid history page")
	}
	for _, k := range p.Order {
		post, ok := p.Posts[k]
		if !ok || post.ID != k || post.ChannelID != id || post.CreateAt <= 0 {
			return p, errors.New("invalid ordered post")
		}
	}
	return p, nil
}
func (a *Auth) Lookup(ctx context.Context, ids []string) ([]HistoryPost, error) {
	body, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	var posts []HistoryPost
	_, err = a.historyJSON(ctx, "posts/ids", nil, body, &posts)
	// Some versions report all missing IDs as 404. Verify individually in that case.
	var e *APIError
	if errors.As(err, &e) && (e.Status == 404 || e.Status == 403) {
		posts = []HistoryPost{}
		for _, id := range ids {
			if err := ValidateID(id); err != nil {
				return nil, err
			}
			var p HistoryPost
			_, oneErr := a.historyJSON(ctx, "posts/"+id, nil, nil, &p)
			if Inaccessible(oneErr) {
				continue
			}
			if oneErr != nil {
				return nil, oneErr
			}
			posts = append(posts, p)
		}
		return posts, nil
	}
	if err == nil && posts == nil {
		return nil, errors.New("invalid batch lookup")
	}
	return posts, err
}

func (a *Auth) VerifiedUser(ctx context.Context) (User, error) {
	u, err := a.Me(ctx)
	if err != nil {
		return User{}, err
	}
	if u.ID == "" {
		return User{}, errors.New("missing verified user ID")
	}
	a.User = u
	return u, nil
}

// WithWorkerSession hands an owned temporary session to the worker only after launch succeeds.
func (a *Auth) WithWorkerSession(retain bool, start func(string, bool) error) error {
	transfer := a.owned && !retain
	if err := start(a.token, transfer); err != nil {
		return err
	}
	if transfer {
		a.owned = false
	}
	return nil
}

// HistoryScope identifies the bearer that observed a row without storing the bearer.
func (a *Auth) HistoryScope() string { return fmt.Sprintf("%x", sha256.Sum256([]byte(a.token))) }
