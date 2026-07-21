package engine

import (
	"context"
	"encoding/json"

	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/webhook"
)

// relayRaw forwards the original event payload verbatim to the bot. If the
// relay isn't enabled, this is a no-op.
func (e *Engine) relayRaw(ctx context.Context, evt webhook.Event) {
	if e.relay == nil || !e.relay.Enabled() {
		return
	}
	if len(evt.Raw) > 0 {
		e.relay.Forward(ctx, evt.Raw)
		return
	}
	// No raw payload (e.g. synthesized repay event): build a minimal one.
	payload, _ := json.Marshal(map[string]any{
		"event": evt.Type,
		"data":  map[string]any{"uuid": evt.UserUUID},
	})
	e.relay.Forward(ctx, payload)
}

// relayUserModifiedActive builds a synthetic "user.modified" event with
// status=ACTIVE and the CURRENT panel traffic limit/used bytes, so the bot:
//   - clears any previously-applied LIMITED status (reactivates subscription);
//   - shows the user's traffic counters accurately (whitelist quota still
//     reflects whatever the panel currently knows).
//
// This is the single reason the orchestrator must talk to the bot at all:
// without it, the bot would treat a whitelist-only limit as a full
// subscription exhaustion.
func (e *Engine) relayUserModifiedActive(ctx context.Context, userUUID string) {
	if e.relay == nil || !e.relay.Enabled() {
		return
	}
	panel, err := e.client.GetUser(ctx, userUUID)
	if err != nil || panel == nil {
		e.log.Warn("relayUserModifiedActive: cannot load panel user; forwarding minimal event",
			"user", userUUID, "err", err)
		// Best effort: send a minimal modified event with status ACTIVE so the
		// bot at least doesn't keep the subscription in LIMITED.
		payload, _ := json.Marshal(map[string]any{
			"event": "user.modified",
			"data": map[string]any{
				"uuid":   userUUID,
				"status": "ACTIVE",
			},
		})
		e.relay.Forward(ctx, payload)
		return
	}

	data := map[string]any{
		"uuid":                 userUUID,
		"status":               string(panel.Status),
		"activeInternalSquads": remnawave.SquadsOf(panel),
	}
	if panel.DataLimitBytes > 0 {
		data["trafficLimitBytes"] = panel.DataLimitBytes
	}
	if panel.UsedBytes > 0 {
		data["usedTrafficBytes"] = panel.UsedBytes
	}
	payload, _ := json.Marshal(map[string]any{
		"event": "user.modified",
		"data":  data,
	})
	e.relay.Forward(ctx, payload)
}
