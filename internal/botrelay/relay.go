// Package botrelay forwards processed Remnawave events to the bedolaga bot's
// /remnawave-webhook endpoint, so the bot keeps its subscription state and user
// notifications consistent with what the panel + this orchestrator decide.
//
// The relay is OPT-IN: if BOT_WEBHOOK_URL is empty, no forwarding happens and
// the orchestrator is fully standalone. When enabled, requests are signed with
// HMAC-SHA256 using BOT_WEBHOOK_SECRET (must equal the bot's
// REMNAWAVE_WEBHOOK_SECRET) in the X-Remnawave-Signature header — exactly what
// the bot's webserver/remnawave_webhook.py verifies.
//
// We never touch the bot's code: this package only calls its existing webhook
// endpoint over HTTP, so the bot remains free to be updated independently.
package botrelay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Relay sends events to the bedolaga bot webhook.
type Relay struct {
	url     string
	secret  string
	http    *http.Client
	log     *slog.Logger
	enabled bool
}

// New builds a relay. url == "" → disabled; Forward is a no-op.
func New(url, secret string, timeout time.Duration, log *slog.Logger) *Relay {
	return &Relay{
		url:     url,
		secret:  secret,
		http:    &http.Client{Timeout: timeout},
		log:     log,
		enabled: url != "",
	}
}

// Enabled reports whether forwarding is on.
func (r *Relay) Enabled() bool { return r != nil && r.enabled }

// Forward sends a pre-built payload to the bot, signed with HMAC-SHA256.
// payload must already be the exact bytes you want delivered (the signature is
// computed over those bytes). It is retried once on transient errors.
func (r *Relay) Forward(ctx context.Context, payload []byte) {
	if !r.Enabled() {
		return
	}
	r.forwardWithRetry(ctx, payload, 2)
}

// ForwardEvent marshals an arbitrary event object and forwards it.
func (r *Relay) ForwardEvent(ctx context.Context, event any) {
	if !r.Enabled() {
		return
	}
	payload, err := json.Marshal(event)
	if err != nil {
		r.log.Warn("botrelay: marshal failed", "err", err)
		return
	}
	r.forwardWithRetry(ctx, payload, 2)
}

func (r *Relay) forwardWithRetry(ctx context.Context, payload []byte, attempts int) {
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return
		}
		err := r.doForward(ctx, payload)
		if err == nil {
			return
		}
		lastErr = err
		// Brief backoff before retry; keep it short so we don't pile up goroutines.
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
	r.log.Warn("botrelay: forward failed", "url", r.url, "err", lastErr, "attempts", attempts)
}

func (r *Relay) doForward(ctx context.Context, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "traffic-limiter/1.0")
	if r.secret != "" {
		mac := hmac.New(sha256.New, []byte(r.secret))
		mac.Write(payload)
		req.Header.Set("X-Remnawave-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return relayError{Status: resp.StatusCode, URL: r.url}
	}
	return nil
}

type relayError struct{ Status int; URL string }

func (e relayError) Error() string { return "botrelay: non-2xx from bot: " + e.URL }
