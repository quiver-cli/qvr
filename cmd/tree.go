package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/output"
	"github.com/spf13/cobra"
)

var (
	treeGlobal bool
	treeAll    bool
)

var treeCmd = &cobra.Command{
	Use:   "tree",
	Short: "Show installed skills as a registry → skill → target tree",
	Long: `Render the installed skills grouped by registry, then skill, then the
agent targets each is linked into — the ` + "`uv tree`" + `-style home screen. Skills
have no transitive dependencies yet, so the tree is two levels deep.

Reads the project lock by default; pass --global for the user-global lock, or
--all to union both with a per-scope section header (mirrors ` + "`qvr list`" + `).

Markers: [edit] / [link] for ejected or locally-linked skills, [disabled] for
skills hidden from agents.`,
	RunE: runTree,
}

func init() {
	treeCmd.Flags().BoolVar(&treeGlobal, "global", false,
		"read the user-global lock file instead of the project lock")
	treeCmd.Flags().BoolVar(&treeAll, "all", false,
		"union project and global locks (adds a per-scope section header)")
	rootCmd.AddCommand(treeCmd)
}

// treeSkill is one skill node in the tree. Commit is the full SHA in JSON;
// the text renderer shortens it. Mode/Disabled surface the same edit/link/
// disabled markers `qvr list` uses (list.go).
type treeSkill struct {
	Name     string   `json:"name"`
	Ref      string   `json:"ref"`
	Commit   string   `json:"commit,omitempty"`
	Mode     string   `json:"mode,omitempty"`
	Disabled bool     `json:"disabled,omitempty"`
	Targets  []string `json:"targets"`
}

// treeGroup is a registry node holding its skills. Scope is populated only
// under --all so single-scope JSON stays uncluttered.
type treeGroup struct {
	Scope    string      `json:"scope,omitempty"`
	Registry string      `json:"registry"`
	Skills   []treeSkill `json:"skills"`
}

func runTree(cmd *cobra.Command, args []string) error {
	_ = cmd
	_ = args
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	locks, err := loadScopedLocks(projectRoot, treeGlobal, treeAll)
	if err != nil {
		return err
	}
	groups := buildTreeGroups(locks, treeAll)

	if printer.Format == output.FormatJSON {
		if groups == nil {
			groups = []treeGroup{}
		}
		return printer.JSON(groups)
	}
	renderTreeText(groups, treeAll)
	return nil
}

// buildTreeGroups flattens the loaded locks into registry-keyed groups,
// preserving scope order (loadScopedLocks returns project then global) so the
// --all section headers stay grouped. Entries with no registry — link, edit,
// or standalone installs — fall under a synthetic "(local)" group.
func buildTreeGroups(locks []scopedLock, showScope bool) []treeGroup {
	var groups []treeGroup
	for _, s := range locks {
		if s.Lock == nil {
			continue
		}
		byReg := map[string][]*model.LockEntry{}
		var regKeys []string
		for _, e := range s.Lock.Entries() {
			key := e.Registry
			if key == "" {
				key = "(local)"
			}
			if _, ok := byReg[key]; !ok {
				regKeys = append(regKeys, key)
			}
			byReg[key] = append(byReg[key], e)
		}
		sort.Strings(regKeys)
		for _, key := range regKeys {
			ents := byReg[key]
			sort.Slice(ents, func(i, j int) bool { return ents[i].Name < ents[j].Name })
			skills := make([]treeSkill, 0, len(ents))
			for _, e := range ents {
				mode := ""
				switch {
				case e.IsEdit():
					mode = "edit"
				case e.IsLink():
					mode = "link"
				}
				tgts := append([]string(nil), e.Targets...)
				sort.Strings(tgts)
				skills = append(skills, treeSkill{
					Name:     e.Name,
					Ref:      e.Ref,
					Commit:   e.Commit,
					Mode:     mode,
					Disabled: e.Disabled,
					Targets:  tgts,
				})
			}
			g := treeGroup{Registry: key, Skills: skills}
			if showScope {
				g.Scope = s.Scope
			}
			groups = append(groups, g)
		}
	}
	return groups
}

func renderTreeText(groups []treeGroup, showScope bool) {
	if len(groups) == 0 {
		printer.Info("No installed skills.")
		return
	}
	lastScope := ""
	first := true
	for _, g := range groups {
		if showScope && g.Scope != lastScope {
			if !first {
				printer.Info("")
			}
			printer.Info(g.Scope + ":")
			lastScope = g.Scope
		}
		first = false
		printer.Info(g.Registry)
		for si, s := range g.Skills {
			lastSkill := si == len(g.Skills)-1
			branch, cont := "├── ", "│   "
			if lastSkill {
				branch, cont = "└── ", "    "
			}
			printer.Info(branch + formatTreeSkillLine(s))
			for ti, tgt := range s.Targets {
				leaf := "├── "
				if ti == len(s.Targets)-1 {
					leaf = "└── "
				}
				printer.Info(cont + leaf + tgt)
			}
		}
	}
}

// formatTreeSkillLine renders "name@ref (sha7)" plus any [edit]/[link]/
// [disabled] markers. A missing commit (link installs) shows an em dash.
func formatTreeSkillLine(s treeSkill) string {
	sha := "—"
	if s.Commit != "" {
		sha = shortSHA(s.Commit)
	}
	line := fmt.Sprintf("%s@%s (%s)", s.Name, s.Ref, sha)
	var markers []string
	if s.Mode != "" {
		markers = append(markers, s.Mode)
	}
	if s.Disabled {
		markers = append(markers, "disabled")
	}
	if len(markers) > 0 {
		line += " [" + strings.Join(markers, ", ") + "]"
	}
	return line
}
