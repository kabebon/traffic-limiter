// Package webhook implements the inbound HTTP receiver for Remnawave webhooks.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"github.com/traffic-limiter/internal/remnawave"
)

// EventHandler dispatches a single event to the engine.
type EventHandler interface {
	Handle(ctx context.Context, event Event) error
}

// Event is the normalized webhook event.
type Event struct {
	Type     string          // e.g. "user.limited", "user.traffic_reset"
	UserUUID string
	Raw      json.RawMessage
}

// Handler serves POST /webhook.
type Handler struct {
	// secret is the shared secret used by the panel to HMAC-sign the body
	// (matches the panel's WEBHOOK_SECRET and the bot's REMNAWAVE_WEBHOOK_SECRET).
	secret     string
	dispatcher EventHandler
}

// NewHandler builds a handler. The signature is verified using HMAC-SHA256
// against the X-Remnawave-Signature header (this is the header the panel sets
// and the bedolaga bot verifies against).
func NewHandler(secret string, dispatcher EventHandler) *Handler {
	return &Handler{secret: secret, dispatcher: dispatcher}
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	// Verify HMAC-SHA256 signature unless no secret is configured (dev mode).
	if h.secret != "" {
		sig := r.Header.Get("X-Remnawave-Signature")
		if sig == "" {
			http.Error(w, "missing signature", http.StatusUnauthorized)
			return
		}
		if !verifySignature(body, sig, h.secret) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	// Remnawave payload: {"event":"user.limited","data":{"uuid":"..."}}
	// (some versions also send scope/userUuid at top level).
	var env struct {
		Event string          `json:"event"`
		Scope string          `json:"scope"`
		UUID  string          `json:"uuid"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "decode body", http.StatusBadRequest)
		return
	}

	uuid := env.UUID
	if uuid == "" && len(env.Data) > 0 {
		var d struct {
			UUID     string `json:"uuid"`
			UserUUID string `json:"userUuid"`
			User     struct {
				UUID string `json:"uuid"`
			} `json:"user"`
		}
		_ = json.Unmarshal(env.Data, &d)
		uuid = firstNonEmpty(d.UUID, d.UserUUID, d.User.UUID)
	}

	eventType := env.Event
	if eventType == "" {
		http.Error(w, "missing event type", http.StatusBadRequest)
		return
	}

	// Keep the remnawave.WebhookEnvelope alias referenced for callers that
	// need a typed view of the same payload.
	_ = remnawave.WebhookEnvelope{}

	// Acknowledge immediately so the panel does not retry; process asynchronously.
	w.WriteHeader(http.StatusOK)

	evt := Event{Type: eventType, UserUUID: uuid, Raw: body}
	go func() {
		// Detached context: webhook retries only fire on connection failure,
		// so we must complete even if the original request is gone.
		ctx := context.Background()
		_ = h.dispatcher.Handle(ctx, evt)
	}()
}

// verifySignature computes HMAC-SHA256(secret, body) and compares it
// against received (a lowercase hex digest) in constant time.
func verifySignature(body []byte, received, secret string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(received))
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
