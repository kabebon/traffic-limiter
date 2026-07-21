// Command orchestrator is the traffic-limiter service.
//
// It receives Remnawave webhooks, polls node usage, and drives user squad
// membership so that the whitelist quota only tariffies "whitelist" nodes,
// while a separate basic quota is enforced by this service itself.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/traffic-limiter/internal/botrelay"
	"github.com/traffic-limiter/internal/config"
	"github.com/traffic-limiter/internal/engine"
	"github.com/traffic-limiter/internal/poller"
	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/state"
	"github.com/traffic-limiter/internal/webhook"
)

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(2)
	}

	log := newLogger(cfg.LogLevel)

	// Ensure the data directory exists for SQLite.
	if idx := strings.LastIndexByte(cfg.DBPath, '/'); idx >= 0 {
		_ = os.MkdirAll(cfg.DBPath[:idx], 0o755)
	} else if idx := strings.LastIndexByte(cfg.DBPath, '\\'); idx >= 0 {
		_ = os.MkdirAll(cfg.DBPath[:idx], 0o755)
	}

	store, err := state.Open(cfg.DBPath)
	if err != nil {
		log.Error("open state", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	client := remnawave.New(cfg.PanelURL, cfg.APIToken, cfg.HTTPTimeout)
	eng := engine.New(cfg, client, store, log)

	rootCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Background workers.
	go eng.RunReconciler(rootCtx)
	go poller.New(cfg, client, store, eng, log).Run(rootCtx)

	mux := http.NewServeMux()
	mux.Handle("/webhook", webhook.NewHandler(cfg.WebhookSecretValue, eng))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Optional: read-only state endpoint for ops / future minimal bot integration.
	stateHandler := newStateHandler(store, eng, client, cfg, log)
	mux.HandleFunc("/api/state/", stateHandler)

	// Optional: admin "user paid" trigger to re-enable squads + reset counters.
	if cfg.AdminToken != "" {
		mux.HandleFunc("/admin/repay/", adminRepayHandler(eng, cfg.AdminToken, log))
	}

	// Optional: forward processed events to the bedolaga bot's webhook endpoint.
	// Disabled unless BOT_WEBHOOK_URL is set. Empty secret = unsigned (the bot
	// will reject it, so set BOT_WEBHOOK_SECRET too when enabling).
	var relay *botrelay.Relay
	if cfg.BotWebhookURL != "" {
		relay = botrelay.New(cfg.BotWebhookURL, cfg.BotWebhookSecret, cfg.HTTPTimeout, log)
		eng.SetRelay(relay)
		log.Info("bot relay enabled", "url", cfg.BotWebhookURL)
	}

	srv := &http.Server{
		Addr:              cfg.HTTPListen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-rootCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("orchestrator listening", "addr", cfg.HTTPListen,
		"basic_squad", cfg.BasicSquadUUID, "whitelist_squad", cfg.WhitelistSquadUUID,
		"basic_nodes", len(cfg.BasicNodeUUIDs))

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http server", "err", err)
		os.Exit(1)
	}
	log.Info("shutdown complete")
}

// adminRepayHandler exposes POST /admin/repay/{userUuid} to re-enable a user
// from an external billing system. Authenticated via AdminToken.
func adminRepayHandler(eng *engine.Engine, adminToken string, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if adminToken == "" {
			http.Error(w, "admin token not configured", http.StatusServiceUnavailable)
			return
		}
		got := r.Header.Get("Authorization")
		if !strings.HasPrefix(got, "Bearer ") || strings.TrimPrefix(got, "Bearer ") != adminToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		userUUID := strings.TrimPrefix(r.URL.Path, "/admin/repay/")
		userUUID = strings.Trim(userUUID, "/")
		if userUUID == "" {
			http.Error(w, "missing user uuid", http.StatusBadRequest)
			return
		}
		if err := eng.Repay(r.Context(), userUUID); err != nil {
			log.Error("repay failed", "user", userUUID, "err", err)
			http.Error(w, "repay failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "user": userUUID})
	}
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}
