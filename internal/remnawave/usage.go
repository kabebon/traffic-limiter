package remnawave

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// NodeUsage returns per-user traffic totals for a given node over [from, to].
//
// Path mirrors the bedolaga client's get_bandwidth_stats_node_users:
//   GET /api/bandwidth-stats/nodes/{nodeUuid}/users?start=...&end=...&topUsersLimit=...
//
// The response shape is { response: { users: [ { uuid/username, usedTrafficBytes } ] } }
// (we accept a few field-name variants since the panel renames these occasionally).
func (c *Client) NodeUsage(ctx context.Context, nodeUUID string, from, to time.Time) ([]NodeUsageEntry, error) {
	path := fmt.Sprintf("/api/bandwidth-stats/nodes/%s/users?start=%s&end=%s&topUsersLimit=1000",
		nodeUUID, from.Format(time.DateOnly), to.Format(time.DateOnly))

	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return nil, fmt.Errorf("node usage %s: %w", nodeUUID, err)
	}
	return parseNodeUsage(raw)
}

// parseNodeUsage extracts per-user byte totals from any known response shape.
func parseNodeUsage(raw json.RawMessage) ([]NodeUsageEntry, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	// 1) Common envelope: {"response": {...}}
	var env struct {
		Response json.RawMessage `json:"response"`
		Data     json.RawMessage `json:"data"`
	}
	body := raw
	if err := json.Unmarshal(raw, &env); err == nil {
		if len(env.Response) > 0 {
			body = env.Response
		} else if len(env.Data) > 0 {
			body = env.Data
		}
	}

	// 2) Try: object with "users" array (current panel shape).
	var wrapper struct {
		Users []struct {
			UUID     string `json:"uuid"`
			UserUUID string `json:"userUuid"`
			Username string `json:"username"`
			User     struct {
				UUID string `json:"uuid"`
			} `json:"user"`
			Used       int64 `json:"usedTrafficBytes"`
			UsedBytes  int64 `json:"usedBytes"`
			BytesTotal int64 `json:"bytesTotal"`
			Total      int64 `json:"totalBytes"`
		} `json:"users"`
	}
	if err := json.Unmarshal(body, &wrapper); err == nil && len(wrapper.Users) > 0 {
		out := make([]NodeUsageEntry, 0, len(wrapper.Users))
		for _, e := range wrapper.Users {
			uuid := e.UUID
			if uuid == "" {
				uuid = e.UserUUID
			}
			if uuid == "" {
				uuid = e.User.UUID
			}
			if uuid == "" {
				uuid = e.Username // last-resort key, not a real uuid
			}
			bytes := pickNonZero(e.Used, e.UsedBytes, e.BytesTotal, e.Total)
			out = append(out, NodeUsageEntry{UserUUID: uuid, Bytes: bytes})
		}
		if len(out) > 0 {
			return out, nil
		}
	}

	// 3) Try: object with "topUsers" array (newest panel shape).
	var topUsersWrapper struct {
		TopUsers []struct {
			Username string `json:"username"`
			Total    int64  `json:"total"`
		} `json:"topUsers"`
	}
	if err := json.Unmarshal(body, &topUsersWrapper); err == nil && len(topUsersWrapper.TopUsers) > 0 {
		out := make([]NodeUsageEntry, 0, len(topUsersWrapper.TopUsers))
		for _, e := range topUsersWrapper.TopUsers {
			out = append(out, NodeUsageEntry{UserUUID: e.Username, Bytes: e.Total})
		}
		if len(out) > 0 {
			return out, nil
		}
	}

	// 4) Try: bare array.
	var arr []struct {
		UUID       string `json:"uuid"`
		UserUUID   string `json:"userUuid"`
		Username   string `json:"username"`
		Used       int64  `json:"usedTrafficBytes"`
		UsedBytes  int64  `json:"usedBytes"`
		BytesTotal int64  `json:"bytesTotal"`
		Total      int64  `json:"totalBytes"`
	}
	if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
		out := make([]NodeUsageEntry, 0, len(arr))
		for _, e := range arr {
			uuid := e.UUID
			if uuid == "" {
				uuid = e.UserUUID
			}
			if uuid == "" {
				uuid = e.Username
			}
			bytes := pickNonZero(e.Used, e.UsedBytes, e.BytesTotal, e.Total)
			out = append(out, NodeUsageEntry{UserUUID: uuid, Bytes: bytes})
		}
		if len(out) > 0 {
			return out, nil
		}
	}

	// 5) Unknown shape: surface raw snippet for diagnostics.
	snippet := string(body)
	if len(snippet) > 256 {
		snippet = snippet[:256]
	}
	return nil, fmt.Errorf("unrecognized node usage payload: %s", snippet)
}

func pickNonZero(vs ...int64) int64 {
	for _, v := range vs {
		if v > 0 {
			return v
		}
	}
	return 0
}
