package subproxy

import (
	"context"
	"encoding/json"
	"io"
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
//
// The cache also holds the panel-side user status (ACTIVE/LIMITED/EXPIRED/...)
// under a separate TTL. Status changes far less often than subscription pulls
// happen, and the failover branch needs it to tell "subscription expired by
// date" apart from "whitelist quota exhausted" (the latter keeps basic nodes).
type Resolver struct {
	client *remnawave.Client
	store  *state.Store
	log    *slog.Logger
	ttl    time.Duration

	mu      sync.RWMutex
	byShort map[string]cacheEntry
}

type cacheEntry struct {
	userUUID string
	expires  time.Time
	// status is the panel-side user status, refreshed independently of the
	// uuid mapping. statusExpires may be zero (never fetched); an empty status
	// means "unknown" (treated as not-expired by the caller).
	status        string
	statusExpires time.Time
}

// NewResolver builds a resolver. ttl controls how long a short→user mapping is
// considered fresh; 0 disables caching (every lookup hits the panel). A nil log
// is replaced with a discard logger so callers (notably tests) don't need to
// supply one.
func NewResolver(client *remnawave.Client, store *state.Store, log *slog.Logger, ttl time.Duration) *Resolver {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
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

// ResolveWithStatus returns the userUuid and its current panel status for a
// shortUuid. The uuid is cached like Resolve; the status is fetched via
// GetUser and cached under its own TTL so we don't hit the panel on every pull.
//
// ok=false means the panel did not have the subscription (404) or the request
// failed. When ok=true but status=="" the status could not be determined
// (panel error); callers must treat that as "not expired" (fail-safe).
func (r *Resolver) ResolveWithStatus(ctx context.Context, short string) (string, string, bool) {
	if short == "" {
		return "", "", false
	}

	// Fast path: uuid mapping fresh AND status fresh.
	if uuid, status, hit := r.cacheGetWithStatus(short); hit {
		return uuid, status, true
	}

	// Resolve the uuid first (may be a warm cache hit or a panel fetch).
	uuid, ok := r.cacheGet(short)
	if !ok {
		uuid, ok = r.fetchFromPanel(ctx, short)
		if !ok {
			return "", "", false
		}
		r.cachePut(short, uuid)
	}

	// Fetch status only when the cached one is stale.
	status := r.fetchStatus(ctx, uuid)
	r.cachePutStatus(short, status)
	return uuid, status, true
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

// fetchStatus loads the panel-side user status. Returns "" on any failure
// (caller treats unknown as not-expired).
func (r *Resolver) fetchStatus(ctx context.Context, userUUID string) string {
	panel, err := r.client.GetUser(ctx, userUUID)
	if err != nil || panel == nil {
		r.log.Debug("resolver: status fetch failed", "user", userUUID, "err", err)
		return ""
	}
	return string(panel.Status)
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

// cacheGetWithStatus returns the uuid and status if BOTH are still fresh.
// The third return is true only when a panel round-trip can be skipped
// entirely (uuid fresh AND status fresh).
func (r *Resolver) cacheGetWithStatus(short string) (string, string, bool) {
	if r.ttl == 0 {
		return "", "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.byShort[short]
	if !ok || time.Now().After(e.expires) {
		return "", "", false
	}
	if !e.statusExpires.IsZero() && time.Now().Before(e.statusExpires) {
		return e.userUUID, e.status, true
	}
	return "", "", false
}

func (r *Resolver) cachePut(short, userUUID string) {
	if r.ttl == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.byShort[short]
	e.userUUID = userUUID
	e.expires = time.Now().Add(r.ttl)
	r.byShort[short] = e
}

// cachePutStatus updates only the status of an existing entry (creating one is
// not useful without a uuid, so a missing entry is left alone).
func (r *Resolver) cachePutStatus(short, status string) {
	if r.ttl == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byShort[short]
	if !ok {
		return
	}
	e.status = status
	e.statusExpires = time.Now().Add(r.ttl)
	r.byShort[short] = e
}

// keep net/http referenced (used by callers expecting an http.Client down the line)
var _ = http.StatusOK
