package mcp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"strings"
)

// Fingerprint mirrors _fingerprint: sha256 hex of value rendered as compact,
// sorted-key, ensure_ascii=False JSON.
func Fingerprint(value *OrderedObject) string {
	sum := sha256.Sum256([]byte(DumpCompactSorted(value)))
	return hex.EncodeToString(sum[:])
}

// --- BuildDesired per format (adapters/__init__.py:112-267) ---

func buildTOML(url string, override *OrderedObject, auth *BasicTokenAuth, headers *OrderedObject, serverName string) (*OrderedObject, bool, string) {
	desired := OO("url", url)
	if auth != nil {
		desired.Set("env_http_headers", OO("Authorization", auth.AuthorizationEnv))
	} else if headers.Len() > 0 {
		staticHeaders := NewOrderedObject()
		envHeaders := NewOrderedObject()
		for _, k := range headers.Keys() {
			v, _ := headers.Get(k)
			vs, _ := v.(string)
			if ref := EnvironmentReference(vs); ref != "" {
				envHeaders.Set(k, ref)
			} else {
				staticHeaders.Set(k, vs)
			}
		}
		if staticHeaders.Len() > 0 {
			desired.Set("headers", staticHeaders)
		}
		if envHeaders.Len() > 0 {
			desired.Set("env_http_headers", envHeaders)
		}
	}
	return desired, false, ""
}

func buildGrokTOML(url string, override *OrderedObject, auth *BasicTokenAuth, headers *OrderedObject, serverName string) (*OrderedObject, bool, string) {
	desired := OO("url", url)
	if auth != nil {
		desired.Set("headers", OO("Authorization", "${"+auth.AuthorizationEnv+"}"))
	} else if headers.Len() > 0 {
		desired.Set("headers", headers)
	}
	return desired, false, ""
}

func buildJSONC(url string, override *OrderedObject, auth *BasicTokenAuth, headers *OrderedObject, serverName string) (*OrderedObject, bool, string) {
	timeout := override.GetOr("timeout", int64(30000))
	desired := OO("type", "remote", "url", url, "enabled", true, "timeout", timeout)
	if auth != nil {
		desired.Set("oauth", false)
		desired.Set("headers", OO("Authorization", "{env:"+auth.AuthorizationEnv+"}"))
	} else if headers.Len() > 0 {
		h := NewOrderedObject()
		for _, k := range headers.Keys() {
			v, _ := headers.Get(k)
			vs, _ := v.(string)
			if ref := EnvironmentReference(vs); ref != "" {
				h.Set(k, "{env:"+ref+"}")
			} else {
				h.Set(k, vs)
			}
		}
		desired.Set("headers", h)
	}
	return desired, false, ""
}

func buildAgyJSON(url string, override *OrderedObject, auth *BasicTokenAuth, headers *OrderedObject, serverName string) (*OrderedObject, bool, string) {
	desired := OO("serverUrl", url)
	if auth != nil {
		if os.Getenv(auth.TokenEnv) != "" {
			header, _ := auth.AuthorizationHeader()
			desired.Set("headers", OO("Authorization", header))
			return desired, true, ""
		}
		return desired, true, auth.TokenEnv
	}
	if headers.Len() > 0 {
		resolvedHeaders := NewOrderedObject()
		missingEnv := ""
		hasSecret := false
		for _, k := range headers.Keys() {
			v, _ := headers.Get(k)
			vs, _ := v.(string)
			if ref := EnvironmentReference(vs); ref != "" {
				hasSecret = true
				envVal := os.Getenv(ref)
				if envVal != "" {
					resolvedHeaders.Set(k, envVal)
				} else {
					missingEnv = ref
				}
			} else {
				resolvedHeaders.Set(k, vs)
			}
		}
		if missingEnv != "" {
			return desired, true, missingEnv
		}
		desired.Set("headers", resolvedHeaders)
		return desired, hasSecret, ""
	}
	return desired, false, ""
}

func buildClaudeJSON(url string, override *OrderedObject, auth *BasicTokenAuth, headers *OrderedObject, serverName string) (*OrderedObject, bool, string) {
	desired := OO("type", "http", "url", url)
	if auth != nil {
		desired.Set("headers", OO("Authorization", "${"+auth.AuthorizationEnv+"}"))
	} else if headers.Len() > 0 {
		h := NewOrderedObject()
		for _, k := range headers.Keys() {
			v, _ := headers.Get(k)
			vs, _ := v.(string)
			if ref := EnvironmentReference(vs); ref != "" {
				h.Set(k, "${"+ref+"}")
			} else {
				h.Set(k, vs)
			}
		}
		desired.Set("headers", h)
	}
	return desired, false, ""
}

