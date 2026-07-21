// Package remnawave is a thin HTTP client for the subset of the Remnawave API
// that traffic-limiter needs. It intentionally avoids third-party SDK
// dependencies so it can be tuned to the exact panel version in production.
package remnawave

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to the Remnawave panel.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New constructs a client. baseURL must not have a trailing slash.
func New(baseURL, token string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: timeout},
	}
}

// do performs an authenticated request and decodes JSON into out (if non-nil).
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	// Panel accepts either Bearer or X-Api-Key; bedolaga-bot sets both. We do
	// the same for compatibility across auth modes (api_key / caddy / basic).
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Api-Key", c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return &APIError{Status: resp.StatusCode, Path: path, Body: string(raw)}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode body (%s %s): %w", method, path, err)
		}
	}
	return nil
}

// APIError is returned for non-2xx responses.
type APIError struct {
	Status int
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("remnawave api %s -> %d: %s", e.Path, e.Status, e.Body)
}

// IsRetryable reports whether the caller should retry with backoff.
func (e *APIError) IsRetryable() bool {
	return e.Status == 0 || e.Status == http.StatusRequestTimeout ||
		e.Status == http.StatusTooManyRequests || e.Status >= 500
}
