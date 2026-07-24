package subproxy

import (
	"encoding/base64"
	"net/url"
	"strings"
)

// extractShortUUID pulls the {shortUuid} segment out of any /sub/... path the
// panel exposes. Supported shapes:
//
//	/sub/{shortUuid}
//	/sub/{shortUuid}/{clientType}        (stash, singbox, mihomo, clash, json, ...)
//	/sub/outline/{shortUuid}/ss/{tag}    (outline SS link)
//
// Returns "" if no short UUID could be identified (the proxy then falls back
// to the "active" title, which is always safe).
func extractShortUUID(path string) string {
	if !strings.HasPrefix(path, "/sub/") {
		return ""
	}
	path = strings.TrimPrefix(path, "/sub/")
	path = strings.Trim(path, "/")
	if path == "" {
		return ""
	}
	segments := strings.Split(path, "/")

	// /sub/outline/{shortUuid}/ss/{tag}
	if segments[0] == "outline" && len(segments) >= 2 {
		return segments[1]
	}
	// /sub/{shortUuid} or /sub/{shortUuid}/{clientType}
	return segments[0]
}

// percentEncode URL-encodes a profile-title value the way Happ / INCY /
// v2rayNG expect (they decode it before displaying). Non-ASCII and the
// standard reserved set are encoded; spaces become %20.
func percentEncode(s string) string {
	// QueryEscape would turn spaces into '+', which some clients render
	// literally. PathEscape keeps spaces as %20 and is closer to what
	// these clients expect for the profile-title header.
	return url.PathEscape(s)
}

// base64Title encodes a profile-title the way the Remnawave panel does —
// base64 with a "base64:" prefix. Happ/INCY/v2rayNG render this cleanly,
// whereas raw percent-encoding shows up as mojibake in some clients.
func base64Title(s string) string {
	return "base64:" + base64.StdEncoding.EncodeToString([]byte(s))
}
