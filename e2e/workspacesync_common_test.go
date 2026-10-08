//go:build e2e || e2e_generate

package e2e

// Whole-workspace `aikito sync` scenarios, run through the project-sync
// harness (runProjectSyncScenario): captured from Python by
// generate_workspacesync_test.go, replayed against Go by
// workspacesync_test.go. Steps avoid anything that writes timestamped
// backups, so the home tree compares exactly.
var workspaceSyncScenarios = []psScenario{
	{"workspace", func(e *psEnv) {
		e.mkdir("p1", "p2", "p3")
		e.skill("gskill")
		e.skill("pskill")
		e.write("aikito/skills.toml", "skills = [\"aikito\", \"durable-memory\", \"gskill\"]\n")
		e.write("aikito/mcps/remote-s.toml", "transport = \"remote\"\nurl = \"https://example.com/mcp\"\nagents = [\"claude-code\"]\n")
		e.write("aikito/subagents/verifier.md", "---\ndescription: \"Verifies things\"\nagents: [\"claude-code\"]\n---\n# Verifier\n\nCheck the work.\n")
		e.run("", "init", "project", "p1", "{H}/p1")
		e.run("", "init", "project", "p2", "{H}/p2")
		e.run("", "init", "project", "p3", "{H}/p3")
		e.setSkills("p1", "link", "pskill")
		e.setSkills("p2", "copy", "pskill")
		e.remove("p3")
		e.write("aikito/projects/p4/agent.toml", "name = \"p4\"\nskills = []\n")
		e.run("", "sync", "--dry-run", "--verbose")
		e.run("", "sync")
		e.run("", "sync", "--verbose")
		e.appendTo("aikito/skills/pskill/SKILL.md", "canonical edit\n")
		e.run("", "sync", "--dry-run", "--verbose")
		e.run("", "sync")
		e.appendTo("p2/.agents/skills/pskill/SKILL.md", "hand edit\n")
		e.appendTo("aikito/skills/pskill/SKILL.md", "another canonical edit\n")
		e.run("", "sync", "--verbose")
		e.run("", "sync", "--dry-run", "global")
		e.run("", "sync", "extra")
	}},
	{"conflicts", func(e *psEnv) {
		e.write(".claude/CLAUDE.md", "# mine\n")
		e.write(".claude/skills/mine/SKILL.md", "hi\n")
		e.run("", "sync", "--dry-run")
		e.run("", "sync", "--verbose")
		e.remove(".claude/skills")
		e.run("", "sync")
		e.remove(".claude/CLAUDE.md")
		e.run("", "sync")
		e.run("", "sync")
	}},
}
