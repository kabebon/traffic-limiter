package subproxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/state"
)

// Resolver maps shortUuid → userUuid, with an in-process TTL cache so we don't
// hit /api/sub/{short}/info on every subscription pull.
//
// We do not persist the cache: it warms up within minutes of normal operation
// (clients pull subscriptions frequently) and is just an optimization — a cold
// cache miss costs one extra panel call.
type Resolver struct {
	client *remnawave.Client
	store  *state.Store
	log    *slog.Logger
	ttl    time.Duration

	mu     sync.RWMutex
	byShort map[string]cacheEntry
}

type cacheEntry struct {
	userUUID string
	expires  time.Time
}

// NewResolver builds a resolver. ttl controls how long a short→user mapping is
// considered fresh; 0 disables caching (every lookup hits the panel).
func NewResolver(client *remnawave.Client, store *state.Store, log *slog.Logger, ttl time.Duration) *Resolver {
	return &Resolver{
		client:  client,
		store:   store,
		log:     log,
		ttl:     ttl,
		byShort: make(map[string]cacheEntry),
	}
}

// Resolve returns the userUuid for a shortUuid. ok=false means the panel did
// not have the subscription (404) or the request failed; the caller falls back
// to a safe default title.
func (r *Resolver) Resolve(ctx context.Context, short string) (string, bool) {
	if short == "" {
		return "", false
	}
	if uuid, ok := r.cacheGet(short); ok {
		return uuid, true
	}

	uuid, ok := r.fetchFromPanel(ctx, short)
	if !ok {
		return "", false
	}
	r.cachePut(short, uuid)
	return uuid, true
}

func (r *Resolver) fetchFromPanel(ctx context.Context, short string) (string, bool) {
	// /api/sub/{short}/info returns { response: { isFound, user: {...} } }
	// where `user` contains the full user object including `uuid`. Field shape
	// varies a bit across panel versions, so we probe several paths.
	body, err := r.client.RawGet(ctx, "/api/sub/"+short+"/info")
	if err != nil {
		r.log.Debug("resolver: info fetch failed", "short", short, "err", err)
		return "", false
	}

	var env struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(body, &env); err != nil || len(env.Response) == 0 {
		return "", false
	}

	// Probe known shapes for the user uuid.
	uuid := probeUserUUID(env.Response)
	if uuid == "" {
		return "", false
	}
	return uuid, true
}

// probeUserUUID tries multiple JSON paths for the user UUID, covering several
// panel versions. Returns "" if none matched.
func probeUserUUID(responseRaw json.RawMessage) string {
	// Shape A: { isFound, user: { uuid } }
	var shapeA struct {
		IsFound bool `json:"isFound"`
		User    struct {
			UUID string `json:"uuid"`
		} `json:"user"`
	}
	if _ = json.Unmarshal(responseRaw, &shapeA); shapeA.IsFound && shapeA.User.UUID != "" {
		return shapeA.User.UUID
	}

	// Shape B: { user: "uuid-string" }
	var shapeB struct {
		User string `json:"user"`
	}
	if _ = json.Unmarshal(responseRaw, &shapeB); shapeB.User != "" {
		return shapeB.User
	}

	// Shape C: top-level uuid in the response object.
	var shapeC struct {
		UUID     string `json:"uuid"`
		UserUUID string `json:"userUuid"`
	}
	if _ = json.Unmarshal(responseRaw, &shapeC); shapeC.UUID != "" {
		return shapeC.UUID
	} else if shapeC.UserUUID != "" {
		return shapeC.UserUUID
	}
	return ""
}

func (r *Resolver) cacheGet(short string) (string, bool) {
	if r.ttl == 0 {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.byShort[short]
	if !ok || time.Now().After(e.expires) {
		return "", false
	}
	return e.userUUID, true
}

func (r *Resolver) cachePut(short, userUUID string) {
	if r.ttl == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Bound the cache size to avoid unbounded growth under abuse; drop a random
	// entry when very large (simple eviction, fine for a few thousand users).
	if len(r.byShort) > 100_000 {
		for k := range r.byShort {
			delete(r.byShort, k)
			break
		}
	}
	r.byShort[short] = cacheEntry{
		userUUID: userUUID,
		expires:  time.Now().Add(r.ttl),
	}
}

// keep net/http referenced (used by callers expecting an http.Client down the line)
var _ = http.StatusOK
