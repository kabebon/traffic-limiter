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

// SquadUUID is a string UUID identifying an internal squad.
type SquadUUID string

// SquadList is the value of the panel's activeInternalSquads field. Remnawave
// is asymmetric here: GET /api/users returns an array of objects
// ([{"uuid":"...","name":"..."},...]) while PATCH /api/users accepts an array
// of plain UUID strings (["...",...]). SquadList therefore unmarshals from
// either shape and always marshals back as an array of strings (the PATCH
// shape), so a round-trip through this type yields what the panel expects on
// write.
type SquadList []SquadUUID

// UnmarshalJSON accepts an array of strings or an array of objects exposing a
// "uuid" field (other object fields are ignored).
func (s *SquadList) UnmarshalJSON(data []byte) error {
	// Try array of strings first (older panel versions).
	var strs []string
	if err := json.Unmarshal(data, &strs); err == nil {
		out := make([]SquadUUID, 0, len(strs))
		for _, v := range strs {
			out = append(out, SquadUUID(v))
		}
		*s = out
		return nil
	}

	// Fall back to array of objects: [{"uuid":"..."}, ...].
	var objs []struct {
		UUID     string `json:"uuid"`
		SquadUUID string `json:"squadUuid"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal(data, &objs); err != nil {
		return err
	}
	out := make([]SquadUUID, 0, len(objs))
	for _, o := range objs {
		if o.UUID != "" {
			out = append(out, SquadUUID(o.UUID))
		} else if o.SquadUUID != "" {
			out = append(out, SquadUUID(o.SquadUUID))
		}
	}
	*s = out
	return nil
}

// MarshalJSON always writes the PATCH shape: an array of UUID strings.
func (s SquadList) MarshalJSON() ([]byte, error) {
	strs := make([]string, 0, len(s))
	for _, v := range s {
		strs = append(strs, string(v))
	}
	return json.Marshal(strs)
}

// Strings returns the squad UUIDs as a plain string slice.
func (s SquadList) Strings() []string {
	out := make([]string, 0, len(s))
	for _, v := range s {
		out = append(out, string(v))
	}
	return out
}

// User is the subset of user fields we read/write.
type User struct {
	UUID                 string               `json:"uuid"`
	Username             string               `json:"username,omitempty"`
	Status               UserStatus           `json:"status"`
	ActiveInternalSquads SquadList            `json:"activeInternalSquads,omitempty"`
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
