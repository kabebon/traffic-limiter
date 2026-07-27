package engine

import (
	"context"
	"fmt"

	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/state"
)

// onUserLimited is invoked when the panel reports that a user exhausted their
// data_limit_bytes. Because only whitelist nodes have coefficient=1, this means
// the *whitelist* quota is exhausted.
func (e *Engine) onUserLimited(ctx context.Context, userUUID string) error {
	return e.withUserLock(userUUID, func() error {
		return e.store.Update(ctx, userUUID, e.cfg.BasicDefaultLimitBytes, func(st *state.UserState) error {
			now := nowUnix()

			if st.WLState == state.WLActive {
				// Capture the panel-side paid limit/strategy before switching the
				// user to basic-only unlimited access.
				panel, _ := e.client.GetUser(ctx, userUUID)
				overLimit := int64(0)
				originalLimit := int64(0)
				originalStrategy := remnawave.NoReset
				if panel != nil {
					originalLimit = panel.DataLimitBytes
					originalStrategy = panel.TrafficLimitStrategy
					overLimit = originalLimit + e.cfg.WhitelistGraceOverlimitMB*1024*1024
				}
				st.WLOverLimit = nullableInt64(overLimit)
				if originalLimit > 0 {
					st.WLOriginalLimit = nullableInt64(originalLimit)
				}
				st.WLOriginalStrategy = nullableString(string(originalStrategy))
				st.LastWLLimitedAt = nullableInt64(now)
				return e.blockWhitelist(ctx, st, panel, now)
			}

			// Re-fires in grace/blocked are treated as repair attempts. This keeps
			// already-limited users moving to basic-only even if an earlier run
			// recorded state but failed to patch the panel.
			panel, _ := e.client.GetUser(ctx, userUUID)
			return e.blockWhitelist(ctx, st, panel, now)
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

// blockWhitelist removes the whitelist squad, sets trafficLimitBytes=0 with
// NO_RESET so the panel treats the remaining basic-only access as unlimited,
// and flips local state to blocked.
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

	// Build new squad list: drop whitelist and force basic in. The bot may
	// have created/renewed the user with only the paid whitelist squad.
	newSquads := ensureSquads(dropSquads(remnawave.SquadsOf(panel), e.cfg.WhitelistSquadUUID), e.cfg.BasicSquadUUID)

	unlimited := int64(0)
	if err := e.callPatch(ctx, userUUID,
		statusPtr(remnawave.StatusActive),
		squadsPtr(newSquads),
		int64Ptr(unlimited),
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
