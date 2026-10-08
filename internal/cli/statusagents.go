package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mr-miles/aikito-go/internal/linkplan"
	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// agentStatusRow ports render.py's AgentStatusRow.
type agentStatusRow struct {
	AgentName, DisplayName, ConsumerName string
	InstructionsStatus, SkillsStatus     string
	SkillsLinkDepth                      int // 0 = None
	MCPStatus, SubagentStatus            string
}

// globalSkillsList ports status.py _get_skills_list. A warning goes to
// stderr when skills.toml can't be read.
func globalSkillsList(aikitoDir string, warn func(string)) []string {
	path := filepath.Join(aikitoDir, "skills.toml")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	var doc map[string]any
	if err == nil {
		doc, err = workspace.DecodeTOML(data)
	}
	if err != nil {
		if warn != nil {
			warn(fmt.Sprintf("[WARN] Failed to read global skills configuration: %v", err))
		}
		return nil
	}
	list, ok := doc["skills"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, pyStr(v))
	}
	return out
}

func summarizeSubagentStatus(statuses []string) string {
	total := len(statuses)
	okCount, has := 0, map[string]bool{}
	for _, s := range statuses {
		if s == linkplan.StatusOK {
			okCount++
		}
		has[s] = true
	}
	switch {
	case okCount == total:
		return fmt.Sprintf("OK (%d)", total)
	case has[linkplan.StatusError]:
		return fmt.Sprintf("ERROR (%d/%d)", okCount, total)
	case has[linkplan.StatusConflict]:
		return fmt.Sprintf("CONFLICT (%d/%d)", okCount, total)
	case has[linkplan.StatusUpdate] || has[linkplan.StatusDrift]:
		return fmt.Sprintf("DRIFT (%d/%d)", okCount, total)
	case has[linkplan.StatusMissing]:
		return fmt.Sprintf("MISSING (%d/%d)", okCount, total)
	}
	return fmt.Sprintf("CONFLICT (%d/%d)", okCount, total)
}

func consumerDisplayName(a registry.Agent) string {
	if a.Detect != nil && len(a.Detect.Commands) > 0 {
		return a.Detect.Commands[0]
	}
	return a.Name
}

