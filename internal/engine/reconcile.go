package engine

import (
	"context"
	"time"

	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/state"
)

// Reconcile is the safety-net loop. It walks users that should still be
// active/grace on the whitelist side and:
//   - ends grace if its window elapsed;
//   - if the panel says the user is LIMITED but we think they are active,
//     treats it like an arriving user.limited event (handles lost webhooks).
func (e *Engine) Reconcile(ctx context.Context) {
	now := nowUnix()
	err := e.store.IterNonBlockedWLUsers(ctx, func(st *state.UserState) error {
		// 1) Grace → blocked transition if window elapsed.
		if st.WLState == state.WLGrace && st.WLGraceUntil.Valid && now >= st.WLGraceUntil.Int64 {
			_ = e.withUserLock(st.UserUUID, func() error {
				return e.store.Update(ctx, st.UserUUID, e.cfg.BasicDefaultLimitBytes, func(s *state.UserState) error {
					if s.WLState != state.WLGrace {
						return nil
					}
					return e.blockWhitelist(ctx, s, nil, now)
				})
			})
			return nil
		}

		// 2) Lost-webhook recovery: panel says LIMITED, we think active/grace.
		panel, err := e.client.GetUser(ctx, st.UserUUID)
		if err != nil || panel == nil {
			return nil
		}
		if panel.Status == remnawave.StatusLimited && st.WLState == state.WLActive {
			e.log.Info("reconcile: detected lost user.limited", "user", st.UserUUID)
			return e.onUserLimited(ctx, st.UserUUID)
		}
		return nil
	})
	if err != nil {
		e.log.Warn("reconcile pass finished with error", "err", err)
	}
}

// RunReconciler ticks on cfg.ReconcileInterval until ctx is cancelled.
func (e *Engine) RunReconciler(ctx context.Context) {
	t := time.NewTicker(e.cfg.ReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Reconcile(ctx)
		}
	}
}
