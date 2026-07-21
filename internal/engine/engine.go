// Package engine implements the orchestration decisions: per-user locks,
// whitelist transitions (with grace), basic-squad transitions, and reconciliation.
package engine

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/traffic-limiter/internal/config"
	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/state"
)

// Engine wires together the Remnawave client, the state store, and per-user locks.
type Engine struct {
	cfg     config.Config
	client  *remnawave.Client
	store   *state.Store
	log     *slog.Logger

	mu    sync.Mutex
	locks map[string]*sync.Mutex // user UUID -> lock

	// relay, if non-nil, forwards events to the bedolaga bot webhook.
	relay interface {
		Enabled() bool
		Forward(ctx context.Context, payload []byte)
	}
}

// SetRelay wires an optional bot relay. May be called once at startup.
func (e *Engine) SetRelay(r interface {
	Enabled() bool
	Forward(ctx context.Context, payload []byte)
}) {
	e.relay = r
}

// New constructs an engine.
func New(cfg config.Config, client *remnawave.Client, store *state.Store, log *slog.Logger) *Engine {
	return &Engine{
		cfg:    cfg,
		client: client,
		store:  store,
		log:    log,
		locks:  make(map[string]*sync.Mutex),
	}
}

// lock returns the per-user mutex (lazily created).
func (e *Engine) lock(userUUID string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	m, ok := e.locks[userUUID]
	if !ok {
		m = &sync.Mutex{}
		e.locks[userUUID] = m
	}
	return m
}

// withUserLock runs fn while holding the per-user mutex.
func (e *Engine) withUserLock(userUUID string, fn func() error) error {
	m := e.lock(userUUID)
	m.Lock()
	defer m.Unlock()
	return fn()
}

// nowUnix is overridable in tests.
var nowUnix = func() int64 { return time.Now().Unix() }

// ptrOf helpers (avoid generic noise at call sites).
func statusPtr(s remnawave.UserStatus) *remnawave.UserStatus { return &s }
func int64Ptr(v int64) *int64                                { return &v }
func strategyPtr(s remnawave.TrafficLimitStrategy) *remnawave.TrafficLimitStrategy {
	return &s
}
func squadsPtr(s []string) *[]string { return &s }

var errNoUserUUID = errors.New("webhook event without user uuid")
