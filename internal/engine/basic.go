package engine

import (
	"context"
	"fmt"

	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/state"
)

// BlockBasicIfOverLimit removes the basic squad when basic_used_bytes has
// reached the configured limit. Called by the poller after it updates usage.
func (e *Engine) BlockBasicIfOverLimit(ctx context.Context, userUUID string) error {
	return e.withUserLock(userUUID, func() error {
		return e.store.Update(ctx, userUUID, e.cfg.BasicDefaultLimitBytes, func(st *state.UserState) error {
			if st.BasicState == state.BasicBlocked {
				return nil
			}
			if st.BasicLimitBytes <= 0 || st.BasicUsedBytes < st.BasicLimitBytes {
				return nil
			}

			panel, err := e.client.GetUser(ctx, userUUID)
			if err != nil || panel == nil {
				return fmt.Errorf("basic-block: load user: %w", err)
			}
			newSquads := dropSquads(remnawave.SquadsOf(panel), e.cfg.BasicSquadUUID)
			if err := e.callPatch(ctx, userUUID,
				statusPtr(remnawave.StatusActive), squadsPtr(newSquads), nil, nil,
			); err != nil {
				return fmt.Errorf("basic-block: patch: %w", err)
			}
			st.BasicState = state.BasicBlocked
			st.LastBasicLimitedAt = nullableInt64(nowUnix())
			e.log.Info("basic: blocked (basic squad removed)",
				"user", userUUID, "used", st.BasicUsedBytes, "limit", st.BasicLimitBytes)
			return nil
		})
	})
}

// SetBasicLimit sets (or overrides) the basic limit for a user.
func (e *Engine) SetBasicLimit(ctx context.Context, userUUID string, limitBytes int64) error {
	return e.store.Update(ctx, userUUID, limitBytes, func(st *state.UserState) error {
		st.BasicLimitBytes = limitBytes
		return nil
	})
}
