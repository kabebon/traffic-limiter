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
	mux := http.NewServeMux()

	mux.HandleFunc("/api/sub/", func(w http.ResponseWriter, r *http.Request) {
		// r.URL.Path is like /api/sub/{short} or /api/sub/{short}/info
		if strings.HasSuffix(r.URL.Path, "/info") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w,
				`{"response":{"isFound":true,"user":{"uuid":%q}}}`, userUUID)
			return
		}
		// The subscription body itself.
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Profile-Title", "original-panel-title")
		_, _ = w.Write([]byte("# subscription body\nvless://...\n"))
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

func TestProxy_ActiveTitleWhenWLActive(t *testing.T) {
	dir := t.TempDir()
	store, _ := state.Open(dir + "/test.sqlite")
	defer store.Close()

	userUUID := "u-active"
	mockPanel := startMockPanel(t, userUUID)
	client := remnawave.New(mockPanel.URL, "tok", 5*time.Second)

	// Default wl_state is active (no row = active).
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
	if got := rec.Header().Get("Profile-Title"); got != "ACTIVE" {
		t.Fatalf("Profile-Title = %q, want ACTIVE", got)
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
