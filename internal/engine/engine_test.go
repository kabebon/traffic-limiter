package engine

import (
	"testing"
)

func TestDropSquads(t *testing.T) {
	in := []string{"basic", "whitelist", "extra"}
	got := dropSquads(in, "whitelist")
	if len(got) != 2 {
		t.Fatalf("want 2 squads, got %d", len(got))
	}
	for _, s := range got {
		if s == "whitelist" {
			t.Fatalf("whitelist should have been dropped")
		}
	}
}

func TestDropSquads_Empty(t *testing.T) {
	got := dropSquads(nil, "x")
	if len(got) != 0 {
		t.Fatalf("want empty slice, got %d", len(got))
	}
}

func TestEnsureSquads_AddsMissing(t *testing.T) {
	in := []string{"basic"}
	got := ensureSquads(in, "basic", "whitelist")
	if len(got) != 2 {
		t.Fatalf("want 2 squads, got %d (%v)", len(got), got)
	}
}

func TestEnsureSquads_NoDuplicates(t *testing.T) {
	in := []string{"basic", "whitelist"}
	got := ensureSquads(in, "basic", "whitelist")
	if len(got) != 2 {
		t.Fatalf("want 2 squads (no dups), got %d (%v)", len(got), got)
	}
}
