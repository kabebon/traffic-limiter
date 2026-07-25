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
//
// IMPORTANT: after a whitelist block, the panel holds our own Plan-B override
// limit (~1 EiB) so the panel itself doesn't re-enter LIMITED. We must NOT
// forward that value to the bot — it would show up in the cabinet as ~1 million
// GB. Instead, when the panel limit is a Plan-B override, we either omit
// trafficLimitBytes (no original captured) or send the original whitelist limit
// captured at block time (st.WLOriginalLimit), so the bot keeps a sane number.
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

	// Effective limit to report to the bot. If the panel currently shows our
	// Plan-B override, fall back to the original whitelist limit we captured at
	// block time; if neither is available, omit the field rather than leak the
	// inflated value.
	effectiveLimit := effectiveLimitForRelay(panel, e.storeLimit(ctx, userUUID))
	if effectiveLimit > 0 {
		data["trafficLimitBytes"] = effectiveLimit
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

// storeLimit returns the saved original whitelist limit (captured at block
// time), or 0 if there is no stored state for the user. Used to fall back to a
// sane limit value when the panel currently holds our Plan-B override.
func (e *Engine) storeLimit(ctx context.Context, userUUID string) int64 {
	st, _ := e.store.Get(ctx, userUUID, 0)
	if st == nil || !st.WLOriginalLimit.Valid {
		return 0
	}
	return st.WLOriginalLimit.Int64
}

// effectiveLimitForRelay decides which trafficLimitBytes value to report to the
// bot. If the panel currently shows our Plan-B override (~1 EiB), reporting it
// would make the cabinet display ~1 million GB; instead fall back to the
// original whitelist limit captured at block time. If neither is available,
// returns 0 (caller then omits the field).
func effectiveLimitForRelay(panel *remnawave.User, originalLimit int64) int64 {
	if panel.DataLimitBytes > 0 && !isPlanBLimit(panel.DataLimitBytes) {
		return panel.DataLimitBytes
	}
	// Panel holds the Plan-B override (or is 0); use the original if we have it.
	return originalLimit
}
