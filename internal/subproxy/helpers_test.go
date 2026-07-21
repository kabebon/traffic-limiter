package subproxy

import "testing"

func TestExtractShortUUID(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"base", "/sub/abc123", "abc123"},
		{"with_client_type", "/sub/abc123/stash", "abc123"},
		{"clash", "/sub/abc123/clash", "abc123"},
		{"trailing_slash", "/sub/abc123/", "abc123"},
		{"outline", "/sub/outline/abc123/ss/sometag", "abc123"},
		{"no_sub_prefix", "/api/foo", ""},
		{"bare_sub", "/sub/", ""},
		{"short_with_dash", "/sub/a1b2-c3d4", "a1b2-c3d4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractShortUUID(tc.path)
			if got != tc.want {
				t.Fatalf("extractShortUUID(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestPercentEncode(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"VPN · active", "VPN%20%C2%B7%20active"},
		{"simple", "simple"},
		{"⚠️ alert", "%E2%9A%A0%EF%B8%8F%20alert"},
		{"a/b", "a%2Fb"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := percentEncode(tc.in)
			if got != tc.want {
				t.Fatalf("percentEncode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
