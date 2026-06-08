package model

import "sort"

// Target represents an AI coding agent that can consume skills. It is a pure
// routing record: a canonical name plus the project-relative (LocalDir) and
// home-relative (GlobalDir) directories qvr symlinks skills into. The full set
// of supported agents lives in targetRegistry (targets_data.go) — edit that
// table to add or re-route an agent.
type Target struct {
	// Name is the canonical, CLI-facing identifier (e.g. "claude").
	Name string `json:"name"`
	// Display is the human-readable label (e.g. "Claude Code").
	Display string `json:"display,omitempty"`
	// LocalDir is the project-relative skills directory used for project
	// installs recorded in qvr.lock (e.g. ".claude/skills").
	LocalDir string `json:"local_dir"`
	// GlobalDir is the home-relative skills directory used for `--global`
	// ambient installs (e.g. "~/.claude/skills").
	GlobalDir string `json:"global_dir"`
	// Aliases are alternate names accepted on the CLI that resolve to this
	// target (e.g. "claude-code" -> "claude"). Never used as map keys.
	Aliases []string `json:"aliases,omitempty"`
}

// Targets maps every canonical target name to its routing record. Built once
// from targetRegistry at package init. Keyed by canonical Name only — alias
// lookups go through LookupTarget. Callers that index this map directly (e.g.
// iterating every target) are fine; callers resolving user input must use
// LookupTarget so aliases work.
var Targets map[string]Target

// targetAliasIndex maps each alias to the canonical target name it resolves to.
var targetAliasIndex map[string]string

func init() {
	Targets = make(map[string]Target, len(targetRegistry))
	targetAliasIndex = make(map[string]string)
	for _, t := range targetRegistry {
		Targets[t.Name] = t
		for _, alias := range t.Aliases {
			targetAliasIndex[alias] = t.Name
		}
	}
}

// LookupTarget resolves a target by canonical name or alias, returning the
// target record and whether it was found. This is the alias-aware entry point —
// every site that accepts a user-supplied target name should route through it.
func LookupTarget(name string) (Target, bool) {
	if t, ok := Targets[name]; ok {
		return t, true
	}
	if canonical, ok := targetAliasIndex[name]; ok {
		return Targets[canonical], true
	}
	return Target{}, false
}

// CanonicalTarget returns the canonical name for a target name or alias, and
// whether it was found. Use when you need to normalise user input to the
// canonical name (e.g. before persisting it to the lockfile).
func CanonicalTarget(name string) (string, bool) {
	t, ok := LookupTarget(name)
	if !ok {
		return "", false
	}
	return t.Name, true
}

// TargetNames returns all canonical target names sorted alphabetically.
func TargetNames() []string {
	names := make([]string, 0, len(targetRegistry))
	for _, t := range targetRegistry {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}