func buildCopilotJSON(url string, override *OrderedObject, auth *BasicTokenAuth, headers *OrderedObject, serverName string) (*OrderedObject, bool, string) {
	desired := OO("type", "http", "url", url, "tools", []any{"*"})
	if headers.Len() > 0 {
		h := NewOrderedObject()
		for _, k := range headers.Keys() {
			v, _ := headers.Get(k)
			vs, _ := v.(string)
			if ref := EnvironmentReference(vs); ref != "" {
				h.Set(k, "${"+ref+"}")
			} else {
				h.Set(k, vs)
			}
		}
		desired.Set("headers", h)
	} else if auth != nil {
		desired.Set("headers", OO("Authorization", "${"+auth.AuthorizationEnv+"}"))
	} else {
		desired.Set("headers", NewOrderedObject())
	}
	return desired, false, ""
}

func buildDSHCordis(url string, override *OrderedObject, auth *BasicTokenAuth, headers *OrderedObject, serverName string) (*OrderedObject, bool, string) {
	transport := override.GetOr("transport", "streamable-http")
	nameOverride := override.GetOr("name", serverName)
	desired := OO("serverName", toPyStr(nameOverride), "transport", transport, "url", url)
	if headers.Len() > 0 {
		h := NewOrderedObject()
		for _, k := range headers.Keys() {
			v, _ := headers.Get(k)
			vs, _ := v.(string)
			if ref := EnvironmentReference(vs); ref != "" {
				h.Set(k, "!!js process.env."+ref)
			} else {
				h.Set(k, vs)
			}
		}
		desired.Set("headers", h)
	} else if auth != nil {
		desired.Set("headers", OO("Authorization", "!!js process.env."+auth.AuthorizationEnv))
	}
	if override.Has("timeout") {
		timeout, _ := override.Get("timeout")
		desired.Set("toolCallTimeoutMs", timeout)
	}
	return desired, false, ""
}

func importVerbatim(entry *OrderedObject) *OrderedObject { return entry.Clone() }

// importCopilotJSON mirrors _import_copilot_json: local Copilot servers
// have no canonical remote equivalent, so only an http entry with a string
// url is adoptable.
func importCopilotJSON(entry *OrderedObject) *OrderedObject {
	typeVal := entry.GetOr("type", "http")
	typeStr, isStr := typeVal.(string)
	if !isStr || typeStr != "http" {
		return nil
	}
	if urlVal, _ := entry.Get("url"); func() bool { _, ok := urlVal.(string); return !ok }() {
		return nil
	}
	result := entry.Clone()
	result.Set("transport", "remote")
	return result
}

// Adapter mirrors adapters/__init__.py's MCPAdapter dataclass.
type Adapter struct {
	BuildDesired        func(url string, override *OrderedObject, auth *BasicTokenAuth, headers *OrderedObject, serverName string) (*OrderedObject, bool, string)
	ReadEntry           func(text, serverName string) (*OrderedObject, error)
	UpdateEntry         func(text, serverName string, desired *OrderedObject) (string, error)
	RemoveEntry         func(text, serverName string) (string, error)
	DocumentFormat      string
	ServerCollectionKey string
	SyntaxName          string
	MaterializesSecrets bool
	ImportEntry         func(entry *OrderedObject) *OrderedObject // nil = not adoptable; a nil result skips an entry
}

// ReadAllEntries mirrors MCPAdapter.read_all_entries: non-dict-valued
// collection entries are silently skipped, not errored.
func (a Adapter) ReadAllEntries(text string) (*OrderedObject, error) {
	doc, err := LoadDocument(a.DocumentFormat, text)
	if err != nil {
		return nil, err
	}
	root, _ := doc.(*OrderedObject)
	serversVal := root.GetOr(a.ServerCollectionKey, NewOrderedObject())
	servers, ok := serversVal.(*OrderedObject)
	if !ok {
		return nil, configErrorf("Agent MCP server collection must be an object")
	}
	out := NewOrderedObject()
	for _, k := range servers.Keys() {
		v, _ := servers.Get(k)
		if obj, ok := v.(*OrderedObject); ok {
			out.Set(k, obj)
		}
	}
	return out, nil
}

