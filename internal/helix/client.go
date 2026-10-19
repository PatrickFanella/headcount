// Package helix is a small Twitch Helix client built around a shared,
// prioritized request budget. Twitch gives an app access token 800 points per
// minute; most endpoints used here cost one point.
package helix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apiBase  = "https://api.twitch.tv/helix"
	tokenURL = "https://id.twitch.tv/oauth2/token"
)

// Priority decides how much of the remaining budget a request may use.
// Lower-priority requests wait while the bucket is close to empty so that the
// per-minute viewer poll is never starved.
type Priority int

const (
	PriorityHigh   Priority = iota // discovery and viewer polling
	PriorityMedium                 // follower totals
	PriorityLow                    // user lookups
)

// reserve is the number of points that must remain in the bucket before a
// request of the given priority is sent.
var reserve = map[Priority]int{
	PriorityHigh:   5,
	PriorityMedium: 120,
	PriorityLow:    300,
}

type Client struct {
	clientID     string
	clientSecret string
	http         *http.Client

	tokenMu     sync.Mutex
	token       string
	tokenExpiry time.Time

	budgetMu  sync.Mutex
	remaining int
	limit     int
	resetAt   time.Time

	statsMu  sync.Mutex
	requests int
	failures int
}

func New(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		http:         &http.Client{Timeout: 15 * time.Second},
		remaining:    800,
		limit:        800,
	}
}

// Stats returns cumulative request and failure counts.
func (c *Client) Stats() (requests, failures, remaining int) {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	c.budgetMu.Lock()
	defer c.budgetMu.Unlock()
	return c.requests, c.failures, c.remaining
}

func (c *Client) accessToken(ctx context.Context, force bool) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if !force && c.token != "" && time.Until(c.tokenExpiry) > time.Hour {
		return c.token, nil
	}
	form := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"grant_type":    {"client_credentials"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("token request: %s: %s", resp.Status, body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("token response: %w", err)
	}
	c.token = tok.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return c.token, nil
}

// acquire blocks until the bucket holds more than the priority's reserve.
func (c *Client) acquire(ctx context.Context, p Priority) error {
	for {
		c.budgetMu.Lock()
		now := time.Now()
		if !c.resetAt.IsZero() && now.After(c.resetAt) {
			c.remaining = c.limit
			c.resetAt = time.Time{}
		}
		if c.remaining > reserve[p] {
			c.remaining-- // optimistic; corrected by the response headers
			c.budgetMu.Unlock()
			return nil
		}
		wait := time.Until(c.resetAt)
		if c.resetAt.IsZero() || wait <= 0 {
			wait = time.Second
		}
		c.budgetMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait + 50*time.Millisecond):
		}
	}
}

func (c *Client) updateBudget(h http.Header) {
	rem, err1 := strconv.Atoi(h.Get("Ratelimit-Remaining"))
	reset, err2 := strconv.ParseInt(h.Get("Ratelimit-Reset"), 10, 64)
	limit, err3 := strconv.Atoi(h.Get("Ratelimit-Limit"))
	c.budgetMu.Lock()
	defer c.budgetMu.Unlock()
	if err1 == nil {
		c.remaining = rem
	}
	if err2 == nil {
		c.resetAt = time.Unix(reset, 0)
	}
	if err3 == nil && limit > 0 {
		c.limit = limit
	}
}

var ErrNotFound = errors.New("not found")

func (c *Client) get(ctx context.Context, p Priority, path string, q url.Values, out any) error {
	var lastErr error
	for attempt := range 4 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * time.Second):
			}
		}
		if err := c.acquire(ctx, p); err != nil {
			return err
		}
		tok, err := c.accessToken(ctx, false)
		if err != nil {
			lastErr = err
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+path+"?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Client-Id", c.clientID)
		req.Header.Set("Authorization", "Bearer "+tok)
		c.statsMu.Lock()
		c.requests++
		c.statsMu.Unlock()
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			c.countFailure()
			continue
		}
		c.updateBudget(resp.Header)
		switch {
		case resp.StatusCode == http.StatusOK:
			err = json.NewDecoder(resp.Body).Decode(out)
			resp.Body.Close()
			return err
		case resp.StatusCode == http.StatusUnauthorized:
			resp.Body.Close()
			c.countFailure()
			if _, err := c.accessToken(ctx, true); err != nil {
				lastErr = err
			} else {
				lastErr = errors.New("unauthorized; token refreshed")
			}
		case resp.StatusCode == http.StatusNotFound:
			resp.Body.Close()
			return ErrNotFound
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			resp.Body.Close()
			c.countFailure()
			lastErr = fmt.Errorf("%s %s: %s", path, resp.Status, body)
		default:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			c.countFailure()
			return fmt.Errorf("%s %s: %s", path, resp.Status, body)
		}
	}
	return lastErr
}