// collectAgentStatusRows ports status.py collect_agent_status_rows. It
// returns the rows, the agent issue count, and the totals of active
// subagents and enabled MCP servers.
func collectAgentStatusRows(c *inspectionContext, warn func(string)) ([]agentStatusRow, int, int, int, error) {
	reg, defs, err := c.agents()
	if err != nil {
		return nil, 0, 0, 0, err
	}
	instrTargetStatus := map[string]string{}
	if _, views, ierr := c.instructionPlan(); ierr == nil {
		for _, v := range views {
			if v.TargetPath != "" {
				instrTargetStatus[v.TargetPath] = v.Status
			}
		}
	}
	globalSkills := globalSkillsList(c.aikitoDir, warn)
	totalGlobalSkills := len(globalSkills)
	skillViews, _ := c.skillViews(globalSkills)
	var containerView *linkplan.View
	for i := range skillViews {
		if skillViews[i].ResourceType == "global_skill_container" {
			containerView = &skillViews[i]
			break
		}
	}

	agentIssues := 0
	specs, merr := c.mcp()
	if merr != nil {
		specs = nil
		agentIssues++
	}
	subViews, subConfigs, serr := c.subagentViews()
	if serr != nil {
		subViews, subConfigs = nil, map[string]bool{}
		agentIssues++
	}

	enabled := map[string]bool{}
	for _, s := range specs {
		if s.Enabled {
			enabled[s.Server] = true
		}
	}
	active := map[string]bool{}
	for _, v := range subViews {
		if v.Status != linkplan.StatusSkip && v.Status != linkplan.StatusOrphan {
			active[v.ResourceName] = true
		}
	}

	shared := filepath.Join(c.home, ".agents", "skills")
	var rows []agentStatusRow
	for _, agent := range reg.Values() {
		name := agent.Name
		def := defs[name]
		if registry.CheckAgentAvailabilityForAgent(agent, c.home, nil).IsNotInstalled() {
			continue
		}

		instructions := "SKIP"
		if agent.InstructionPath != nil {
			if st, ok := instrTargetStatus[*agent.InstructionPath]; ok {
				instructions = st
			}
			if instructions != "OK" && instructions != "SKIP" {
				agentIssues++
			}
		}

		skills := "SKIP"
		if agent.SkillsPath != nil {
			var consumer *linkplan.View
			for i := range skillViews {
				if skillViews[i].ResourceType == "global_skill_consumer" && skillViews[i].TargetPath == *agent.SkillsPath {
					consumer = &skillViews[i]
					break
				}
			}
			conflict := "CONFLICT"
			if totalGlobalSkills > 0 {
				conflict = fmt.Sprintf("CONFLICT (0/%d)", totalGlobalSkills)
			}
			switch {
			case consumer == nil || consumer.Status == linkplan.StatusSkip:
			case consumer.Status == linkplan.StatusMissing:
				skills = "MISSING"
				agentIssues++
			case consumer.Status == linkplan.StatusConflict:
				skills = conflict
				agentIssues++
			case containerView != nil && containerView.Status == linkplan.StatusConflict:
				skills = conflict
				agentIssues++
			case containerView != nil && containerView.Status == linkplan.StatusMissing:
				skills = "MISSING"
				agentIssues++
			default:
				okSkills := 0
				for _, v := range skillViews {
					if v.ResourceType == "global_skill_entry" && v.Status == linkplan.StatusOK {
						okSkills++
					}
				}
				switch {
				case okSkills == totalGlobalSkills && totalGlobalSkills > 0:
					skills = fmt.Sprintf("OK (%d)", totalGlobalSkills)
				case totalGlobalSkills > 0:
					skills = fmt.Sprintf("CONFLICT (%d/%d)", okSkills, totalGlobalSkills)
					agentIssues++
				default:
					skills = "OK (0)"
				}
			}
		}

		mcpStatus := "SKIP"
		if def.MCP != nil && def.MCP.IsSupported() {
			var agentSpecs []int
			for i, s := range specs {
				if s.Agent == name {
					agentSpecs = append(agentSpecs, i)
				}
			}
			if len(agentSpecs) == 0 {
				mcpStatus = "OK (0)"
			} else {
				total := len(agentSpecs)
				okN, skipN := 0, 0
				drift, missing, hasErr := false, false, false
				for _, i := range agentSpecs {
					switch c.mcpStatus(specs[i]) {
					case "OK":
						okN++
					case "SKIP":
						skipN++
					case "DRIFT", "UPDATE":
						drift = true
					case "MISSING":
						missing = true
					case "ERROR":
						hasErr = true
					}
				}
				switch {
				case skipN == total:
					mcpStatus = "SKIP"
				case okN+skipN == total:
					mcpStatus = fmt.Sprintf("OK (%d)", okN)
				case hasErr:
					mcpStatus = fmt.Sprintf("ERROR (%d/%d)", okN, total)
					agentIssues++
				case drift:
					mcpStatus = fmt.Sprintf("DRIFT (%d/%d)", okN, total)
					agentIssues++
				case missing:
					mcpStatus = fmt.Sprintf("MISSING (%d/%d)", okN, total)
					agentIssues++
				default:
					mcpStatus = fmt.Sprintf("CONFLICT (%d/%d)", okN, total)
					agentIssues++
				}
			}
		}

		subStatus := "SKIP"
		var activeStatuses []string
		for _, v := range subViews {
			if (v.Agent == name || v.Agent == agent.DisplayName) && v.Status != linkplan.StatusSkip && v.Status != linkplan.StatusOrphan {
				activeStatuses = append(activeStatuses, v.Status)
			}
		}
		if len(activeStatuses) > 0 {
			subStatus = summarizeSubagentStatus(activeStatuses)
			if len(subStatus) < 2 || subStatus[:2] != "OK" {
				agentIssues++
			}
		} else if subConfigs[name] {
			subStatus = "OK (0)"
		}

		depth := 0
		if agent.SkillsPath != nil {
			depth = 2
			if *agent.SkillsPath == shared {
				depth = 1
			}
		}
		rows = append(rows, agentStatusRow{
			AgentName: name, DisplayName: agent.DisplayName, ConsumerName: consumerDisplayName(agent),
			InstructionsStatus: instructions, SkillsStatus: skills, SkillsLinkDepth: depth,
			MCPStatus: mcpStatus, SubagentStatus: subStatus,
		})
	}
	return rows, agentIssues, len(active), len(enabled), nil
}

// statusConsumers is the sorted, de-duplicated consumer label list.
func statusConsumers(rows []agentStatusRow) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		n := r.ConsumerName
		if n == "" {
			n = r.AgentName
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
