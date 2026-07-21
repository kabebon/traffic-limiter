package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/traffic-limiter/internal/config"
	"github.com/traffic-limiter/internal/engine"
	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/state"
)

// newStateHandler exposes GET /api/state/{userUuid} returning the basic/whitelist
// counter view for a user, useful for ops dashboards or a future minimal bot
// integration (read-only — the bot can call this without us touching its code).
//
// Auth: optional Bearer token via STATE_API_TOKEN if configured; otherwise
// the endpoint is open (bind behind a private network / reverse proxy).
func newStateHandler(store *state.Store, eng *engine.Engine, client *remnawave.Client,
	cfg config.Config, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.StateAPIToken != "" {
			got := r.Header.Get("Authorization")
			if !strings.HasPrefix(got, "Bearer ") || strings.TrimPrefix(got, "Bearer ") != cfg.StateAPIToken {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		userUUID := strings.TrimPrefix(r.URL.Path, "/api/state/")
		userUUID = strings.Trim(userUUID, "/")
		if userUUID == "" {
			http.Error(w, "missing user uuid", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		resp := buildStateResponse(ctx, store, client, userUUID, cfg.BasicDefaultLimitBytes)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func buildStateResponse(ctx context.Context, store *state.Store, client *remnawave.Client,
	userUUID string, defaultBasicLimit int64) map[string]any {
	st, _ := store.Get(ctx, userUUID, defaultBasicLimit)

	resp := map[string]any{
		"user_uuid": userUUID,
		"basic": map[string]any{
			"used_bytes":  st.BasicUsedBytes,
			"limit_bytes": st.BasicLimitBytes,
			"state":       string(st.BasicState),
		},
		"whitelist": map[string]any{
			"state":           string(st.WLState),
			"original_limit":  st.WLOriginalLimit,
			"over_limit":      st.WLOverLimit,
			"grace_until":     st.WLGraceUntil,
			"last_limited_at": st.LastWLLimitedAt,
		},
	}

	// Enrich with panel-side current values (best effort).
	if panel, err := client.GetUser(ctx, userUUID); err == nil && panel != nil {
		resp["panel"] = map[string]any{
			"status":                 string(panel.Status),
			"active_internal_squads": remnawave.SquadsOf(panel),
			"traffic_limit_bytes":    panel.DataLimitBytes,
			"used_bytes":             panel.UsedBytes,
			"strategy":               string(panel.TrafficLimitStrategy),
		}
	}

	return resp
}
