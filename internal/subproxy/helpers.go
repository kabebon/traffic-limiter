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

// base64Announce encodes a status message with the "base64:" prefix so that
// clients like Happ and INCY properly decode and render it in the subscription
// view without showing raw base64 strings. Literal "\n" sequences are converted
// to actual line breaks so messages can be written as a single line in .env files.
func base64Announce(s string) string {
	s = strings.ReplaceAll(s, "\\n", "\n")
	return "base64:" + base64.StdEncoding.EncodeToString([]byte(s))
}

// unlimitedUserinfo returns Subscription-Userinfo with total=0 while preserving
// the panel's upload/download/expire fields. In Remnawave subscriptions and
// common clients, total=0 is displayed as unlimited traffic.
func unlimitedUserinfo(v string) string {
	if strings.TrimSpace(v) == "" {
		return "upload=0; download=0; total=0"
	}
	parts := strings.Split(v, ";")
	sawTotal := false
	for i, part := range parts {
		p := strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(p), "total=") {
			parts[i] = " total=0"
			sawTotal = true
			continue
		}
		parts[i] = part
	}
	if !sawTotal {
		parts = append(parts, " total=0")
	}
	return strings.Join(parts, ";")
}
