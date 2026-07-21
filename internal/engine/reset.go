package engine

import (
	"context"
	"fmt"

	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/state"
)

// onUserReset is called when traffic is reset (auto or external). It:
//   - resets the used counter on the panel;
//   - restores the original whitelist limit/strategy;
//   - re-adds the whitelist and basic squads;
//   - zeroes the local basic counter;
//   - flips both states back to active.
func (e *Engine) onUserReset(ctx context.Context, userUUID string, fromExternal bool) error {
	return e.withUserLock(userUUID, func() error {
		return e.store.Update(ctx, userUUID, e.cfg.BasicDefaultLimitBytes, func(st *state.UserState) error {
			now := nowUnix()
			panel, err := e.client.GetUser(ctx, userUUID)
			if err != nil || panel == nil {
				return fmt.Errorf("reset: load user: %w", err)
			}

			// Restore original whitelist limit/strategy if we captured them.
			var limit *int64
			var strategy *remnawave.TrafficLimitStrategy
			if st.WLOriginalLimit.Valid {
				v := st.WLOriginalLimit.Int64
				limit = &v
			}
			if st.WLOriginalStrategy.Valid {
				s := remnawave.TrafficLimitStrategy(st.WLOriginalStrategy.String)
				strategy = &s
			}

			// Build the canonical squad set: both basic + whitelist.
			squads := ensureSquads(remnawave.SquadsOf(panel), e.cfg.BasicSquadUUID, e.cfg.WhitelistSquadUUID)

			if err := e.callPatch(ctx, userUUID,
				statusPtr(remnawave.StatusActive),
				squadsPtr(squads),
				limit, strategy,
			); err != nil {
				return fmt.Errorf("reset: patch: %w", err)
			}

			// Zero the panel's used counter for a clean start.
			if err := remnawave.RetryWithBackoff(ctx, 4, func() error {
				return e.client.ResetUserTraffic(ctx, userUUID)
			}); err != nil {
				// Non-fatal: the next reset window will pick it up; log and continue.
				e.log.Warn("reset: ResetUserTraffic failed", "user", userUUID, "err", err)
			}

			st.WLState = state.WLActive
			st.WLGraceUntil = state.NullInt64{}
			st.WLOverLimit = state.NullInt64{}
			st.BasicUsedBytes = 0
			st.BasicState = state.BasicActive
			st.LastReconciledAt = nullableInt64(now)
			e.log.Info("reset: user re-enabled (whitelist+basic restored)",
				"user", userUUID, "external", fromExternal)
			return nil
		})
	})
}

// ensureSquads returns the squad UUID list with the required UUIDs present
// (no duplicates). Order is preserved; new squads are appended.
func ensureSquads(current []string, want ...string) []string {
	have := make(map[string]bool, len(current))
	out := make([]string, 0, len(current)+len(want))
	for _, s := range current {
		have[s] = true
		out = append(out, s)
	}
	for _, w := range want {
		if w == "" || have[w] {
			continue
		}
		out = append(out, w)
		have[w] = true
	}
	return out
}
