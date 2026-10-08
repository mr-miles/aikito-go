package mcp

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

func targetName(nameStyle, serverName string) string {
	if nameStyle == "underscore" {
		return strings.ReplaceAll(serverName, "-", "_")
	}
	return serverName
}

func renderCommand(template []string, target string) []string {
	out := make([]string, len(template))
	for i, part := range template {
		out[i] = strings.ReplaceAll(part, "{target}", target)
	}
	return out
}

// tomlMapToOrdered converts a go-toml-decoded map[string]any into an
// *OrderedObject. go-toml/v2 decodes tables into a plain Go map, which has
// no deterministic iteration order — unlike Python's tomllib, which (via
// ordinary dict insertion-order preservation) keeps each table's keys in
// the order they appeared in the source file. This sorts keys
// alphabetically instead: deterministic (no test flakiness from Go map
// iteration order) but NOT guaranteed to byte-match Python's source-order
// rendering of multi-header override/headers tables. Revisit with an
// order-preserving TOML decoder if exact byte parity for multi-key
// headers/overrides tables becomes a real requirement.
func tomlMapToOrdered(m map[string]any) *OrderedObject {
	if m == nil {
		return NewOrderedObject()
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := NewOrderedObject()
	for _, k := range keys {
		out.Set(k, tomlValueToOrdered(m[k]))
	}
	return out
}

func tomlValueToOrdered(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return tomlMapToOrdered(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = tomlValueToOrdered(item)
		}
		return out
	case int:
		return int64(x)
	default:
		return v
	}
}

func loadBasicTokenAuth(serverName string, server map[string]any) (*BasicTokenAuth, error) {
	authVal, ok := server["authentication"]
	if !ok || authVal == nil {
		return nil, nil
	}
	auth, ok := authVal.(map[string]any)
	if !ok {
		return nil, configErrorf("Server '%s' authentication must be a table", serverName)
	}
	method, _ := auth["method"].(string)
	if method != "basic_api_token" {
		return nil, configErrorf("Server '%s' has unsupported authentication method: %v", serverName, auth["method"])
	}
	fields := map[string]string{}
	for _, field := range []string{"account_email", "token_env", "authorization_env"} {
		v, _ := auth[field].(string)
		if v == "" {
			return nil, configErrorf("Server '%s' authentication requires '%s'", serverName, field)
		}
		fields[field] = v
	}
	return &BasicTokenAuth{AccountEmail: fields["account_email"], TokenEnv: fields["token_env"], AuthorizationEnv: fields["authorization_env"]}, nil
}

