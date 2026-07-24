package remnawave

import (
	"encoding/json"
	"testing"
)

// TestSquadList_UnmarshalObjects covers the panel v2 GET shape where
// activeInternalSquads is an array of objects, not strings.
func TestSquadList_UnmarshalObjects(t *testing.T) {
	raw := []byte(`[{"uuid":"11111111-0000-0000-0000-000000000001","name":"basic"},{"uuid":"22222222-0000-0000-0000-000000000002","name":"whitelist"}]`)
	var s SquadList
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := SquadList{
		"11111111-0000-0000-0000-000000000001",
		"22222211-0000-0000-0000-000000000002",
	}
	if len(s) != len(want) || string(s[0]) != "11111111-0000-0000-0000-000000000001" || string(s[1]) != "22222222-0000-0000-0000-000000000002" {
		t.Fatalf("got %v, want %v", s, want)
	}
}

// TestSquadList_UnmarshalStrings covers the older panel shape (array of
// strings) and is also what we write back on PATCH.
func TestSquadList_UnmarshalStrings(t *testing.T) {
	raw := []byte(`["11111111-0000-0000-0000-000000000001","22222222-0000-0000-0000-000000000002"]`)
	var s SquadList
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(s) != 2 || string(s[0]) != "11111111-0000-0000-0000-000000000001" {
		t.Fatalf("got %v", s)
	}
}

// TestSquadList_MarshalStrings verifies we always write the PATCH shape
// (array of strings), even though we may have read objects.
func TestSquadList_MarshalStrings(t *testing.T) {
	s := SquadList{
		"11111111-0000-0000-0000-000000000001",
		"22222222-0000-0000-0000-000000000002",
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `["11111111-0000-0000-0000-000000000001","22222222-0000-0000-0000-000000000002"]`
	if string(out) != want {
		t.Fatalf("marshal = %s, want %s", out, want)
	}
}

// TestDecodeUser_SquadsAsObjects exercises the full decodeUser path with the
// panel v2 shape (squads as objects), which was the production failure.
func TestDecodeUser_SquadsAsObjects(t *testing.T) {
	raw := []byte(`{
		"uuid":"35cf30db-bbd1-4d58-b697-124b66919102",
		"status":"LIMITED",
		"activeInternalSquads":[{"uuid":"11111111-0000-0000-0000-000000000001","name":"basic"}],
		"trafficLimitBytes":32212254720,
		"trafficLimitStrategy":"MONTH",
		"userTraffic":{"usedTrafficBytes":352592788}
	}`)
	u, err := decodeUser(raw)
	if err != nil {
		t.Fatalf("decodeUser: %v", err)
	}
	if len(u.ActiveInternalSquads) != 1 || string(u.ActiveInternalSquads[0]) != "11111111-0000-0000-0000-000000000001" {
		t.Fatalf("squads = %v", u.ActiveInternalSquads)
	}
	if u.Status != StatusLimited {
		t.Fatalf("status = %v", u.Status)
	}
	if u.UsedBytes != 352592788 {
		t.Fatalf("used = %d", u.UsedBytes)
	}
}
