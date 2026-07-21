// Package config holds runtime configuration for the traffic-limiter orchestrator.
//
// All settings are read from environment variables. Sensible defaults are
// provided for everything that is not deployment specific.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the parsed runtime configuration.
type Config struct {
	// PanelURL is the base URL of the Remnawave panel, e.g. https://panel.example.com.
	PanelURL string
	// APIToken is the Bearer token used to call the Remnawave API.
	APIToken string

	// WebhookSecretHeaderName is the name of the header carrying the webhook signature
	// (kept for backward-compat; the panel always sends X-Remnawave-Signature).
	WebhookSecretHeaderName string
	// WebhookSecretValue is the HMAC-SHA256 shared secret used to verify the
	// inbound webhook from the panel (matches the panel's WEBHOOK_SECRET and
	// the bedolaga bot's REMNAWAVE_WEBHOOK_SECRET).
	WebhookSecretValue string

	// BotWebhookURL is the bedolaga bot's /remnawave-webhook endpoint, if we
	// should relay processed events onward. Empty = relay disabled (fully
	// standalone service).
	BotWebhookURL string
	// BotWebhookSecret is the HMAC secret used to sign relayed requests so the
	// bot's signature check passes. Must equal the bot's REMNAWAVE_WEBHOOK_SECRET.
	BotWebhookSecret string

	// BasicSquadUUID is the UUID of the Internal Squad that exposes the "basic" (untariffed) nodes.
	BasicSquadUUID string
	// WhitelistSquadUUID is the UUID of the Internal Squad that exposes the tariffed "whitelist" nodes.
	WhitelistSquadUUID string

	// BasicNodeUUIDs is the list of node UUIDs whose per-user traffic feeds the basic counter.
	BasicNodeUUIDs []string

	// WhitelistGraceWindow is how long a user stays in "grace" after hitting the whitelist limit.
	// If both grace window and over-limit are configured, whichever triggers first applies.
	WhitelistGraceWindow time.Duration
	// WhitelistGraceOverlimitMB is the additional bytes (above data_limit_bytes) allowed during grace.
	WhitelistGraceOverlimitMB int64

	// BasicPollInterval is how often the poller queries node statistics for the basic counter.
	BasicPollInterval time.Duration
	// BasicDefaultLimitBytes is the default basic limit applied to newly seen users.
	BasicDefaultLimitBytes int64

	// DBPath is the filesystem path to the SQLite database file.
	DBPath string
	// HTTPListen is the address the HTTP server binds to.
	HTTPListen string
	// AdminToken authorizes the external "repay" trigger endpoint.
	AdminToken string

	// StateAPIToken, if set, requires Bearer auth on GET /api/state/{uuid}.
	// Empty = open (rely on network isolation / reverse proxy).
	StateAPIToken string

	// LogLevel is one of: debug, info, warn, error.
	LogLevel string

	// ReconcileInterval is how often the reconciliation loop runs as a safety net.
	ReconcileInterval time.Duration

	// HTTPTimeout is the timeout for outbound calls to the Remnawave API.
	HTTPTimeout time.Duration
}

// FromEnv reads configuration from environment variables.
func FromEnv() (Config, error) {
	c := Config{
		PanelURL:                 strings.TrimRight(getenv("REMNAWAVE_PANEL_URL"), "/"),
		APIToken:                 getenv("REMNAWAVE_API_TOKEN"),
		WebhookSecretHeaderName:  getenvDefault("WEBHOOK_SECRET_HEADER_NAME", "X-Webhook-Secret"),
		WebhookSecretValue:       getenv("WEBHOOK_SECRET_VALUE"),
		BotWebhookURL:            strings.TrimRight(getenv("BOT_WEBHOOK_URL"), "/"),
		BotWebhookSecret:         getenv("BOT_WEBHOOK_SECRET"),
		BasicSquadUUID:           getenv("BASIC_SQUAD_UUID"),
		WhitelistSquadUUID:       getenv("WHITELIST_SQUAD_UUID"),
		BasicNodeUUIDs:           splitCSV(getenv("BASIC_NODE_UUIDS")),
		WhitelistGraceWindow:     getDurationDefault("WHITELIST_GRACE_WINDOW_SEC", 3600) * time.Second,
		WhitelistGraceOverlimitMB: getInt64Default("WHITELIST_GRACE_OVERLIMIT_MB", 50),
		BasicPollInterval:        getDurationDefault("BASIC_POLL_INTERVAL_SEC", 90) * time.Second,
		BasicDefaultLimitBytes:   getInt64Default("BASIC_DEFAULT_LIMIT_GB", 20) * 1024 * 1024 * 1024,
		DBPath:                   getenvDefault("DB_PATH", "./data/state.sqlite"),
		HTTPListen:               getenvDefault("HTTP_LISTEN", ":8080"),
		AdminToken:               getenv("ADMIN_TOKEN"),
		StateAPIToken:            getenv("STATE_API_TOKEN"),
		LogLevel:                 getenvDefault("LOG_LEVEL", "info"),
		ReconcileInterval:        getDurationDefault("RECONCILE_INTERVAL_SEC", 300) * time.Second,
		HTTPTimeout:              getDurationDefault("HTTP_TIMEOUT_SEC", 15) * time.Second,
	}

	var missing []string
	if c.PanelURL == "" {
		missing = append(missing, "REMNAWAVE_PANEL_URL")
	}
	if c.APIToken == "" {
		missing = append(missing, "REMNAWAVE_API_TOKEN")
	}
	if c.WebhookSecretValue == "" {
		missing = append(missing, "WEBHOOK_SECRET_VALUE")
	}
	if c.BasicSquadUUID == "" {
		missing = append(missing, "BASIC_SQUAD_UUID")
	}
	if c.WhitelistSquadUUID == "" {
		missing = append(missing, "WHITELIST_SQUAD_UUID")
	}
	if len(c.BasicNodeUUIDs) == 0 {
		missing = append(missing, "BASIC_NODE_UUIDS")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

func getenv(key string) string { return os.Getenv(key) }

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getInt64Default(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func getDurationDefault(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return time.Duration(n)
}

func splitCSV(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