// LoadAgentSpecs mirrors load_agent_specs: parses every workspace
// mcps/*.toml file (sorted by filename for determinism; a file is either
// one server definition with the filename stem as the server name, or a
// `[servers]` table of several named server sub-tables — both shapes may
// coexist across different files, with no cross-file collision check at
// this layer), resolves each (server, agent) pairing against the agent
// registry, and returns one AgentSpec per pairing.
func LoadAgentSpecs(aikitoDir, home string) ([]AgentSpec, error) {
	mcpsDir := filepath.Join(aikitoDir, DefaultMCPsDir)
	info, err := os.Stat(mcpsDir)
	if os.IsNotExist(err) {
		return nil, configErrorf("MCP config directory not found: %s", mcpsDir)
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, configErrorf("MCP config path is not a directory: %s", mcpsDir)
	}

	entries, err := os.ReadDir(mcpsDir)
	if err != nil {
		return nil, err
	}
	var filenames []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".toml") && !e.IsDir() {
			filenames = append(filenames, e.Name())
		}
	}
	sort.Strings(filenames)

	servers := map[string]map[string]any{}
	var serverOrder []string
	for _, name := range filenames {
		path := filepath.Join(mcpsDir, name)
		stem := strings.TrimSuffix(name, ".toml")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, configErrorf("Invalid MCP config %s: %v", path, err)
		}
		document, err := workspace.DecodeTOML(data)
		if err != nil {
			return nil, configErrorf("Invalid MCP config %s: %v", path, err)
		}
		if serversVal, ok := document["servers"]; ok {
			if serversTable, ok := serversVal.(map[string]any); ok {
				names := make([]string, 0, len(serversTable))
				for n := range serversTable {
					names = append(names, n)
				}
				sort.Strings(names)
				for _, sName := range names {
					sVal, ok := serversTable[sName].(map[string]any)
					if !ok {
						return nil, configErrorf("Server '%s' in %s must be a table", sName, path)
					}
					if _, exists := servers[sName]; !exists {
						serverOrder = append(serverOrder, sName)
					}
					servers[sName] = sVal
				}
				continue
			}
		}
		if _, exists := servers[stem]; !exists {
			serverOrder = append(serverOrder, stem)
		}
		servers[stem] = document
	}

	defs, err := registry.LoadAgentDefinitions(aikitoDir, home)
	if err != nil {
		return nil, configErrorf("%v", err)
	}

	var specs []AgentSpec
	for _, serverName := range serverOrder {
		server := servers[serverName]
		if transport, _ := server["transport"].(string); transport != "remote" {
			return nil, configErrorf("Server '%s' must use remote transport", serverName)
		}
		url, _ := server["url"].(string)
		if url == "" {
			return nil, configErrorf("Server '%s' requires a URL", serverName)
		}
		agentsVal, _ := server["agents"].([]any)
		if agentsVal == nil {
			return nil, configErrorf("Server '%s' requires an agents list", serverName)
		}
		var agentNames []string
		for _, a := range agentsVal {
			s, ok := a.(string)
			if !ok {
				return nil, configErrorf("Server '%s' requires an agents list", serverName)
			}
			agentNames = append(agentNames, s)
		}
		overridesRaw, _ := server["overrides"].(map[string]any)
		authentication, err := loadBasicTokenAuth(serverName, server)
		if err != nil {
			return nil, err
		}
		var headers *OrderedObject
		if headersVal, ok := server["headers"]; ok && headersVal != nil {
			headersMap, ok := headersVal.(map[string]any)
			if !ok {
				return nil, configErrorf("Server '%s' headers must be a string-to-string table", serverName)
			}
			headers = NewOrderedObject()
			keys := make([]string, 0, len(headersMap))
			for k := range headersMap {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				v, ok := headersMap[k].(string)
				if !ok {
					return nil, configErrorf("Server '%s' headers must be a string-to-string table", serverName)
				}
				headers.Set(k, v)
			}
		}

		for _, agent := range agentNames {
			definition, ok := defs[agent]
			if !ok {
				return nil, configErrorf("Server '%s' references unknown agent '%s'; define it in %s", serverName, agent, DefaultAgentsConfigDir)
			}
			defCopy := definition

			var override map[string]any
			if overridesRaw != nil {
				if o, ok := overridesRaw[agent]; ok {
					override, ok = o.(map[string]any)
					if !ok {
						return nil, configErrorf("Server '%s' override for %s must be a table", serverName, agent)
					}
				}
			}
			overrideOrdered := tomlMapToOrdered(override)
			enabled := true
			if v, ok := overrideOrdered.Get("enabled"); ok {
				if b, ok := v.(bool); ok {
					enabled = b
				}
			}
			reason := ""
			if v, ok := overrideOrdered.Get("reason"); ok {
				reason = toPyStr(v)
			}

			capability := definition.MCP
			if capability == nil || !capability.IsSupported() {
				configPath := ""
				capReason := ""
				if capability != nil {
					configPath = capability.ConfigPath
					capReason = capability.Reason
				}
				finalReason := reason
				if finalReason == "" {
					finalReason = capReason
				}
				if finalReason == "" {
					finalReason = "MCP synchronization is not supported for agent '" + agent + "'"
				}
				specs = append(specs, NewAgentSpec(AgentSpec{
					Agent: agent, Server: serverName, ConfigPath: configPath,
					ConfigFormat: "unsupported", TargetName: serverName,
					Desired: NewOrderedObject(), Enabled: false, Reason: finalReason,
					Home: home, Definition: &defCopy,
				}))
				continue
			}

			defaultTarget := targetName(capability.NameStyle, serverName)
			targetNameVal := defaultTarget
			if v, ok := overrideOrdered.Get("name"); ok {
				targetNameVal = toPyStr(v)
			}
			desired, containsSecret, missingEnv, err := BuildDesiredPayload(capability.Adapter, url, overrideOrdered, authentication, headers, targetNameVal)
			if err != nil {
				return nil, err
			}
			var authCommand []string
			if authentication == nil {
				authCommand = renderCommand(capability.AuthCommand, targetNameVal)
			}
			specs = append(specs, NewAgentSpec(AgentSpec{
				Agent: agent, Server: serverName, ConfigPath: capability.ConfigPath,
				ConfigFormat: capability.ConfigFormat, TargetName: targetNameVal,
				Desired: desired, Enabled: enabled, Reason: reason,
				LiveCommand: renderCommand(capability.LiveCommand, targetNameVal), AuthCommand: authCommand,
				ContainsSecret: containsSecret, MissingCredentialEnv: missingEnv,
				Home: home, Definition: &defCopy, Adapter: capability.Adapter,
			}))
		}
	}
	return specs, nil
}

// AgentDetected mirrors _agent_detected: prefer the registry's own
// availability signal when a Definition is attached, falling back to "does
// the config file's parent directory already exist".
func AgentDetected(spec AgentSpec) bool {
	if spec.Definition != nil {
		installed := registry.IsAgentInstalled(spec.Definition.Agent, spec.Home, nil)
		if installed != nil {
			return *installed
		}
	}
	_, err := os.Stat(filepath.Dir(spec.ConfigPath))
	return err == nil
}
