package integrationtools

import (
	"net"
	"net/url"
	"regexp"
	"strings"
)

var webSecretPattern = regexp.MustCompile(`(?i)(-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(?:api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|password|passwd|client[_-]?secret|secret[_-]?key|token|secret|signature|credential)\b["']?\s*[:=]\s*["']?\S+|\bbearer\s+\S+|\b(?:sk-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]+|AKIA[A-Z0-9]{16})\b)`)
var webURLPattern = regexp.MustCompile(`(?i)https?://[^\s<>"'\x60]+`)

// Conservative fail-open locally, fail-closed for Jev egress. Credential-bearing
// text, private URLs and secret paths bypass classification altogether. Known
// configured credentials are checked even when the source omits their labels.
func webSafeText(text string, secrets []string) bool {
	if webSecretPattern.MatchString(text) {
		return false
	}
	for _, secret := range secrets {
		if strings.TrimSpace(secret) != "" && strings.Contains(text, secret) {
			return false
		}
	}
	for _, raw := range webURLPattern.FindAllString(text, -1) {
		u, err := url.Parse(raw)
		if err != nil || u.User != nil {
			return false
		}
		host := strings.ToLower(u.Hostname())
		if host == "" || host == "localhost" || !strings.Contains(host, ".") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".localhost") {
			return false
		}
		if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()) {
			return false
		}
		path := strings.ToLower(u.Path)
		for _, segment := range strings.Split(path, "/") {
			if segment == ".env" || strings.HasPrefix(segment, ".env.") || segment == ".ssh" || segment == "secrets" || segment == "credentials" || strings.HasSuffix(segment, ".pem") || strings.HasSuffix(segment, ".key") {
				return false
			}
		}
		// Signed URLs and arbitrary query values can be credentials without a known
		// parameter name. Do not send any URL with query parameters to a third party.
		if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
			return false
		}
	}
	return true
}
