package engine

import (
	"context"
	"fmt"

	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/state"
	"github.com/traffic-limiter/internal/webhook"
)

// onUserModified reacts to panel-side changes of the user (most importantly:
// the bedolaga bot's "докупка трафика" button, which PATCHes trafficLimitBytes
// up and triggers a user.modified webhook — but NOT a user.traffic_reset one).
//
// It must NOT unblock whitelist just because the panel reports some limit —
// our own Plan-B override set a huge trafficLimitBytes (so the panel wouldn't
// re-enter LIMITED), and that would match "used < limit" trivially. The
// correct signal is one of:
//
//   1. The user's CURRENT usedTrafficBytes is below their ORIGINAL (pre-block)
//      limit — meaning the bot reset their traffic or the cycle rolled over.
//   2. The user's trafficLimitBytes is now ABOVE the original limit — meaning
//      the bot added extra quota (докупка).
//
// We always re-read the panel user to avoid acting on a stale webhook payload
// (the panel may have fired user.modified several times in quick succession).
func (e *Engine) onUserModified(ctx context.Context, userUUID string, d webhook.EventData) error {
	// Fast path: nothing useful in the payload → ignore (avoids a panel fetch
	// on every incidental user.modified, e.g. admin edits username).
	if d.TrafficLimitBytes == nil && d.UsedTrafficBytes == nil && d.Status == nil {
		return nil
	}

	return e.withUserLock(userUUID, func() error {
		return e.store.Update(ctx, userUUID, e.cfg.BasicDefaultLimitBytes, func(st *state.UserState) error {
			// Only relevant if the whitelist is currently cut off.
			if st.WLState != state.WLBlocked && st.WLState != state.WLGrace {
				return nil
			}

			panel, err := e.client.GetUser(ctx, userUUID)
			if err != nil || panel == nil {
				return fmt.Errorf("user.modified: load user: %w", err)
			}

			if shouldRestoreWhitelist(st, panel) {
				return e.restoreWhitelist(ctx, st, panel)
			}
			return nil
		})
	})
}

// shouldRestoreWhitelist decides whether to bring the whitelist squad back.
//
// `originalLimit` is the limit we captured when the whitelist first entered
// grace/block; it is the authoritative "how much the user is paying for".
// The panel's current state is `panel`.
func shouldRestoreWhitelist(st *state.UserState, panel *remnawave.User) bool {
	originalLimit := int64(0)
	if st.WLOriginalLimit.Valid {
		originalLimit = st.WLOriginalLimit.Int64
	}

	currentLimit := panel.DataLimitBytes
	currentUsed := panel.UsedBytes

	// Signal 1: limit grew beyond the original — user bought more traffic.
	// (Plan-B override sets a ~1 EiB limit; this comparison must use the
	// ORIGINAL limit, not the inflated one.)
	if originalLimit > 0 && currentLimit > originalLimit && currentLimit < planBLimitCeiling {
		return true
	}

	// Signal 2: used dropped below the original limit — either a manual reset
	// (bot's "сброс трафика") or the strategy cycle rolled over. In both cases
	// the user has usable whitelist quota again.
	//
	// We additionally require the panel's CURRENT limit not to be our own
	// Plan-B override (>= ceiling). Otherwise we'd act on a transient state
	// where the panel still holds the inflated limit we set at block time and
	// the "used < original" comparison is just an artifact. Real resets also
	// fire user.traffic_reset, which is handled separately — this signal is a
	// fallback for cases where only user.modified arrives.
	if originalLimit > 0 && currentUsed < originalLimit && !isPlanBLimit(currentLimit) {
		return true
	}

	// Signal 3: limit returned to a sane value below the Plan-B ceiling —
	// admin manually restored the original limit.
	if currentLimit > 0 && currentLimit < planBLimitCeiling && originalLimit > 0 && currentLimit >= originalLimit {
		// Limit is at-or-above the original and used is under it.
		if currentUsed < currentLimit {
			return true
		}
	}

	return false
}

// restoreWhitelist puts the whitelist squad back and undoes the Plan-B override
// (restoring the original limit + strategy captured at block time). Unlike the
// full reset path, this does NOT call reset-traffic — the panel already has
// the right usedTrafficBytes (the whole point of докупка is to keep history).
func (e *Engine) restoreWhitelist(ctx context.Context, st *state.UserState, panel *remnawave.User) error {
	userUUID := st.UserUUID

	// Build the canonical squad set: both basic + whitelist, regardless of what
	// the panel currently shows (it might still have only basic).
	squads := ensureSquads(remnawave.SquadsOf(panel), e.cfg.BasicSquadUUID, e.cfg.WhitelistSquadUUID)

	// Restore original limit/strategy if we captured them; otherwise keep
	// whatever the panel currently has (the докупка already updated it).
	var limit *int64
	var strategy *remnawave.TrafficLimitStrategy
	if st.WLOriginalLimit.Valid {
		// If the panel currently shows a HIGHER limit (докупка added quota),
		// honor the new one — do not downgrade the user.
		newLimit := st.WLOriginalLimit.Int64
		if panel.DataLimitBytes > newLimit && panel.DataLimitBytes < planBLimitCeiling {
			newLimit = panel.DataLimitBytes
		}
		v := newLimit
		limit = &v
	}
	if st.WLOriginalStrategy.Valid {
		s := remnawave.TrafficLimitStrategy(st.WLOriginalStrategy.String)
		strategy = &s
	}

	if err := e.callPatch(ctx, userUUID,
		statusPtr(remnawave.StatusActive),
		squadsPtr(squads),
		limit, strategy,
	); err != nil {
		return fmt.Errorf("user.modified restore: patch: %w", err)
	}

	st.WLState = state.WLActive
	st.WLGraceUntil = state.NullInt64{}
	st.WLOverLimit = state.NullInt64{}
	e.log.Info("whitelist: restored on user.modified (traffic bought or reset)",
		"user", userUUID, "limit", limit, "used", panel.UsedBytes)
	return nil
}

// planBLimitCeiling is the threshold above which we assume a trafficLimitBytes
// value is our own Plan-B override rather than a real user-facing limit.
// Plan-B sets ~1 EiB (1<<60); any real limit is many orders of magnitude lower.
const planBLimitCeiling int64 = 1 << 50

// isPlanBLimit reports whether v looks like our Plan-B override rather than a
// real user-facing quota.
func isPlanBLimit(v int64) bool { return v >= planBLimitCeiling }
