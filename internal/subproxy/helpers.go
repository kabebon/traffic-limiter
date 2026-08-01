package subproxy

import (
	"encoding/base64"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
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

func parseUserinfo(ui string) (upload, download, total, expire int64) {
	for _, field := range strings.Split(ui, ";") {
		field = strings.TrimSpace(strings.ToLower(field))
		if strings.HasPrefix(field, "upload=") {
			upload, _ = strconv.ParseInt(strings.TrimPrefix(field, "upload="), 10, 64)
		} else if strings.HasPrefix(field, "download=") {
			download, _ = strconv.ParseInt(strings.TrimPrefix(field, "download="), 10, 64)
		} else if strings.HasPrefix(field, "total=") {
			total, _ = strconv.ParseInt(strings.TrimPrefix(field, "total="), 10, 64)
		} else if strings.HasPrefix(field, "expire=") {
			expire, _ = strconv.ParseInt(strings.TrimPrefix(field, "expire="), 10, 64)
		}
	}
	return
}

func formatDaysLeft(expire int64) string {
	if expire <= 0 {
		return "∞"
	}
	secLeft := expire - time.Now().Unix()
	if secLeft <= 0 {
		return "0"
	}
	days := int(secLeft / 86400)
	if days == 0 && secLeft > 0 {
		days = 1
	}
	return strconv.Itoa(days)
}

func formatExpireDate(expire int64) string {
	if expire <= 0 {
		return "∞"
	}
	return time.Unix(expire, 0).Format("02.01.2006")
}

func formatBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	if exp >= len(units) {
		exp = len(units) - 1
	}
	val := float64(b) / float64(div)
	if val == math.Trunc(val) {
		return fmt.Sprintf("%.0f %s", val, units[exp])
	}
	return fmt.Sprintf("%.1f %s", val, units[exp])
}

func renderPlaceholders(s, ui string) string {
	if s == "" || !strings.Contains(s, "{") {
		return s
	}
	upload, download, total, expire := parseUserinfo(ui)

	daysLeft := formatDaysLeft(expire)
	expireDate := formatExpireDate(expire)
	totalStr := "∞"
	if total > 0 {
		totalStr = formatBytes(total)
	}
	usedStr := formatBytes(upload + download)
	leftStr := "∞"
	if total > 0 {
		left := total - (upload + download)
		if left <= 0 {
			leftStr = "0 B"
		} else {
			leftStr = formatBytes(left)
		}
	}

	replacements := []string{
		"{{DAYS_LEFT}}", daysLeft,
		"{DAYS_LEFT}", daysLeft,
		"{{EXPIRE_DATE}}", expireDate,
		"{EXPIRE_DATE}", expireDate,
		"{{TOTAL_TRAFFIC}}", totalStr,
		"{TOTAL_TRAFFIC}", totalStr,
		"{{TRAFFIC_TOTAL}}", totalStr,
		"{TRAFFIC_TOTAL}", totalStr,
		"{{USED_TRAFFIC}}", usedStr,
		"{USED_TRAFFIC}", usedStr,
		"{{TRAFFIC_USED}}", usedStr,
		"{TRAFFIC_USED}", usedStr,
		"{{LEFT_TRAFFIC}}", leftStr,
		"{LEFT_TRAFFIC}", leftStr,
		"{{TRAFFIC_LEFT}}", leftStr,
		"{TRAFFIC_LEFT}", leftStr,
	}
	r := strings.NewReplacer(replacements...)
	return r.Replace(s)
}

func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}
