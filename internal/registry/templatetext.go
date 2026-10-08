package registry

// BundledAgentTemplateText returns the raw, verbatim text of a bundled
// agent's templates/agents/<name>.toml file (not a re-serialization of its
// parsed spec) — this is what `aikito init workspace` writes into a fresh
// workspace's agents/<name>.toml, matching Python's render_workspace_files,
// which writes load_template(f"agents/{name}.toml") unmodified.
//
// This file intentionally adds a new accessor rather than editing
// templates.go, which already declares the embedded templatesFS this reads
// from (same package, so no export needed).
func BundledAgentTemplateText(name string) (string, error) {
	data, err := templatesFS.ReadFile("templates/agents/" + name + ".toml")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
