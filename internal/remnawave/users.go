package remnawave

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// GetUser returns the current state of a user. Reads the userTraffic block
// to populate UsedBytes.
func (c *Client) GetUser(ctx context.Context, uuid string) (*User, error) {
	return c.getUserByPath(ctx, "/api/users/"+uuid)
}

// FindUserByShortUUID resolves a short UUID (the public subscription id, e.g.
// "Eg_ShWCVXs1Uhx1F") to the full user via the panel's /api/users?search=
// endpoint. Used by the subproxy resolver when /api/sub/{short}/info does not
// expose the full user uuid (some panel versions only return shortUuid).
// Returns the first matching ACTIVE/LIMITED/EXPIRED user whose shortUuid
// matches; nil if nothing found.
func (c *Client) FindUserByShortUUID(ctx context.Context, short string) (*User, error) {
	if short == "" {
		return nil, nil
	}
	var w struct {
		Response []json.RawMessage `json:"response"`
		Data     []json.RawMessage `json:"data"`
		Users    []json.RawMessage `json:"users"`
	}
	path := fmt.Sprintf("/api/users?search=%s&limit=10", short)
	if err := c.do(ctx, http.MethodGet, path, nil, &w); err != nil {
		return nil, err
	}
	items := w.Response
	if len(w.Data) > 0 {
		items = w.Data
	} else if len(w.Users) > 0 {
		items = w.Users
	}
	for _, raw := range items {
		u, err := decodeUser(raw)
		if err != nil || u == nil || u.UUID == "" {
			continue
		}
		// Confirm the short match on the parsed object to avoid collisions
		// (search is substring-based on the panel side).
		var probe struct {
			ShortUUID string `json:"shortUuid"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.ShortUUID == "" || probe.ShortUUID == short {
			return u, nil
		}
	}
	return nil, nil
}

// getUserByPath fetches a single user from an arbitrary /api/users... path and
// decodes the response envelope. Shared by GetUser (by uuid) and the subproxy
// resolver fallback.
func (c *Client) getUserByPath(ctx context.Context, path string) (*User, error) {
	var w struct {
		Response json.RawMessage `json:"response"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &w); err != nil {
		return nil, err
	}
	if len(w.Response) == 0 {
		return nil, fmt.Errorf("get user %s: empty response", path)
	}
	u, err := decodeUser(w.Response)
	if err != nil {
		return nil, fmt.Errorf("decode user %s: %w", path, err)
	}
	return u, nil
}

// GetUsers returns all users by paginating through the API.
func (c *Client) GetUsers(ctx context.Context) ([]*User, error) {
	var all []*User
	offset := 0
	limit := 1000

	for {
		var w struct {
			Response []json.RawMessage `json:"response"`
			Data     []json.RawMessage `json:"data"`
			Users    []json.RawMessage `json:"users"`
		}
		path := fmt.Sprintf("/api/users?limit=%d&offset=%d", limit, offset)
		if err := c.do(ctx, http.MethodGet, path, nil, &w); err != nil {
			return nil, err
		}

		items := w.Response
		if len(w.Data) > 0 {
			items = w.Data
		} else if len(w.Users) > 0 {
			items = w.Users
		}

		if len(items) == 0 {
			break
		}

		for _, raw := range items {
			u, err := decodeUser(raw)
			if err == nil && u != nil {
				all = append(all, u)
			}
		}

		if len(items) < limit {
			break
		}
		offset += limit
	}
	return all, nil
}

// decodeUser parses a panel user object, including the userTraffic sub-object
// (usedTrafficBytes lives there in current panel versions, not at the top level).
func decodeUser(raw json.RawMessage) (*User, error) {
	var u User
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, err
	}
	// UsedBytes comes from userTraffic.usedTrafficBytes.
	if len(u.RawUserTraffic) > 0 {
		var t struct {
			Used                 int64 `json:"usedTrafficBytes"`
			LifetimeUsed         int64 `json:"lifetimeUsedTrafficBytes"`
			LastConnectedNode    any   `json:"lastConnectedNodeUuid"`
		}
		_ = json.Unmarshal(u.RawUserTraffic, &t)
		u.UsedBytes = t.Used
	}
	return &u, nil
}

// PatchUser applies the requested changes. Nil fields are omitted from the request.
// squads is the full desired list of activeInternalSquads UUIDs (panel replaces
// the list on PATCH, not appends).
func (c *Client) PatchUser(ctx context.Context, uuid string, status *UserStatus,
	squads *[]string, trafficLimitBytes *int64, strategy *TrafficLimitStrategy) (*User, error) {
	body := patchUserRequest{
		UUID:                 uuid,
		Status:               status,
		TrafficLimitBytes:    trafficLimitBytes,
		TrafficLimitStrategy: strategy,
	}
	if squads != nil {
		// Copy to avoid the caller's slice being mutated by json marshalling surprises.
		copySquads := make([]string, len(*squads))
		copy(copySquads, *squads)
		body.ActiveInternalSquads = &copySquads
	}
	var w struct {
		Response json.RawMessage `json:"response"`
	}
	if err := c.do(ctx, http.MethodPatch, "/api/users", body, &w); err != nil {
		return nil, err
	}
	if len(w.Response) > 0 {
		if u, err := decodeUser(w.Response); err == nil {
			return u, nil
		}
	}
	// Fallback: re-fetch.
	return c.GetUser(ctx, uuid)
}

// ResetUserTraffic zeroes the user's used traffic counter.
// Path matches what the panel actually exposes (see bedolaga
// reset_user_traffic → /api/users/{uuid}/actions/reset-traffic).
func (c *Client) ResetUserTraffic(ctx context.Context, uuid string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/users/%s/actions/reset-traffic", uuid), struct{}{}, nil)
}

// EnableUser sets the user ACTIVE via the panel's action endpoint.
func (c *Client) EnableUser(ctx context.Context, uuid string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/users/%s/actions/enable", uuid), struct{}{}, nil)
}

// DisableUser sets the user DISABLED via the panel's action endpoint.
func (c *Client) DisableUser(ctx context.Context, uuid string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/users/%s/actions/disable", uuid), struct{}{}, nil)
}

// HasSquad reports whether the user is currently in the given squad.
func HasSquad(u *User, squadUUID string) bool {
	if u == nil {
		return false
	}
	for _, s := range u.ActiveInternalSquads {
		if string(s) == squadUUID {
			return true
		}
	}
	return false
}

// SquadsOf returns the current squad UUIDs as a string slice.
func SquadsOf(u *User) []string {
	if u == nil {
		return nil
	}
	out := make([]string, 0, len(u.ActiveInternalSquads))
	for _, s := range u.ActiveInternalSquads {
		out = append(out, string(s))
	}
	return out
}
