package cli

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/linkplan"
	"github.com/mr-miles/aikito-go/internal/mcp"
	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/sync"
)

// inspectionContext ports workspace/inspection.py's WorkspaceInspectionContext:
// it builds each resource plan at most once per command, read-only, and
// projects plans into linkplan.View values. A failed plan only affects the
// views that depend on it.
type inspectionContext struct {
	aikitoDir, home string

	regLoaded bool
	reg       *registry.AgentRegistry
	regErr    error
	defs      map[string]registry.AgentDefinition

	instrLoaded bool
	instrPlan   linkplan.InstructionPlan
	instrViews  []linkplan.View
	instrErr    error

	skillPlans map[string]linkplan.GlobalSkillPlan

	subLoaded  bool
	subViews   []linkplan.View
	subConfigs map[string]bool
	subErr     error

	mcpLoaded bool
	mcpSpecs  []mcp.AgentSpec
	mcpPlan   mcp.MCPPlan
	mcpErr    error
}

func newInspectionContext(aikitoDir, home string) *inspectionContext {
	return &inspectionContext{aikitoDir: aikitoDir, home: home, skillPlans: map[string]linkplan.GlobalSkillPlan{}}
}

// agents is load_agent_definitions: the registry in agents/<name>.toml file
// order, plus the full definitions keyed by name.
func (c *inspectionContext) agents() (*registry.AgentRegistry, map[string]registry.AgentDefinition, error) {
	if !c.regLoaded {
		c.regLoaded = true
		reg, err := registry.LoadStrict(c.aikitoDir, c.home)
		if err == nil {
			c.reg = reg.InFileOrder()
			c.defs, err = registry.LoadAgentDefinitions(c.aikitoDir, c.home)
		}
		c.regErr = err
	}
	return c.reg, c.defs, c.regErr
}

func (c *inspectionContext) instructionPlan() (linkplan.InstructionPlan, []linkplan.View, error) {
	if !c.instrLoaded {
		c.instrLoaded = true
		reg, _, err := c.agents()
		if err == nil {
			var batch linkplan.InstructionBatch
			batch, err = linkplan.BuildGlobalInstructionBatch(c.aikitoDir, c.home, reg)
			if err == nil {
				c.instrPlan = linkplan.PlanInstructions(batch, c.home, false)
				c.instrViews = c.instrPlan.Inspect()
			}
		}
		c.instrErr = err
	}
	return c.instrPlan, c.instrViews, c.instrErr
}

func (c *inspectionContext) skillPlan(skills []string) (linkplan.GlobalSkillPlan, error) {
	sorted := append([]string(nil), skills...)
	sort.Strings(sorted)
	key := strings.Join(sorted, "\x00")
	if p, ok := c.skillPlans[key]; ok {
		return p, nil
	}
	reg, _, err := c.agents()
	if err != nil {
		return linkplan.GlobalSkillPlan{}, err
	}
	batch, err := linkplan.BuildGlobalSkillBatch(c.aikitoDir, c.home, skills, reg, filepath.Join(c.home, ".agents", "skills"))
	if err != nil {
		return linkplan.GlobalSkillPlan{}, err
	}
	p := linkplan.PlanGlobalSkills(batch, c.home, nil)
	c.skillPlans[key] = p
	return p, nil
}

func (c *inspectionContext) skillViews(skills []string) ([]linkplan.View, error) {
	p, err := c.skillPlan(skills)
	if err != nil {
		return nil, err
	}
	return p.Inspect(), nil
}

// subagentViews ports subagent_plan.inspect() and subagent_configs.
func (c *inspectionContext) subagentViews() ([]linkplan.View, map[string]bool, error) {
	if !c.subLoaded {
		c.subLoaded = true
		ops, err := sync.BuildSubagentPlan(c.aikitoDir, c.home, sync.BuildSubagentPlanOptions{GateInstalled: true})
		c.subErr = err
		if err == nil {
			for _, op := range ops {
				st := linkplan.StatusError
				switch op.Action {
				case "OK", sync.SANoop:
					st = linkplan.StatusOK
				case sync.SACreate:
					st = linkplan.StatusMissing
				case sync.SAUpdate:
					st = linkplan.StatusUpdate
				case sync.SAOrphan, sync.SARemove:
					st = linkplan.StatusOrphan
				case sync.SAConflict:
					st = linkplan.StatusConflict
				case sync.SASkip:
					st = linkplan.StatusSkip
				}
				c.subViews = append(c.subViews, linkplan.View{
					ResourceType: "subagent", ResourceName: op.Subagent, Status: st, Scope: "global",
					Agent: op.Agent, TargetPath: op.TargetPath, Reason: op.Reason,
				})
			}
			c.subConfigs = map[string]bool{}
			if _, defs, derr := c.agents(); derr == nil {
				for name, d := range defs {
					if d.Subagents != nil {
						c.subConfigs[name] = true
					}
				}
			}
		}
	}
	return c.subViews, c.subConfigs, c.subErr
}

// mcp loads the specs and builds the MCP plan (mcp_specs + ensure_mcp_plan).
func (c *inspectionContext) mcp() ([]mcp.AgentSpec, error) {
	if !c.mcpLoaded {
		c.mcpLoaded = true
		c.mcpSpecs, c.mcpErr = mcp.LoadAgentSpecs(c.aikitoDir, c.home)
		if c.mcpErr == nil {
			c.mcpPlan, c.mcpErr = mcp.BuildMCPPlan(c.aikitoDir, c.home, mcp.BuildMCPPlanOptions{Specs: c.mcpSpecs})
		}
	}
	return c.mcpSpecs, c.mcpErr
}

// mcpStatus ports WorkspaceInspectionContext.mcp_status.
func (c *inspectionContext) mcpStatus(spec mcp.AgentSpec) string {
	if _, err := c.mcp(); err != nil {
		return mcp.EvaluateSpecStatus(spec, c.home, nil)
	}
	for _, op := range c.mcpPlan.Operations {
		if op.Target.Agent == spec.Agent && op.Target.LogicalIdentity == spec.Server {
			return mcp.MapOperationToStatus(op)
		}
	}
	return mcp.EvaluateSpecStatus(spec, c.home, &c.mcpPlan)
}
