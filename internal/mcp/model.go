package mcp

import (
	"encoding/base64"
	"fmt"
	"os"
)

// MCPConfigError mirrors Python's MCPConfigError: an MCP definition or
// target config cannot be safely managed.
type MCPConfigError struct{ Message string }

func (e *MCPConfigError) Error() string { return e.Message }

func configErrorf(format string, args ...any) error {
	return &MCPConfigError{Message: fmt.Sprintf(format, args...)}
}

// Constants mirroring model.py's module-level constants.
const (
	StateVersion           = 1
	DefaultMCPsDir         = "mcps"
	DefaultAgentsConfigDir = "agents"
	StateFile              = ".local/state/aikito/mcp-state.json"
	BackupDir              = ".local/state/aikito/backups"
	// LegacyPlaceholderToken is the sentinel credential value a pre-env-var
	// agy entry may still carry; _entry_matches_desired treats an entry
	// whose decoded Basic-auth token equals this literal as NOT matching,
	// forcing a real re-sync once the real env var is set.
	LegacyPlaceholderToken = "placeholder-token-set-environment-variable"
)

// AgentSpec is one (agent, server) pairing resolved from a workspace's
// mcps/*.toml definitions plus the agent registry. Mirrors model.py's
// AgentSpec exactly, including the Adapter defaulting rule.
type AgentSpec struct {
	Agent                string
	Server               string
	ConfigPath           string
	ConfigFormat         string // e.g. "toml", "claude_json", or the "unsupported" sentinel
	TargetName           string // post name_style-transform server name, or override
	Desired              *OrderedObject
	Enabled              bool
	Reason               string
	LiveCommand          []string
	AuthCommand          []string
	ContainsSecret       bool
	MissingCredentialEnv string
	Home                 string
	// Adapter is the semantic adapter key; defaults to ConfigFormat when
	// empty (model.py's __post_init__). The only built-in case where it
	// legitimately differs is Grok: ConfigFormat="toml", Adapter="grok_toml".
	Adapter string
}

// NewAgentSpec mirrors AgentSpec's __post_init__: constructs a spec with
// Enabled defaulting true and Adapter defaulting to ConfigFormat when empty.
// Use this instead of a bare struct literal so the defaulting rule can't be
// silently skipped at a call site.
func NewAgentSpec(spec AgentSpec) AgentSpec {
	if spec.Adapter == "" {
		spec.Adapter = spec.ConfigFormat
	}
	return spec
}

// StateKey mirrors AgentSpec.state_key.
func (s AgentSpec) StateKey() string { return s.Agent + ":" + s.Server }

// BasicTokenAuth keeps credential policy canonical while resolving secrets
// only at runtime (model.py's BasicTokenAuth).
type BasicTokenAuth struct {
	AccountEmail     string
	TokenEnv         string
	AuthorizationEnv string
}

// AuthorizationHeader reads TokenEnv from the environment and returns
// "Basic <base64(account_email:token)>", or an error if the env var is
// empty/unset.
func (a BasicTokenAuth) AuthorizationHeader() (string, error) {
	token := os.Getenv(a.TokenEnv)
	if token == "" {
		return "", configErrorf("Required MCP credential environment variable is missing: %s", a.TokenEnv)
	}
	credentials := a.AccountEmail + ":" + token
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(credentials)), nil
}

// LiveMCPResult is the result of one agent CLI's live MCP status command.
type LiveMCPResult struct {
	Agent      string
	Command    []string
	Status     string
	ReturnCode *int
	Output     string
}

// MCPToolProbeResult is the read-only result of discovering one agent's
// tools for one MCP server.
type MCPToolProbeResult struct {
	Agent      string
	Status     string
	AuthMethod string
	ToolNames  []string
	Error      string
}

// MCPObservedEntry is the observed runtime state of an MCP server entry in
// an agent config file. rawEntry is deliberately unexported: Entry() (the
// only way another package can read this type's entry) always redacts;
// internal comparison/fingerprinting code within this package uses
// RawEntry() directly. Preserve this split when extending the type — it is
// the main safeguard against ever displaying/logging a raw credential.
type MCPObservedEntry struct {
	Target             MCPConfigTarget
	Exists             bool
	Fingerprint        *string
	ManagedFingerprint *string
	IsManaged          bool
	rawEntry           *OrderedObject
}

func NewMCPObservedEntry(target MCPConfigTarget, exists bool, fingerprint, managedFingerprint *string, isManaged bool, rawEntry *OrderedObject) MCPObservedEntry {
	return MCPObservedEntry{target, exists, fingerprint, managedFingerprint, isManaged, rawEntry}
}

// Entry returns a display-safe (redacted) copy of the observed entry, or
// nil if there is none.
func (e MCPObservedEntry) Entry() *OrderedObject {
	if e.rawEntry == nil {
		return nil
	}
	return RedactMCPEntry(e.rawEntry)
}

// RawEntry returns the raw, unredacted entry for internal use only (never
// log or display this directly).
func (e MCPObservedEntry) RawEntry() *OrderedObject { return e.rawEntry }

// MCPDesiredEntry is the desired configuration state of an MCP server, with
// the same raw/display split as MCPObservedEntry.
type MCPDesiredEntry struct {
	Target               MCPConfigTarget
	Fingerprint          *string
	ContainsSecret       bool
	MissingCredentialEnv string
	LiveCommand          []string
	AuthCommand          []string
	rawDesired           *OrderedObject
}

func NewMCPDesiredEntry(target MCPConfigTarget, fingerprint *string, containsSecret bool, missingCredentialEnv string, liveCommand, authCommand []string, rawDesired *OrderedObject) MCPDesiredEntry {
	return MCPDesiredEntry{target, fingerprint, containsSecret, missingCredentialEnv, liveCommand, authCommand, rawDesired}
}

func (e MCPDesiredEntry) Desired() *OrderedObject {
	if e.rawDesired == nil {
		return nil
	}
	return RedactMCPEntry(e.rawDesired)
}

func (e MCPDesiredEntry) RawDesired() *OrderedObject { return e.rawDesired }

// MCPConfigTarget is a placeholder for the generic ConfigTarget type
// (config_runtime.py), not yet ported in this Go build (owned by a
// different fork's territory per the architecture research). It carries
// just enough fields for this package's own use (physical path + the
// post-name_style-transform target server name); extend/replace once the
// real ConfigTarget/FileSnapshot framework lands.
type MCPConfigTarget struct {
	Agent           string
	Server          string
	Path            string
	LogicalIdentity string
	TargetName      string
}