// MCPAdapters mirrors MCP_ADAPTERS: the dispatch table from config_format
// key to its adapter.
var MCPAdapters = map[string]Adapter{
	"toml": {
		buildTOML, GetTOMLServer, UpdateTOMLServer, RemoveTOMLServer,
		"toml", "mcp_servers", "TOML", false, importVerbatim,
	},
	"grok_toml": {
		buildGrokTOML, GetTOMLServer, UpdateTOMLServer, RemoveTOMLServer,
		"toml", "mcp_servers", "TOML", false, nil,
	},
	"jsonc": {
		buildJSONC, GetJSONCServer, UpdateJSONCServer, RemoveJSONCServer,
		"jsonc", "mcp", "JSONC", false, nil,
	},
	"agy_json": {
		buildAgyJSON, GetAgyJSONServer, UpdateAgyJSONServer, RemoveAgyJSONServer,
		"agy_json", "mcpServers", "JSON", true, nil,
	},
	"claude_json": {
		buildClaudeJSON, GetClaudeJSONServer, UpdateClaudeJSONServer, RemoveClaudeJSONServer,
		"claude_json", "mcpServers", "JSON", true, importVerbatim,
	},
	"copilot_json": {
		buildCopilotJSON, GetCopilotJSONServer, UpdateCopilotJSONServer, RemoveCopilotJSONServer,
		"copilot_json", "mcpServers", "JSON", false, importCopilotJSON,
	},
	"dsh_cordis": {
		buildDSHCordis, GetDSHCordisServer, UpdateDSHCordisServer, RemoveDSHCordisServer,
		"dsh_cordis", "mcpServers", "Cordis patch YAML", false, nil,
	},
}

// GetMCPAdapter mirrors get_mcp_adapter.
func GetMCPAdapter(configFormat string) (Adapter, error) {
	a, ok := MCPAdapters[configFormat]
	if !ok {
		return Adapter{}, configErrorf("Unsupported config format: %s", configFormat)
	}
	return a, nil
}

// BuildDesiredPayload mirrors _build_desired.
func BuildDesiredPayload(configFormat, url string, override *OrderedObject, auth *BasicTokenAuth, headers *OrderedObject, serverName string) (*OrderedObject, bool, string, error) {
	if override == nil {
		override = NewOrderedObject()
	}
	if headers == nil {
		headers = NewOrderedObject()
	}
	a, err := GetMCPAdapter(configFormat)
	if err != nil {
		return nil, false, "", err
	}
	desired, secret, missing := a.BuildDesired(url, override, auth, headers, serverName)
	return desired, secret, missing, nil
}

// ReadEntry mirrors read_entry.
func ReadEntry(spec AgentSpec, text string) (*OrderedObject, error) {
	a, err := GetMCPAdapter(spec.Adapter)
	if err != nil {
		return nil, err
	}
	return a.ReadEntry(text, spec.TargetName)
}

// ReadAllEntries mirrors read_all_entries (module-level).
func ReadAllEntries(adapter, text string) (*OrderedObject, error) {
	a, err := GetMCPAdapter(adapter)
	if err != nil {
		return nil, err
	}
	return a.ReadAllEntries(text)
}

// EntryMatchesDesired mirrors _entry_matches_desired exactly, including the
// agy-basic-auth-with-missing-env-var special case: an entry whose
// Authorization header decodes to "...:<LegacyPlaceholderToken>" is treated
// as NOT matching (forcing a real re-sync once the env var is set), while
// any other entry that's otherwise identical modulo the one unresolvable
// secret header is treated as still matching.
func EntryMatchesDesired(spec AgentSpec, current *OrderedObject) bool {
	if current == nil {
		return false
	}
	if spec.MissingCredentialEnv == "" {
		return JSONEqual(current, spec.Desired)
	}

	headersVal, _ := current.Get("headers")
	headers, _ := headersVal.(*OrderedObject)
	var authorization string
	if headers != nil {
		if v, ok := headers.Get("Authorization"); ok {
			authorization, _ = v.(string)
		}
	}
	if !strings.HasPrefix(authorization, "Basic ") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authorization, "Basic "))
	if err != nil {
		return false
	}
	decodedStr := string(decoded)
	if strings.HasSuffix(decodedStr, ":"+LegacyPlaceholderToken) {
		return false
	}
	withoutHeaders := current.Clone()
	withoutHeaders.Delete("headers")
	return JSONEqual(withoutHeaders, spec.Desired)
}

// UpdateEntry mirrors _update_entry.
func UpdateEntry(spec AgentSpec, text string) (string, error) {
	a, err := GetMCPAdapter(spec.Adapter)
	if err != nil {
		return "", err
	}
	return a.UpdateEntry(text, spec.TargetName, spec.Desired)
}

// RemoveEntry mirrors _remove_entry.
func RemoveEntry(spec AgentSpec, text string) (string, error) {
	a, err := GetMCPAdapter(spec.Adapter)
	if err != nil {
		return "", err
	}
	return a.RemoveEntry(text, spec.TargetName)
}
