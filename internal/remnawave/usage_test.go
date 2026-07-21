package remnawave

import (
	"encoding/json"
	"testing"
)

func TestParseNodeUsage_PanelShape(t *testing.T) {
	// Current panel shape: { response: { users: [ { uuid, usedTrafficBytes } ] } }
	raw := json.RawMessage(`{"response":{"users":[
		{"uuid":"u1","usedTrafficBytes":1024},
		{"uuid":"u2","usedBytes":2048}
	]}}`)
	got, err := parseNodeUsage(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	m := map[string]int64{}
	for _, e := range got {
		m[e.UserUUID] = e.Bytes
	}
	if m["u1"] != 1024 || m["u2"] != 2048 {
		t.Fatalf("unexpected values: %+v", m)
	}
}

func TestParseNodeUsage_ArrayShape(t *testing.T) {
	// Fallback: bare array of objects.
	raw := json.RawMessage(`{"response":[
		{"userUuid":"u1","totalBytes":5120},
		{"userUuid":"u2","bytesTotal":100}
	]}`)
	got, err := parseNodeUsage(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	m := map[string]int64{}
	for _, e := range got {
		m[e.UserUUID] = e.Bytes
	}
	if m["u1"] != 5120 || m["u2"] != 100 {
		t.Fatalf("unexpected values: %+v", m)
	}
}

func TestParseNodeUsage_UnknownShape(t *testing.T) {
	raw := json.RawMessage(`{"weird":"payload"}`)
	if _, err := parseNodeUsage(raw); err == nil {
		t.Fatalf("expected error on unknown shape")
	}
}

func TestParseNodeUsage_Empty(t *testing.T) {
	if got, err := parseNodeUsage(nil); err != nil || len(got) != 0 {
		t.Fatalf("nil input should return empty, no error; got %v %v", got, err)
	}
}

func TestPickNonZero(t *testing.T) {
	if got := pickNonZero(0, 0, 5, 0); got != 5 {
		t.Fatalf("want 5, got %d", got)
	}
	if got := pickNonZero(0, 0, 0); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}
