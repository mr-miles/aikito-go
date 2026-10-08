package mcp

import (
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// SensitiveURLParameters mirrors redact.py's SENSITIVE_URL_PARAMETERS.
var SensitiveURLParameters = map[string]bool{
	"code": true, "access_token": true, "refresh_token": true, "id_token": true,
	"token": true, "secret": true, "client_secret": true, "password": true, "pass": true,
	"credential": true, "signature": true,
	"api_key": true, "apikey": true, "api-key": true, "x-api-key": true, "key": true,
	"authorization": true, "auth": true,
	"session": true, "session_token": true, "user_token": true, "private_token": true,
	"x-amz-signature": true, "x_amz_signature": true, "x-amz-credential": true, "x_amz_credential": true,
	"x-amz-security-token": true, "x_amz_security_token": true,
	"x-goog-signature": true, "x_goog_signature": true, "x-goog-credential": true, "x_goog_credential": true,
	"sig": true,
	"pat": true, "app_token": true, "app-token": true, "auth_token": true, "auth-token": true,
	"jwt": true, "bearer": true,
}

var sensitiveParamSegments = map[string]bool{
	"token": true, "secret": true, "password": true, "passwd": true,
	"credential": true, "credentials": true, "signature": true, "jwt": true, "apikey": true,
}

var sensitiveParamPrefixes = []string{"auth_", "oauth_"}

var sensitiveParamSuffixes = []string{
	"_key", "_token", "_secret", "_password", "_pass", "_sig", "_signature",
	"_credential", "_credentials", "_jwt", "_pat",
}

// IsSensitiveURLParameter mirrors is_sensitive_url_parameter: exact naming
// plus controlled segment/prefix/suffix matching, avoiding false positives
// on legitimate parameters like "author" or "authentication_mode".
func IsSensitiveURLParameter(paramName string) bool {
	lowered := strings.ToLower(paramName)
	normalized := strings.ReplaceAll(strings.ReplaceAll(lowered, "-", "_"), ".", "_")

	if SensitiveURLParameters[lowered] || SensitiveURLParameters[normalized] {
		return true
	}
	for _, seg := range strings.Split(normalized, "_") {
		if sensitiveParamSegments[seg] {
			return true
		}
	}
	for _, prefix := range sensitiveParamPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	for _, suffix := range sensitiveParamSuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func hasSensitiveParameters(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	values := u.Query()
	for key := range values {
		if IsSensitiveURLParameter(strings.ToLower(key)) {
			return true
		}
	}
	return false
}

var envReferencePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`),
	regexp.MustCompile(`^\{env:([A-Za-z_][A-Za-z0-9_]*)\}$`),
	regexp.MustCompile(`^!!js process\.env\.([A-Za-z_][A-Za-z0-9_]*)$`),
}

var credentialHeaderFragments = []string{
	"authorization", "token", "secret", "password", "api-key", "api_key", "apikey", "cookie",
}

// EnvironmentReference mirrors _environment_reference: matches exactly one
// of "${VAR}", "{env:VAR}", or "!!js process.env.VAR" (full-string match),
// returning the captured env var name, or "" if value matches none.
func EnvironmentReference(value string) string {
	for _, pattern := range envReferencePatterns {
		if m := pattern.FindStringSubmatch(value); m != nil {
			return m[1]
		}
	}
	return ""
}

// IsCredentialHeader mirrors _is_credential_header: lowercase name contains
// any credential-header fragment as a substring.
func IsCredentialHeader(name string) bool {
	lower := strings.ToLower(name)
	for _, frag := range credentialHeaderFragments {
		if strings.Contains(lower, frag) {
			return true
		}
	}
	return false
}

// IsLoopbackURL mirrors _is_loopback_url: host is localhost/*.localhost, or
// a loopback IP literal.
func IsLoopbackURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// RedactProbeError mirrors _redact_probe_error: collects every credential
// header's literal value (plus, for Authorization specifically, the part
// after the first space — the bare token without its scheme word) into a
// secret set, replaces all occurrences (longest-first, to avoid partial-
// substring corruption when one secret prefixes another) with "<redacted>",
// strips non-printable characters to a space, collapses whitespace runs,
// and truncates to 300 chars.
func RedactProbeError(text string, headers map[string]string) string {
	secretSet := map[string]struct{}{}
	for name, value := range headers {
		if value == "" || !IsCredentialHeader(name) {
			continue
		}
		secretSet[value] = struct{}{}
		if strings.ToLower(name) == "authorization" {
			scheme, sep, credential := partition(value, " ")
			_ = scheme
			if sep != "" && credential != "" {
				secretSet[credential] = struct{}{}
			}
		}
	}
	secrets := make([]string, 0, len(secretSet))
	for s := range secretSet {
		secrets = append(secrets, s)
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })

	redacted := text
	for _, secret := range secrets {
		redacted = strings.ReplaceAll(redacted, secret, "<redacted>")
	}
	var b strings.Builder
	for _, r := range redacted {
		if isPrintable(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	collapsed := strings.Join(strings.Fields(b.String()), " ")
	if len(collapsed) > 300 {
		// Truncate on runes, not bytes, to avoid splitting a multi-byte
		// UTF-8 sequence (Python's [:300] truncates on code points).
		r := []rune(collapsed)
		if len(r) > 300 {
			return string(r[:300])
		}
	}
	return collapsed
}

// partition mirrors Python's str.partition(sep): returns (before, sep,
// after), with sep=="" and after=="" if sep is not found.
func partition(s, sep string) (string, string, string) {
	i := strings.Index(s, sep)
	if i < 0 {
		return s, "", ""
	}
	return s[:i], sep, s[i+len(sep):]
}

// isPrintable mirrors Python's str.isprintable() for a single character.
// Python treats categories Cc, Cf, Cs, Co, Cn, Zl, Zp, and Zs (except the
// ASCII space) as non-printable, which is exactly unicode.IsPrint's
// definition. Verified exhaustively over all code points against Python
// 3.14: no character is printable in Go but not in Python. The only
// mismatches are ~5.8k characters newly assigned in Unicode 16.0 (Python's
// tables) but absent from Go's 15.0 tables; Go treats them as unassigned
// and replaces them with a space, which is the conservative direction.
func isPrintable(r rune) bool {
	return unicode.IsPrint(r)
}

var sensitiveKeyFragments = []string{
	"authorization", "token", "secret", "password", "credential", "bearer",
	"api-key", "api_key", "apikey",
}

var sensitiveParamPattern = regexp.MustCompile(
	`(?i)([?&](?:token|secret|key|api_key|api-key|password|credential|bearer|pat)=)[^&]+`,
)

// RedactMCPEntry returns a display-safe copy of entry with credentials
// redacted, mirroring redact_mcp_entry's parent-key-aware recursive walk.
// entry may be nil.
func RedactMCPEntry(entry *OrderedObject) *OrderedObject {
	if entry == nil {
		return nil
	}
	result, _ := redactValue(entry, "", "").(*OrderedObject)
	return result
}

func redactValue(value any, key, parent string) any {
	switch x := value.(type) {
	case *OrderedObject:
		out := NewOrderedObject()
		for _, k := range x.Keys() {
			v, _ := x.Get(k)
			out.Set(k, redactValue(v, k, key))
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = redactValue(item, key, parent)
		}
		return out
	case string:
		return redactString(x, key, parent)
	default:
		return value
	}
}

func redactString(value, key, parent string) string {
	if (strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}")) ||
		(strings.HasPrefix(value, "{env:") && strings.HasSuffix(value, "}")) {
		return value
	}
	if parent == "env_http_headers" {
		return value
	}
	if parent == "headers" || parent == "http_headers" || strings.HasSuffix(parent, "headers") {
		return "<redacted>"
	}
	keyLower := strings.ToLower(key)
	sensitiveKey := false
	for _, frag := range sensitiveKeyFragments {
		if strings.Contains(keyLower, frag) {
			sensitiveKey = true
			break
		}
	}
	if !sensitiveKey {
		sensitiveKey = keyLower == "key" || keyLower == "pat" ||
			strings.HasSuffix(keyLower, "_key") || strings.HasSuffix(keyLower, "-key") ||
			strings.HasSuffix(keyLower, "_pat") || strings.HasSuffix(keyLower, "-pat")
	}
	if sensitiveKey {
		return "<redacted>"
	}
	if strings.Contains(value, "?") && strings.Contains(value, "=") {
		return sensitiveParamPattern.ReplaceAllString(value, "${1}<redacted>")
	}
	return value
}

var urlPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

// URLsInText mirrors _urls_in_text.
func URLsInText(text string) []string {
	matches := urlPattern.FindAllString(text, -1)
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = strings.TrimRight(m, ").,;]")
	}
	return out
}

// IsAuthorizationURL mirrors _is_authorization_url.
func IsAuthorizationURL(rawURL string) bool {
	if hasSensitiveParameters(rawURL) {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	location := strings.ToLower(u.Host + u.Path)
	params := u.Query()
	if strings.Contains(location, "authorize") || strings.Contains(location, "oauth") {
		return true
	}
	_, hasClientID := params["client_id"]
	_, hasRedirectURI := params["redirect_uri"]
	return hasClientID && hasRedirectURI
}

// RedactSensitiveURLs mirrors _redact_sensitive_urls: replaces any URL
// carrying sensitive query parameters with a literal placeholder.
func RedactSensitiveURLs(text string) string {
	return urlPattern.ReplaceAllStringFunc(text, func(m string) string {
		if hasSensitiveParameters(m) {
			return "[REDACTED CALLBACK URL]"
		}
		return m
	})
}