func (c *Client) countFailure() {
	c.statsMu.Lock()
	c.failures++
	c.statsMu.Unlock()
}

type Stream struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	UserLogin   string    `json:"user_login"`
	UserName    string    `json:"user_name"`
	GameID      string    `json:"game_id"`
	GameName    string    `json:"game_name"`
	Type        string    `json:"type"`
	Title       string    `json:"title"`
	ViewerCount int       `json:"viewer_count"`
	StartedAt   time.Time `json:"started_at"`
	Language    string    `json:"language"`
	IsMature    bool      `json:"is_mature"`
	Tags        []string  `json:"tags"`
}

type streamsResponse struct {
	Data       []Stream `json:"data"`
	Pagination struct {
		Cursor string `json:"cursor"`
	} `json:"pagination"`
}

// TopStreams returns up to n live streams ordered by viewer count.
func (c *Client) TopStreams(ctx context.Context, n int) ([]Stream, error) {
	var out []Stream
	seen := map[string]bool{}
	cursor := ""
	for len(out) < n {
		q := url.Values{"first": {"100"}, "type": {"live"}}
		if cursor != "" {
			q.Set("after", cursor)
		}
		var r streamsResponse
		if err := c.get(ctx, PriorityHigh, "/streams", q, &r); err != nil {
			return out, err
		}
		for _, s := range r.Data {
			// Pages are computed on a moving list, so a stream can appear twice.
			if !seen[s.UserID] {
				seen[s.UserID] = true
				out = append(out, s)
			}
		}
		if r.Pagination.Cursor == "" || len(r.Data) == 0 {
			break
		}
		cursor = r.Pagination.Cursor
	}
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// StreamsByUserIDs returns the live streams among the given broadcaster IDs.
func (c *Client) StreamsByUserIDs(ctx context.Context, ids []string) ([]Stream, error) {
	var out []Stream
	for start := 0; start < len(ids); start += 100 {
		end := min(start+100, len(ids))
		q := url.Values{"first": {"100"}, "type": {"live"}}
		for _, id := range ids[start:end] {
			q.Add("user_id", id)
		}
		var r streamsResponse
		if err := c.get(ctx, PriorityHigh, "/streams", q, &r); err != nil {
			return out, err
		}
		out = append(out, r.Data...)
	}
	return out, nil
}

// FollowerTotal returns a channel's follower count. An app token receives the
// total without the follower list.
func (c *Client) FollowerTotal(ctx context.Context, broadcasterID string) (int64, error) {
	var r struct {
		Total int64 `json:"total"`
	}
	q := url.Values{"broadcaster_id": {broadcasterID}, "first": {"1"}}
	if err := c.get(ctx, PriorityMedium, "/channels/followers", q, &r); err != nil {
		return 0, err
	}
	return r.Total, nil
}

type User struct {
	ID              string    `json:"id"`
	Login           string    `json:"login"`
	DisplayName     string    `json:"display_name"`
	BroadcasterType string    `json:"broadcaster_type"`
	CreatedAt       time.Time `json:"created_at"`
}

// UsersByLogin looks up at most 100 users by login name.
func (c *Client) UsersByLogin(ctx context.Context, logins []string) ([]User, error) {
	if len(logins) > 100 {
		return nil, fmt.Errorf("users: %d logins, max 100", len(logins))
	}
	q := url.Values{}
	for _, l := range logins {
		q.Add("login", l)
	}
	var r struct {
		Data []User `json:"data"`
	}
	if err := c.get(ctx, PriorityHigh, "/users", q, &r); err != nil {
		return nil, err
	}
	return r.Data, nil
}

// Users looks up at most 100 users by ID. Suspended or deleted accounts are
// omitted from the response.
func (c *Client) Users(ctx context.Context, ids []string) ([]User, error) {
	if len(ids) > 100 {
		return nil, fmt.Errorf("users: %d ids, max 100", len(ids))
	}
	q := url.Values{}
	for _, id := range ids {
		q.Add("id", id)
	}
	var r struct {
		Data []User `json:"data"`
	}
	if err := c.get(ctx, PriorityLow, "/users", q, &r); err != nil {
		return nil, err
	}
	return r.Data, nil
}
