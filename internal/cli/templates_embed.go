package cli

import "embed"

// templatesFS embeds the workspace/project init templates and the two
// bundled skill snapshots (aikito, durable-memory), copied verbatim from
// the Python package's src/aikito/templates/ (not the agents/ fragments —
// those are already embedded in internal/registry/templates/agents/).
//
//go:embed templates/config.toml templates/skills.toml templates/gitignore templates/global/AGENTS.md templates/project/AGENTS.md templates/skills
var templatesFS embed.FS

func loadTemplate(name string) (string, error) {
	data, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
