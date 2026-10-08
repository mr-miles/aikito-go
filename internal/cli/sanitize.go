package cli

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/mr-miles/aikito-go/internal/mcp"
)

// SanitizeMCPURL mirrors add.py's _sanitize_mcp_url: strips userinfo
// credentials (https://user:pass@host/...) and known-sensitive query
// parameters (mcp.IsSensitiveURLParameter) from a URL before it is ever
// written into the git-tracked canonical mcps/<name>.toml. Returns the
// sanitized URL and human-readable warnings that never include the actual
// secret values. A URL that fails to parse is returned unchanged (same
// best-effort fallback as Python's try/except).
func SanitizeMCPURL(rawURL, serverName string) (string, []string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, nil
	}
	var warnings []string

	pw, _ := u.User.Password()
	if u.User != nil && (u.User.Username() != "" || pw != "") {
		u.User = nil
		// Python rebuilds the netloc from urlsplit's hostname, which is
		// lowercased, plus the port.
		host := strings.ToLower(u.Hostname())
		if p := u.Port(); p != "" {
			host += ":" + p
		}
		u.Host = host
		warnings = append(warnings, fmt.Sprintf(
			"[SECURITY] URL for '%s' contains userinfo credentials. "+
				"Stripped from canonical TOML to prevent leakage into workspace. "+
				"Supply credentials via headers or environment variables.", serverName))
	}

	if u.RawQuery != "" {
		pairs := parseQueryOrdered(u.RawQuery)
		clean := make([]queryPair, 0, len(pairs))
		var stripped []string
		for _, p := range pairs {
			if mcp.IsSensitiveURLParameter(p.key) {
				stripped = append(stripped, p.key)
			} else {
				clean = append(clean, p)
			}
		}
		if len(stripped) > 0 {
			u.RawQuery = encodeQueryOrdered(clean)
			warnings = append(warnings, fmt.Sprintf(
				"[SECURITY] URL for '%s' contains sensitive query parameter(s): %s. "+
					"Stripped from canonical TOML to prevent leakage into workspace. "+
					"Supply credentials via headers or environment variables.",
				serverName, strings.Join(stripped, ", ")))
		}
	}

	return u.String(), warnings
}

type queryPair struct{ key, value string }

// parseQueryOrdered parses a raw query string into ordered key/value pairs,
// preserving duplicates, blank values, and original order — mirroring
// Python's parse_qsl(keep_blank_values=True) (Go's net/url.Values is an
// unordered map and would lose the original parameter order on re-encode).
func parseQueryOrdered(raw string) []queryPair {
	var out []queryPair
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		var key, value string
		if i := strings.IndexByte(part, '='); i >= 0 {
			key, value = part[:i], part[i+1:]
		} else {
			key = part
		}
		k, err1 := url.QueryUnescape(strings.ReplaceAll(key, "+", " "))
		if err1 != nil {
			k = key
		}
		v, err2 := url.QueryUnescape(strings.ReplaceAll(value, "+", " "))
		if err2 != nil {
			v = value
		}
		out = append(out, queryPair{k, v})
	}
	return out
}

func encodeQueryOrdered(pairs []queryPair) string {
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = url.QueryEscape(p.key) + "=" + url.QueryEscape(p.value)
	}
	return strings.Join(parts, "&")
}

// SanitizeMCPHeaders mirrors add.py's _sanitize_mcp_headers: any header
// whose name looks like a credential header (mcp.IsCredentialHeader —
// Authorization, token, secret, api-key, cookie, ...) and whose value is
// NOT already an env-var reference (mcp.EnvironmentReference) is replaced
// with an ${AIKITO_<SERVER>_<KEY>} placeholder before being written to
// disk, never the plaintext secret.
func SanitizeMCPHeaders(headers map[string]string, serverName string) (map[string]string, []string) {
	safeServer := toEnvSafe(strings.ToUpper(serverName))
	sanitized := make(map[string]string, len(headers))
	var warnings []string
	for key, value := range headers {
		isReference := mcp.EnvironmentReference(value) != ""
		isSensitive := mcp.IsCredentialHeader(key)
		if isSensitive && !isReference {
			safeKey := toEnvSafe(strings.ToUpper(key))
			envVar := fmt.Sprintf("AIKITO_%s_%s", safeServer, safeKey)
			placeholder := "${" + envVar + "}"
			sanitized[key] = placeholder
			warnings = append(warnings, fmt.Sprintf(
				"[SECURITY] Detected plaintext secret in header '%s'. "+
					"Replaced with environment variable reference '%s' to prevent secret leakage into workspace.\n"+
					"       To provide this credential at runtime, set the environment variable and run:\n"+
					"         export %s=<your-secret-value>\n"+
					"       (or configure canonical [authentication] via 'aikito auth mcp')",
				key, placeholder, envVar))
		} else {
			sanitized[key] = value
		}
	}
	return sanitized, warnings
}

// toEnvSafe mirrors "".join(c if c.isalnum() else "_" for c in s): every
// character that isn't an ASCII letter or digit becomes an underscore.
// Callers pass an already-uppercased string, so only A-Z/0-9 need checking.
func toEnvSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
