package registry

import (
	"embed"
	"fmt"

	"github.com/mr-miles/aikito-go/internal/workspace"
)

//go:embed templates/agents/*.toml
var templatesFS embed.FS

// bundledSpecs holds the raw [agents.<name>] spec table for each of the 8
// built-in agent templates, loaded once at package init. A failure here is
// a packaging bug (the embedded assets are fixed at build time), mirroring
// Python's own unconditional bundled_agent_spec read plus its
// verify_templates() startup self-check — so it panics rather than
// threading an error through every call site.
var bundledSpecs = loadBundledSpecs()

func loadBundledSpecs() map[string]map[string]any {
	cache := map[string]map[string]any{}
	for _, name := range BuiltinAgents {
		data, err := templatesFS.ReadFile("templates/agents/" + name + ".toml")
		if err != nil {
			panic(fmt.Sprintf("registry: missing bundled agent template %q: %v", name, err))
		}
		doc, err := workspace.DecodeTOML(data)
		if err != nil {
			panic(fmt.Sprintf("registry: invalid bundled agent template %q: %v", name, err))
		}
		agentsTable, ok := doc["agents"].(map[string]any)
		if !ok {
			panic(fmt.Sprintf("registry: bundled agent template %q missing [agents] table", name))
		}
		spec, ok := agentsTable[name].(map[string]any)
		if !ok {
			panic(fmt.Sprintf("registry: bundled agent template %q missing [agents.%s] table", name, name))
		}
		cache[name] = spec
	}
	return cache
}

// BundledAgentSpec reads a built-in agent's bundled defaults, without
// merging any workspace capability declarations. Returns nil for a name
// that is not one of BuiltinAgents (matching Python's bundled_agent_spec
// returning {} for unknown names — nil indexes safely as "key absent" at
// every call site in this package).
func BundledAgentSpec(name string) map[string]any {
	return bundledSpecs[name]
}

// BundledAgent builds the base Agent identity for name purely from its
// bundled template, or — for a name outside BuiltinAgents — a minimal Agent
// with no paths/detect/capabilities, matching Python's bundled_agent, which
// never errors: bundled_agent_spec returns {} for an unknown name and an
// empty spec table builds a valid (if empty) Agent.
func BundledAgent(name, home string) (Agent, error) {
	return buildAgentFromSpec(name, BundledAgentSpec(name), home)
}
