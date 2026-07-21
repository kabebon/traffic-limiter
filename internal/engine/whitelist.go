package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/state"
)

// onUserLimited is invoked when the panel reports that a user exhausted their
// data_limit_bytes. Because only whitelist nodes have coefficient=1, this means
// the *whitelist* quota is exhausted.
func (e *Engine) onUserLimited(ctx context.Context, userUUID string) error {
	return e.withUserLock(userUUID, func() error {
		return e.store.Update(ctx, userUUID, e.cfg.BasicDefaultLimitBytes, func(st *state.UserState) error {
			if st.WLState == state.WLBlocked {
				// Already blocked; nothing to do (idempotency).
				return nil
			}
			now := nowUnix()

			if st.WLState == state.WLActive {
				// Enter grace. We capture the panel-side data_limit_bytes at this
				// moment so we know when "grace over-limit" is reached.
				panel, _ := e.client.GetUser(ctx, userUUID)
				overLimit := int64(0)
				originalLimit := int64(0)
				originalStrategy := remnawave.NoReset
				if panel != nil {
					originalLimit = panel.DataLimitBytes
					originalStrategy = panel.TrafficLimitStrategy
					overLimit = originalLimit + e.cfg.WhitelistGraceOverlimitMB*1024*1024
				}
				graceUntil := now + int64(e.cfg.WhitelistGraceWindow.Seconds())
				if graceUntil == now {
					// Window disabled → block immediately.
					return e.blockWhitelist(ctx, st, panel, now)
				}
				st.WLState = state.WLGrace
				st.WLGraceUntil = nullableInt64(graceUntil)
				st.WLOverLimit = nullableInt64(overLimit)
				if originalLimit > 0 {
					st.WLOriginalLimit = nullableInt64(originalLimit)
				}
				st.WLOriginalStrategy = nullableString(string(originalStrategy))
				st.LastWLLimitedAt = nullableInt64(now)
				e.log.Info("whitelist: entered grace",
					"user", userUUID, "grace_until", time.Unix(graceUntil, 0).Format(time.RFC3339),
					"over_limit_bytes", overLimit)
				return nil
			}

			// st.WLState == WLGrace — this is a re-fire during grace. Decide whether
			// grace is over (window elapsed OR over-limit exceeded) and block.
			if e.shouldEndGrace(st, now) {
				panel, _ := e.client.GetUser(ctx, userUUID)
				return e.blockWhitelist(ctx, st, panel, now)
			}
			return nil
		})
	})
}

// shouldEndGrace reports whether grace has elapsed (time window or over-limit).
func (e *Engine) shouldEndGrace(st *state.UserState, now int64) bool {
	if st.WLGraceUntil.Valid && now >= st.WLGraceUntil.Int64 {
		return true
	}
	// Over-limit is checked by the poller using current panel used bytes; we
	// cannot evaluate it here without a panel round-trip, so we rely on time
	// window plus the poller's dedicated over-limit check.
	return false
}

// blockWhitelist removes the whitelist squad, sets a very large data_limit with
// NO_RESET (Plan B) so the panel does not re-enter LIMITED on the next stat
// tick, and flips local state to blocked.
func (e *Engine) blockWhitelist(ctx context.Context, st *state.UserState, panel *remnawave.User, now int64) error {
	userUUID := st.UserUUID
	if panel == nil {
		p, err := e.client.GetUser(ctx, userUUID)
		if err != nil || p == nil {
			return fmt.Errorf("blockWhitelist: load user: %w", err)
		}
		panel = p
	}

	// Preserve originals if we didn't capture them at grace entry.
	if !st.WLOriginalLimit.Valid && panel.DataLimitBytes > 0 {
		st.WLOriginalLimit = nullableInt64(panel.DataLimitBytes)
	}
	if !st.WLOriginalStrategy.Valid {
		st.WLOriginalStrategy = nullableString(string(panel.TrafficLimitStrategy))
	}

	// Build new squad list: drop whitelist, keep everything else (incl. basic).
	newSquads := dropSquads(remnawave.SquadsOf(panel), e.cfg.WhitelistSquadUUID)

	huge := int64(1) << 50 // ~1 EiB — effectively unlimited, prevents auto-LIMITED
	if err := e.callPatch(ctx, userUUID,
		statusPtr(remnawave.StatusActive),
		squadsPtr(newSquads),
		int64Ptr(huge),
		strategyPtr(remnawave.NoReset),
	); err != nil {
		return fmt.Errorf("blockWhitelist: patch: %w", err)
	}

	st.WLState = state.WLBlocked
	st.WLGraceUntil = state.NullInt64{}
	st.LastWLLimitedAt = nullableInt64(now)
	e.log.Info("whitelist: blocked (whitelist squad removed, basic still available)",
		"user", userUUID)
	return nil
}

// dropSquads returns the squad UUID list minus the given UUID.
func dropSquads(in []string, drop string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == drop {
			continue
		}
		out = append(out, s)
	}
	return out
}

// callPatch wraps PatchUser with retry.
func (e *Engine) callPatch(ctx context.Context, userUUID string,
	status *remnawave.UserStatus, squads *[]string,
	dataLimit *int64, strategy *remnawave.TrafficLimitStrategy) error {
	return remnawave.RetryWithBackoff(ctx, 4, func() error {
		_, err := e.client.PatchUser(ctx, userUUID, status, squads, dataLimit, strategy)
		return err
	})
}

// nullableInt64 / nullableString wrap Go values into sql.Null* helpers.
func nullableInt64(v int64) state.NullInt64        { return state.NullInt64{Int64: v, Valid: true} }
func nullableString(v string) state.NullString      { return state.NullString{String: v, Valid: true} }
