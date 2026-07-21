package remnawave

import "encoding/json"

// UserStatus mirrors the Remnawave user status enum.
type UserStatus string

const (
	StatusActive   UserStatus = "ACTIVE"
	StatusDisabled UserStatus = "DISABLED"
	StatusLimited  UserStatus = "LIMITED"
	StatusExpired  UserStatus = "EXPIRED"
)

// TrafficLimitStrategy mirrors the Remnawave reset strategy enum.
type TrafficLimitStrategy string

const (
	NoReset       TrafficLimitStrategy = "NO_RESET"
	Day           TrafficLimitStrategy = "DAY"
	Week          TrafficLimitStrategy = "WEEK"
	Month         TrafficLimitStrategy = "MONTH"
	MonthRolling  TrafficLimitStrategy = "MONTH_ROLLING"
)

// SquadUUID is a string UUID identifying an internal squad (panel's
// activeInternalSquads is a plain string array).
type SquadUUID string

// User is the subset of user fields we read/write.
type User struct {
	UUID                 string               `json:"uuid"`
	Username             string               `json:"username,omitempty"`
	Status               UserStatus           `json:"status"`
	ActiveInternalSquads []SquadUUID          `json:"activeInternalSquads,omitempty"`
	DataLimitBytes       int64                `json:"trafficLimitBytes,omitempty"`
	TrafficLimitStrategy TrafficLimitStrategy `json:"trafficLimitStrategy,omitempty"`
	// UsedBytes is filled from userTraffic.usedTrafficBytes in GET responses.
	UsedBytes int64 `json:"-"`
	// RawUserTraffic allows callers to read nested traffic fields.
	RawUserTraffic json.RawMessage `json:"userTraffic,omitempty"`
}

// patchUserRequest is the body of PATCH /api/users. The panel expects
// activeInternalSquads as a string array (not an array of objects).
type patchUserRequest struct {
	UUID                 string               `json:"uuid"`
	Status               *UserStatus          `json:"status,omitempty"`
	ActiveInternalSquads *[]string            `json:"activeInternalSquads,omitempty"`
	TrafficLimitBytes    *int64               `json:"trafficLimitBytes,omitempty"`
	TrafficLimitStrategy *TrafficLimitStrategy `json:"trafficLimitStrategy,omitempty"`
}

// NodeUsageEntry is a per-user row in a node's usage report.
type NodeUsageEntry struct {
	UserUUID string
	Bytes    int64
}

// WebhookEnvelope mirrors the outer shape of a Remnawave webhook payload.
type WebhookEnvelope struct {
	EventType string          `json:"event"`
	UserUUID  string          `json:"userUuid,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}
