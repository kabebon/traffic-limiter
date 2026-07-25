package engine

import (
	"testing"

	"github.com/traffic-limiter/internal/remnawave"
	"github.com/traffic-limiter/internal/state"
)

// planBLimitCeiling is defined in modified.go; tests reuse it by literal.

func TestShouldRestoreWhitelist_BuyMoreTraffic(t *testing.T) {
	// User had 10 GB original limit, blocked. Bot raised limit to 25 GB via докупка.
	st := &state.UserState{
		WLState:         state.WLBlocked,
		WLOriginalLimit: state.NullInt64{Int64: 10 * 1024 * 1024 * 1024, Valid: true},
	}
	panel := &remnawave.User{
		DataLimitBytes: 25 * 1024 * 1024 * 1024, // higher than original
		UsedBytes:      10 * 1024 * 1024 * 1024, // used is high but limit is now higher
	}
	if !shouldRestoreWhitelist(st, panel) {
		t.Error("expected restore after limit grew (buy more traffic)")
	}
}

func TestShouldRestoreWhitelist_ManualReset(t *testing.T) {
	// User had 10 GB, used 10 GB (blocked). Bot's reset button zeroed usedTraffic.
	st := &state.UserState{
		WLState:         state.WLBlocked,
		WLOriginalLimit: state.NullInt64{Int64: 10 * 1024 * 1024 * 1024, Valid: true},
	}
	panel := &remnawave.User{
		DataLimitBytes: 10 * 1024 * 1024 * 1024,
		UsedBytes:      0, // reset
	}
	if !shouldRestoreWhitelist(st, panel) {
		t.Error("expected restore after used dropped below original limit")
	}
}

func TestShouldRestoreWhitelist_PlanBOverrideIgnored(t *testing.T) {
	// Critical: our own Plan-B override set ~1 EiB limit. This MUST NOT trigger
	// a restore (otherwise whitelist unblocks instantly and meaninglessly).
	st := &state.UserState{
		WLState:         state.WLBlocked,
		WLOriginalLimit: state.NullInt64{Int64: 10 * 1024 * 1024 * 1024, Valid: true},
	}
	panel := &remnawave.User{
		DataLimitBytes: 1 << 60, // Plan-B ceiling
		UsedBytes:      0,
	}
	if shouldRestoreWhitelist(st, panel) {
		t.Error("Plan-B override must NOT trigger restore")
	}
}

func TestShouldRestoreWhitelist_StillExhausted(t *testing.T) {
	// User modified but still at/over original limit with no quota increase.
	st := &state.UserState{
		WLState:         state.WLBlocked,
		WLOriginalLimit: state.NullInt64{Int64: 10 * 1024 * 1024 * 1024, Valid: true},
	}
	panel := &remnawave.User{
		DataLimitBytes: 10 * 1024 * 1024 * 1024,
		UsedBytes:      10 * 1024 * 1024 * 1024, // equal — exhausted
	}
	if shouldRestoreWhitelist(st, panel) {
		t.Error("must not restore when still exhausted with no increase")
	}
}

func TestShouldRestoreWhitelist_NoOriginalLimit(t *testing.T) {
	// We never captured an original limit (edge case): only Signal 3 can fire,
	// which still requires originalLimit > 0. So result should be false —
	// safer to leave blocked than to guess.
	st := &state.UserState{
		WLState:         state.WLBlocked,
		WLOriginalLimit: state.NullInt64{Valid: false},
	}
	panel := &remnawave.User{
		DataLimitBytes: 50 * 1024 * 1024 * 1024,
		UsedBytes:      1 * 1024 * 1024 * 1024,
	}
	if shouldRestoreWhitelist(st, panel) {
		t.Error("without original limit captured, must not auto-restore")
	}
}

func TestShouldRestoreWhitelist_AdminRestoredSaneLimit(t *testing.T) {
	// Signal 3: admin sets limit to a sane value >= original, user under it.
	st := &state.UserState{
		WLState:         state.WLBlocked,
		WLOriginalLimit: state.NullInt64{Int64: 10 * 1024 * 1024 * 1024, Valid: true},
	}
	panel := &remnawave.User{
		DataLimitBytes: 15 * 1024 * 1024 * 1024, // sane, above original
		UsedBytes:      3 * 1024 * 1024 * 1024,
	}
	if !shouldRestoreWhitelist(st, panel) {
		t.Error("expected restore when admin set sane limit above original and user is under it")
	}
}

// Sanity: the ceiling used in modified.go matches what we test against.
func TestPlanBLimitCeilingValue(t *testing.T) {
	if planBLimitCeiling != int64(1)<<50 {
		t.Fatalf("planBLimitCeiling drifted: %d", planBLimitCeiling)
	}
	// And the Plan-B override set in whitelist.go is 1<<50 (same ceiling),
	// so values >= ceiling are treated as override.
	if (int64(1) << 50) >= planBLimitCeiling {
		// ok
	} else {
		t.Fatal("planB override must be >= ceiling")
	}
}

// TestEffectiveLimitForRelay verifies that the relay never forwards the Plan-B
// override (~1 EiB) to the bot, which would otherwise show ~1 million GB in the
// cabinet. It falls back to the original whitelist limit, or 0 if unknown.
func TestEffectiveLimitForRelay(t *testing.T) {
	planB := int64(1) << 50 // Plan-B override
	original := int64(100 * 1024 * 1024 * 1024)

	cases := []struct {
		name          string
		panelLimit    int64
		originalLimit int64
		want          int64
	}{
		{"plan_b_falls_back_to_original", planB, original, original},
		{"plan_b_no_original_returns_zero", planB, 0, 0},
		{"sane_panel_limit_passes_through", original, original, original},
		{"zero_panel_uses_original", 0, original, original},
		{"zero_panel_no_original_zero", 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			panel := &remnawave.User{DataLimitBytes: tc.panelLimit}
			got := effectiveLimitForRelay(panel, tc.originalLimit)
			if got != tc.want {
				t.Fatalf("effectiveLimitForRelay(panelLimit=%d, original=%d) = %d, want %d",
					tc.panelLimit, tc.originalLimit, got, tc.want)
			}
		})
	}
}
