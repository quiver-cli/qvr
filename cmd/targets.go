package cmd

import "github.com/quiver-cli/qvr/internal/config"

// orderedTargets returns the target search order: configured defaults first
// (in the order the user listed them), then the remaining built-in targets
// in their canonical order. Honours comma-separated default_target values
// like "claude,cursor". Shared by the commands that walk agent targets to
// locate a skill's symlink (e.g. `qvr ls`).
func orderedTargets() []string {
	all := []string{"claude", "cursor", "copilot", "codex", "windsurf", "project"}
	cfg, err := config.Load()
	if err != nil {
		return all
	}
	preferred := config.ParseDefaultTargets(cfg.DefaultTarget)
	if len(preferred) == 0 {
		return all
	}
	seen := make(map[string]struct{}, len(preferred))
	out := make([]string, 0, len(all))
	for _, p := range preferred {
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, t := range all {
		if _, used := seen[t]; used {
			continue
		}
		out = append(out, t)
	}
	return out
}
