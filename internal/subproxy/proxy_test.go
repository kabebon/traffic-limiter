package subproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/traffic-limiter/internal/config"
	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/state"
)

// startMockPanel simulates just enough of the Remnawave subscription endpoint:
//   GET /api/sub/{short}/info   → { response: { isFound: true, user: { uuid } } }
//   GET /api/sub/{short}        → plain-text body + a Profile-Title header
func startMockPanel(t *testing.T, userUUID string) *httptest.Server {
	t.Helper()
	return startMockPanelWithExpire(t, userUUID, 0)
}

// startMockPanelWithExpire is like startMockPanel but also sets the
// Subscription-Userinfo expire= field on subscription responses. expire=0 means
// "no expiry" (header omitted); a negative value means "in the past".
func startMockPanelWithExpire(t *testing.T, userUUID string, expire int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/sub/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/info") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w,
				`{"response":{"isFound":true,"user":{"uuid":%q}}}`, userUUID)
			return
		}
		// The subscription body itself.
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Profile-Title", "original-panel-title")
		if expire != 0 {
			w.Header().Set("Subscription-Userinfo",
				fmt.Sprintf("upload=0; download=100; total=0; expire=%d", expire))
		}
		_, _ = w.Write([]byte("# subscription body\nvless://should-not-appear\n"))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestProxy_RewritesProfileTitle(t *testing.T) {
	// Real SQLite-backed state store so we exercise the full lookup path.
	dir := t.TempDir()
	store, err := state.Open(dir + "/test.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	userUUID := "user-uuid-123"
	mockPanel := startMockPanel(t, userUUID)
	client := remnawave.New(mockPanel.URL, "test-token", 5*time.Second)

	// Seed the user as whitelist-blocked.
	err = store.Update(context.Background(), userUUID, 0, func(st *state.UserState) error {
		st.WLState = state.WLBlocked
		return nil
	})
	if err != nil {
		t.Fatalf("seed state: %v", err)
	}

	cfg := config.Config{
		PanelURL:        mockPanel.URL,
		WLTitleActive:   "ACTIVE",
		WLTitleBlocked:  "BLOCKED",
		SubproxyCacheTTL: 60 * time.Second,
	}
	p := New(cfg, client, store, nil)

	req := httptest.NewRequest(http.MethodGet, "/sub/shortabc", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	got := rec.Header().Get("Profile-Title")
	if got != "BLOCKED" {
		t.Fatalf("Profile-Title = %q, want %q", got, "BLOCKED")
	}
	if !strings.Contains(rec.Body.String(), "vless://") {
		t.Fatalf("subscription body not proxied: %q", rec.Body.String())
	}
}

func TestProxy_ActiveTitlePassesThroughPanelTitle(t *testing.T) {
	dir := t.TempDir()
	store, _ := state.Open(dir + "/test.sqlite")
	defer store.Close()

	userUUID := "u-active"
	mockPanel := startMockPanel(t, userUUID)
	client := remnawave.New(mockPanel.URL, "tok", 5*time.Second)

	// Default wl_state is active (no row = active). For a healthy user we must
	// NOT overlay our title — the panel's branded title passes through.
	cfg := config.Config{
		PanelURL:        mockPanel.URL,
		WLTitleActive:   "ACTIVE",
		WLTitleBlocked:  "BLOCKED",
		SubproxyCacheTTL: 60 * time.Second,
	}
	p := New(cfg, client, store, nil)

	req := httptest.NewRequest(http.MethodGet, "/sub/shortdef", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	// The mock panel sets Profile-Title: original-panel-title; we expect it
	// to be forwarded unchanged (no "ACTIVE" overlay).
	if got := rec.Header().Get("Profile-Title"); got != "original-panel-title" {
		t.Fatalf("Profile-Title = %q, want the panel's original title to pass through", got)
	}
}

func TestProxy_RejectsNonSubPath(t *testing.T) {
	mockPanel := startMockPanel(t, "u")
	client := remnawave.New(mockPanel.URL, "tok", 5*time.Second)
	p := New(config.Config{PanelURL: mockPanel.URL}, client, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for non-/sub/ path, got %d", rec.Code)
	}
}

// TestProxy_FailoverOnExpired verifies that a subscription whose expire=
// timestamp is in the past is served the single rescue server instead of the
// panel body, with the expired profile-title, and that the panel body is NOT
// returned.
func TestProxy_FailoverOnExpired(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir + "/test.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	userUUID := "u-expired"
	// expire far in the past.
	mockPanel := startMockPanelWithExpire(t, userUUID, time.Now().Add(-24*time.Hour).Unix())
	client := remnawave.New(mockPanel.URL, "tok", 5*time.Second)

	failover := "vless://rescue@example.com:443?encryption=none#RESCUE"
	cfg := config.Config{
		PanelURL:         mockPanel.URL,
		WLTitleActive:    "ACTIVE",
		WLTitleBlocked:   "BLOCKED",
		WLTitleExpired:   "EXPIRED-TITLE",
		FailoverConfig:   failover,
		SubproxyCacheTTL: 60 * time.Second,
	}
	p := New(cfg, client, store, nil)

	req := httptest.NewRequest(http.MethodGet, "/sub/shortexp", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Profile-Title"); got != "EXPIRED-TITLE" {
		t.Fatalf("Profile-Title = %q, want %q", got, "EXPIRED-TITLE")
	}
	body := rec.Body.String()
	if !strings.Contains(body, failover) {
		t.Fatalf("body should contain the failover server, got %q", body)
	}
	if strings.Contains(body, "should-not-appear") {
		t.Fatalf("body must not contain the panel subscription body, got %q", body)
	}
}

// TestProxy_NoFailoverOnActive verifies that an active subscription (no expire
// header) is NOT served the failover server — it must be proxied through to
// the panel.
func TestProxy_NoFailoverOnActive(t *testing.T) {
	dir := t.TempDir()
	store, _ := state.Open(dir + "/test.sqlite")
	defer store.Close()

	userUUID := "u-active2"
	// No expire header at all (active/unlimited).
	mockPanel := startMockPanel(t, userUUID)
	client := remnawave.New(mockPanel.URL, "tok", 5*time.Second)

	cfg := config.Config{
		PanelURL:         mockPanel.URL,
		WLTitleActive:    "ACTIVE",
		WLTitleBlocked:   "BLOCKED",
		WLTitleExpired:   "EXPIRED-TITLE",
		FailoverConfig:   "vless://rescue@example.com:443#RESCUE",
		SubproxyCacheTTL: 60 * time.Second,
	}
	p := New(cfg, client, store, nil)

	req := httptest.NewRequest(http.MethodGet, "/sub/shortact", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "should-not-appear") {
		t.Fatalf("active user should get the proxied panel body, got %q", body)
	}
	if strings.Contains(body, "rescue") {
		t.Fatalf("active user must NOT get the failover server, got %q", body)
	}
}

// TestProxy_NoFailoverOnFutureExpiry verifies that a subscription whose expire
// is in the future is NOT treated as expired.
func TestProxy_NoFailoverOnFutureExpiry(t *testing.T) {
	dir := t.TempDir()
	store, _ := state.Open(dir + "/test.sqlite")
	defer store.Close()

	userUUID := "u-future"
	mockPanel := startMockPanelWithExpire(t, userUUID, time.Now().Add(24*time.Hour).Unix())
	client := remnawave.New(mockPanel.URL, "tok", 5*time.Second)

	cfg := config.Config{
		PanelURL:         mockPanel.URL,
		WLTitleActive:    "ACTIVE",
		WLTitleBlocked:   "BLOCKED",
		WLTitleExpired:   "EXPIRED-TITLE",
		FailoverConfig:   "vless://rescue@example.com:443#RESCUE",
		SubproxyCacheTTL: 60 * time.Second,
	}
	p := New(cfg, client, store, nil)

	req := httptest.NewRequest(http.MethodGet, "/sub/shortfut", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "should-not-appear") {
		t.Fatalf("future-expiry user should get the proxied panel body, got %q", body)
	}
	if strings.Contains(body, "rescue") {
		t.Fatalf("future-expiry user must NOT get the failover server, got %q", body)
	}
}

// TestIsExpiredByHeader covers the expiry detection directly.
func TestIsExpiredByHeader(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name string
		h    http.Header
		want bool
	}{
		{"no header", http.Header{}, false},
		{"expire_zero_means_unlimited", mkUI(0), false},
		{"past", mkUI(now - 1), true},
		{"future", mkUI(now + 100000), false},
		{"missing_expire_field", mkHeaderNoExpire(), false},
		{"unparsable", mkUIUnparsable(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isExpiredByHeader(tc.h); got != tc.want {
				t.Fatalf("isExpiredByHeader = %v, want %v", got, tc.want)
			}
		})
	}
}

func mkUI(expire int64) http.Header {
	h := http.Header{}
	h.Set("Subscription-Userinfo", fmt.Sprintf("upload=0; download=100; total=0; expire=%d", expire))
	return h
}

func mkHeaderNoExpire() http.Header {
	h := http.Header{}
	h.Set("Subscription-Userinfo", "upload=0; download=100; total=0")
	return h
}

func mkUIUnparsable() http.Header {
	h := http.Header{}
	h.Set("Subscription-Userinfo", "upload=0; download=100; total=0; expire=abc")
	return h
}
