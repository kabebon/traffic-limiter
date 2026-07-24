// Package subproxy is a reverse-proxy in front of the Remnawave subscription
// endpoint that rewrites the profile-title header per-user based on this
// orchestrator's whitelist state. This lets Happ / INCY / v2rayNG show a
// status line ("⚠️ whitelist exhausted, basic still works") in the app header
// instead of the static subscription title.
//
// Flow:
//
//	client GET /sub/{shortUuid}[...]
//	  → proxy fetches /api/sub/{shortUuid}[/...] from the panel (passthrough body)
//	  → proxy resolves shortUuid → userUuid (via /api/sub/{short}/info, cached)
//	  → proxy reads wl_state from SQLite
//	  → proxy overwrites the profile-title response header and forwards body
//
// The proxy is OPT-IN: it only mounts when SUBPROXY_ENABLED=true. Otherwise
// traffic-limiter runs without it (clients keep pointing at the panel directly).
package subproxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/traffic-limiter/internal/config"
	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/state"
)

// Proxy serves /sub/{shortUuid}[/...] and rewrites the profile-title header.
type Proxy struct {
	client    *remnawave.Client
	store     *state.Store
	resolver  *Resolver
	log       *slog.Logger
	titleOn   string // shown when wl_state == active
	titleOff  string // shown when wl_state == grace/blocked
	titleExp  string // shown when the subscription is expired by date
	panelBase string // base URL, e.g. https://panel.example.com
	http      *http.Client
	// failover is the single server link served to users whose subscription
	// is expired by date (panel status EXPIRED), so they can still reach the
	// cabinet to renew. Empty disables the failover branch.
	failover string
}

// New builds a proxy. titleOn/titleOff/titleExp are the profile-title strings.
func New(cfg config.Config, client *remnawave.Client, store *state.Store, log *slog.Logger) *Proxy {
	return &Proxy{
		client:    client,
		store:     store,
		resolver:  NewResolver(client, store, log, cfg.SubproxyCacheTTL),
		log:       log,
		titleOn:   cfg.WLTitleActive,
		titleOff:  cfg.WLTitleBlocked,
		titleExp:  cfg.WLTitleExpired,
		failover:  cfg.FailoverConfig,
		panelBase: strings.TrimRight(cfg.PanelURL, "/"),
		http:      &http.Client{Timeout: cfg.HTTPTimeout},
	}
}

// ServeHTTP implements http.Handler for /sub/ routes.
//
// Clients (Happ/INCY/...) point at the proxy with the same path shape they
// would use against the panel minus the /api prefix, e.g.:
//
//	https://proxy.example.com/sub/{shortUuid}
//	https://proxy.example.com/sub/{shortUuid}/{clientType}
//	https://proxy.example.com/sub/outline/{shortUuid}/ss/{tag}
//
// Internally the panel exposes all of these under /api/sub/..., so we prepend
// "/api" when forwarding upstream.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/sub/") {
		http.NotFound(w, r)
		return
	}

	// Failover branch: if the subscription is expired by date (panel status
	// EXPIRED), serve a single rescue server so the user can open the cabinet
	// and renew. We do NOT proxy the panel here — an expired subscription has
	// no usable nodes to serve. This is deliberately distinct from a whitelist
	// quota exhaustion (which keeps basic nodes and is handled below).
	if p.failover != "" {
		short := extractShortUUID(r.URL.Path)
		if _, status, ok := p.resolver.ResolveWithStatus(r.Context(), short); ok && isExpiredStatus(status) {
			p.serveFailover(w)
			return
		}
	}

	panelURL := p.panelBase + "/api" + r.URL.Path
	if r.URL.RawQuery != "" {
		panelURL += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, panelURL, nil)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Pass through client headers (User-Agent matters: panel may format the
	// response based on the client type).
	copyHeaders(req.Header, r.Header)
	// Strip hop-by-hop and auth headers we don't want to leak to the panel.
	sanitizeRequestHeaders(req.Header)
	// The panel accepts the same token our client uses.
	req.Header.Set("Authorization", "Bearer "+p.client.Token())
	req.Header.Set("X-Api-Key", p.client.Token())

	resp, err := p.http.Do(req)
	if err != nil {
		p.log.Warn("subproxy: upstream fetch failed", "url", panelURL, "err", err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	short := extractShortUUID(r.URL.Path)
	title := p.titleForShort(r.Context(), short)

	// Copy upstream response headers. We drop the panel's profile-title only
	// when we have our own status title to overlay; otherwise we forward it
	// untouched so the panel's branded title (e.g. "KabebaVPN") reaches the
	// client for healthy users.
	for k, vs := range resp.Header {
		if title != "" && strings.EqualFold(k, "profile-title") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	if title != "" {
		w.Header().Set("Profile-Title", percentEncode(title))
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// titleForShort resolves shortUuid → userUuid → wl_state and returns the
// status title to overlay on the panel's profile-title. Returns "" when the
// user is healthy, so the panel's own (branded) title passes through untouched
// — we only override the header when something is actually wrong (whitelist
// exhausted or subscription expired by date).
func (p *Proxy) titleForShort(ctx context.Context, short string) string {
	if short == "" {
		return ""
	}
	userUUID, status, ok := p.resolver.ResolveWithStatus(ctx, short)
	if !ok {
		// Unknown / new user — leave the panel title alone.
		return ""
	}
	if isExpiredStatus(status) {
		return p.titleExp
	}
	st, _ := p.store.Get(ctx, userUUID, 0)
	if st == nil {
		return ""
	}
	switch st.WLState {
	case state.WLGrace, state.WLBlocked:
		return p.titleOff
	default:
		return ""
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func sanitizeRequestHeaders(h http.Header) {
	// Drop hop-by-hop (RFC 7230 §6.1) and other headers we don't want to forward.
	for _, k := range []string{
		"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Te", "Trailers", "Transfer-Encoding", "Upgrade",
		"Host", "Content-Length",
	} {
		h.Del(k)
	}
}

// serveFailover responds with the rescue server link for an expired-by-date
// subscription. The body is a plain-text list (Happ/INCY/v2rayNG accept plain
// as well as base64), with a profile-title that tells the user to renew. We
// deliberately omit Subscription-Userinfo — the subscription is expired, there
// is no quota to report.
func (p *Proxy) serveFailover(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if p.titleExp != "" {
		w.Header().Set("Profile-Title", percentEncode(p.titleExp))
	}
	w.Header().Set("Profile-Update-Interval", "24")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, p.failover)
	if !strings.HasSuffix(p.failover, "\n") {
		_, _ = io.WriteString(w, "\n")
	}
}

// isExpiredStatus reports whether the panel-side status means the subscription
// is expired by date and should be served the failover server. An empty status
// (unknown / fetch error) returns false so we never fail-closed into rescue
// mode for a user who might still be active.
func isExpiredStatus(status string) bool {
	switch status {
	case string(remnawave.StatusExpired):
		return true
	default:
		return false
	}
}
