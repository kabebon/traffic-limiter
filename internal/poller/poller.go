// Package poller periodically queries the panel for per-node per-user traffic
// on the "basic" nodes and feeds the deltas into the local basic counter.
package poller

import (
	"context"
	"log/slog"
	"time"

	"github.com/traffic-limiter/internal/config"
	"github.com/traffic-limiter/internal/engine"
	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/state"
)

// Poller runs on a fixed interval.
type Poller struct {
	cfg    config.Config
	client *remnawave.Client
	store  *state.Store
	engine *engine.Engine
	log    *slog.Logger
}

// New constructs a poller.
func New(cfg config.Config, client *remnawave.Client, store *state.Store, eng *engine.Engine, log *slog.Logger) *Poller {
	return &Poller{cfg: cfg, client: client, store: store, engine: eng, log: log}
}

// Run polls until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	t := time.NewTicker(p.cfg.BasicPollInterval)
	defer t.Stop()
	// First tick immediately so we don't wait a full interval at startup.
	p.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.tick(ctx)
		}
	}
}

// tick queries all basic nodes once.
func (p *Poller) tick(ctx context.Context) {
	// Query a wide window; the high-water mark in usage_checkpoint makes us
	// count only deltas, so the window length is mostly cosmetic.
	to := time.Now()
	from := to.Add(-24 * time.Hour)

	// Collect per-user deltas across all basic nodes.
	type acc struct{ delta int64 }
	totals := make(map[string]*acc)

	var usernameMap map[string]string

	for _, nodeUUID := range p.cfg.BasicNodeUUIDs {
		entries, err := p.client.NodeUsage(ctx, nodeUUID, from, to)
		if err != nil {
			p.log.Warn("node usage fetch failed", "node", nodeUUID, "err", err)
			continue
		}

		// Ensure we have a username->uuid map if we see non-UUIDs.
		for i, e := range entries {
			if len(e.UserUUID) != 36 {
				if usernameMap == nil {
					usernameMap = p.buildUsernameMap(ctx)
				}
				if uuid, ok := usernameMap[e.UserUUID]; ok && uuid != "" {
					entries[i].UserUUID = uuid
				}
			}
		}

		for _, e := range entries {
			delta, err := p.store.SetUsageCheckpoint(ctx, nodeUUID, e.UserUUID, e.Bytes)
			if err != nil {
				p.log.Warn("usage checkpoint failed", "node", nodeUUID, "user", e.UserUUID, "err", err)
				continue
			}
			if delta <= 0 {
				continue
			}
			a := totals[e.UserUUID]
			if a == nil {
				a = &acc{}
				totals[e.UserUUID] = a
			}
			a.delta += delta
		}
	}

	// Apply deltas and evaluate per-user basic limits.
	for userUUID, a := range totals {
		if err := p.store.AddBasicUsage(ctx, userUUID, a.delta); err != nil {
			p.log.Warn("add basic usage failed", "user", userUUID, "err", err)
			continue
		}
		if err := p.engine.BlockBasicIfOverLimit(ctx, userUUID); err != nil {
			p.log.Warn("basic-block evaluation failed", "user", userUUID, "err", err)
		}
	}
}

// buildUsernameMap fetches users from the panel and builds a username->uuid map.
func (p *Poller) buildUsernameMap(ctx context.Context) map[string]string {
	m := make(map[string]string)
	users, err := p.client.GetUsers(ctx)
	if err != nil {
		p.log.Warn("failed to fetch users for username mapping", "err", err)
		return m
	}
	for _, u := range users {
		if u.Username != "" {
			m[u.Username] = u.UUID
		}
	}
	return m
}
