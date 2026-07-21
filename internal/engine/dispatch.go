package engine

import (
	"context"

	"github.com/traffic-limiter/internal/webhook"
)

// Handle dispatches an inbound webhook event.
//
// Processing order:
//  1. Apply our own decision (cut/restore squads).
//  2. If a bot relay is configured, forward a bot-safe view of the event so
//     the bedolaga bot stays in sync — but never let the bot see a raw
//     user.limited for a user that still has the basic squad available, since
//     the bot would otherwise flip the subscription to LIMITED (which means
//     "subscription exhausted" in the bot's UX).
func (e *Engine) Handle(ctx context.Context, evt webhook.Event) error {
	if evt.UserUUID == "" {
		return errNoUserUUID
	}
	log := e.log.With("event", evt.Type, "user", evt.UserUUID)

	switch evt.Type {
	case "user.limited":
		if err := e.onUserLimited(ctx, evt.UserUUID); err != nil {
			log.Error("user.limited handling failed", "err", err)
			return err
		}
		// Translate to user.modified(status=ACTIVE) so the bot does NOT mark the
		// subscription as exhausted — basic squad is still usable.
		e.relayUserModifiedActive(ctx, evt.UserUUID)

	case "user.traffic_reset", "user.data_used_reset":
		if err := e.onUserReset(ctx, evt.UserUUID, false); err != nil {
			log.Error("user reset handling failed", "err", err)
			return err
		}
		// Pass through to the bot as-is so it clears traffic_used_gb.
		e.relayRaw(ctx, evt)

	case "user.enabled":
		// No-op locally; forward to bot as-is.
		e.relayRaw(ctx, evt)
	case "user.disabled", "user.expired", "user.deleted":
		log.Info("ignoring lifecycle event", "type", evt.Type)
		// These are intentionally NOT forwarded: they reflect panel-level
		// lifecycle, and the bot already has its own handling for them via
		// whatever webhook source it normally uses. Forwarding here could
		// double-fire if the bot is also wired to the panel directly.
	default:
		log.Debug("unhandled event", "type", evt.Type)
	}
	return nil
}

// Repay is the external "user paid" trigger; mirrors an auto-reset and then
// notifies the bot.
func (e *Engine) Repay(ctx context.Context, userUUID string) error {
	if err := e.onUserReset(ctx, userUUID, true); err != nil {
		return err
	}
	// Tell the bot traffic was reset so it clears its local counters.
	e.relayRaw(ctx, webhook.Event{
		Type:     "user.traffic_reset",
		UserUUID: userUUID,
	})
	return nil
}
