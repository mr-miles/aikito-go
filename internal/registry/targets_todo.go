package registry

// TODO(item 6, triaged out for time): agents.py's Target / resolve_targets /
// check_target_availability / Target.is_same_object are not yet ported.
// These implement the physical-identity-based consumer-link dedup grouping
// used for "global_skills" / "global_instructions" / "project_instructions"
// target resolution (e.g. collapsing the 8 bundled agents' skills_path
// values down to 3 physically distinct consumer-link targets).
//
// This depends on a not-yet-ported compat.py primitive, get_physical_path
// (symlink-resolved canonical path + real on-disk casing), which
// IsDirectoryCaseSensitive in internal/compat alone doesn't provide — port
// that first. Once it exists, port (in agents.py order):
//   - Target struct (kind/scope/path/canonical_source/consumers/...)
//   - _normalized_physical_path / _physical_target_key
//   - _group_agent_paths
//   - check_target_availability
//   - resolve_targets (global_skills / global_instructions / project_instructions)
//
// Items 1-5 of this package (AgentSpec/AgentDefinition, the mcp.adapter
// inheritance rule, the intentional path-validation asymmetry, BuiltinAgents
// order, and tri-state availability detection) are implemented and tested
// in registry.go / capabilities.go / availability.go / templates.go.
