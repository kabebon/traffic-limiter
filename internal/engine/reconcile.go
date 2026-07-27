package engine

import (
	"context"
	"time"

	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/state"
)

// Reconcile is the safety-net loop. It walks known users and:
//   - repairs grace/blocked users so the panel is basic-only ACTIVE;
//   - if the panel says the user is LIMITED but we think they are active,
//     treats it like an arriving user.limited event (handles lost webhooks).
func (e *Engine) Reconcile(ctx context.Context) {
	now := nowUnix()
	err := e.store.IterWLUsers(ctx, func(st *state.UserState) error {
		panel, err := e.client.GetUser(ctx, st.UserUUID)
		if err != nil || panel == nil {
			return nil
		}

		// 1) Grace/blocked repair: make sure the panel actually got the
		// basic-only ACTIVE patch. This repairs users stranded by an earlier
		// failed run or by the old grace behavior.
		if st.WLState == state.WLGrace || st.WLState == state.WLBlocked {
			if shouldRestoreWhitelist(st, panel) {
				return e.withUserLock(st.UserUUID, func() error {
					return e.store.Update(ctx, st.UserUUID, e.cfg.BasicDefaultLimitBytes, func(s *state.UserState) error {
						return e.restoreWhitelist(ctx, s, panel)
					})
				})
			}
			if panel.Status == remnawave.StatusLimited ||
				remnawave.HasSquad(panel, e.cfg.WhitelistSquadUUID) ||
				!remnawave.HasSquad(panel, e.cfg.BasicSquadUUID) ||
				panel.DataLimitBytes != 0 {
				return e.withUserLock(st.UserUUID, func() error {
					return e.store.Update(ctx, st.UserUUID, e.cfg.BasicDefaultLimitBytes, func(s *state.UserState) error {
						return e.blockWhitelist(ctx, s, panel, now)
					})
				})
			}
			return nil
		}

		// 2) Lost-webhook recovery: panel says LIMITED, we think active.
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
