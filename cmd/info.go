package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var (
	infoGlobal bool
	infoAll    bool
)

// targetStatus reports whether the symlink for a given agent target points at
// the worktree we expect.
type targetStatus struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// skillInfo is the structured single-skill summary returned by `qvr info`.
//
// Worktree, LinkTarget, and Source are synthesised from the v5 lock entry:
// the on-disk lock only carries Source (URL or absolute path), so the JSON
// surface keeps the friendlier fields for downstream consumers that already
// scripted against them.
type skillInfo struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	AllowedTools  string            `json:"allowed_tools,omitempty"`
	Registry      string            `json:"registry,omitempty"`
	Branch        string            `json:"branch,omitempty"`
	Commit        string            `json:"commit,omitempty"`
	// CommitDrift, when non-empty, is the worktree's actual HEAD SHA when
	// it differs from the recorded `commit` field. Populated by `qvr info`
	// for issue #73 so tampered or unhealed lockfile entries are visible
	// next to the recorded commit instead of buried behind `qvr lock verify`.
	CommitDrift string         `json:"commit_drift,omitempty"`
	Worktree    string         `json:"worktree,omitempty"`
	LinkTarget  string         `json:"link_target,omitempty"`
	Source      string         `json:"source,omitempty"`
	SubtreeHash string         `json:"subtree_hash,omitempty"`
	Targets     []targetStatus `json:"targets"`
	Files       []string       `json:"files"`
	// Verification surfaces real signals (scan, signature, eval, attestation,
	// skill card) when present on the lock entry.
	Verification *model.VerificationRecord `json:"verification,omitempty"`
}

var infoCmd = &cobra.Command{
	Use:   "info <skill>",
	Short: "Show structured details for a single installed skill",
	Long: `Print frontmatter, registry/branch/commit, target symlink status, and
the bundled file tree for an installed skill. Fast — reads only local state.`,
	Args: cobra.ExactArgs(1),
	RunE: runInfo,
}

func init() {
	infoCmd.Flags().BoolVar(&infoGlobal, "global", false,
		"read the user-global lock file instead of the project lock")
	infoCmd.Flags().BoolVar(&infoAll, "all", false,
		"search both project and global locks (errors when both contain the skill)")
	rootCmd.AddCommand(infoCmd)
}

func runInfo(cmd *cobra.Command, args []string) error {
	name := args[0]
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	locks, err := loadScopedLocks(projectRoot, infoGlobal, infoAll)
	if err != nil {
		return err
	}
	entry, scope, err := findEntryAcrossLocks(name, locks)
	if err != nil {
		return err
	}
	// Target-path resolution still needs to know which agent directory tree
	// to consult, so derive `global` from the lock that actually owns the
	// entry rather than the command-line flag.
	global := scope.Scope == "global"

	info, err := buildSkillInfo(entry, projectRoot, global)
	if err != nil {
		return err
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(info)
	}
	renderInfoText(info)
	return nil
}

// buildSkillInfo gathers all the data for `qvr info` from local state only.
// Pulled out as a top-level function so tests can call it directly with a
// hand-built lock entry instead of going through Cobra.
func buildSkillInfo(entry *model.LockEntry, projectRoot string, global bool) (*skillInfo, error) {
	info := &skillInfo{
		Name:         entry.Name,
		Registry:     entry.Registry,
		Source:       entry.Source,
		SubtreeHash:  entry.SubtreeHash,
		Verification: entry.Verification,
	}
	if entry.IsLink() {
		// Link installs carry no upstream git state — leave Branch/Commit/
		// Worktree blank so consumers don't render placeholder columns and
		// surface LinkTarget instead. Source still appears for parity with
		// remote installs.
		info.LinkTarget = entry.Source
	} else {
		info.Branch = entry.Ref
		info.Commit = entry.Commit
		info.Worktree = skill.EntryWorktreePath(entry)
		// Cross-check entry.Commit against the worktree HEAD so a tampered
		// lockfile entry surfaces here next to the recorded commit (issue
		// #73). Suppress the warning when HEAD descends from entry.Commit —
		// that's the normal "user committed locally; lockfile catches up at
		// publish time" pattern (issue #99), not tamper. HEAD-read errors
		// are non-fatal — info is read-only.
		if entry.Commit != "" {
			if head, hErr := skill.ResolveEntryHeadCommit(entry, projectRoot); hErr == nil && head != "" && head != entry.Commit {
				if ancestor, _ := skill.EntryCommitIsAncestorOfHead(entry, projectRoot); !ancestor {
					info.CommitDrift = head
				}
			}
		}
	}

	skillDir := skill.EffectiveTarget(entry, projectRoot)
	if skillDir != "" {
		if loaded, err := skill.LoadFromPath(skillDir); err == nil {
			info.Description = loaded.Frontmatter.Description
			info.License = loaded.Frontmatter.License
			info.Compatibility = loaded.Frontmatter.Compatibility
			info.Metadata = loaded.Frontmatter.Metadata
			info.AllowedTools = loaded.Frontmatter.AllowedTools
			info.Files = loaded.Files
			sort.Strings(info.Files)
		}
	}

	expectedTarget := skillDir
	for _, t := range entry.Targets {
		linkPath, err := skill.ResolveTargetPath(t, entry.Name, projectRoot, global)
		ts := targetStatus{Target: t, Path: linkPath}
		if err != nil {
			ts.Error = err.Error()
			info.Targets = append(info.Targets, ts)
			continue
		}
		if expectedTarget != "" {
			if verr := skill.VerifyTarget(linkPath, expectedTarget); verr != nil {
				ts.Error = verr.Error()
			} else {
				ts.OK = true
			}
		}
		info.Targets = append(info.Targets, ts)
	}
	return info, nil
}

func renderInfoText(info *skillInfo) {
	w := printer.Out
	fmt.Fprintf(w, "Name:        %s\n", info.Name)
	if info.Description != "" {
		fmt.Fprintf(w, "Description: %s\n", info.Description)
	}
	if info.Registry != "" {
		fmt.Fprintf(w, "Registry:    %s\n", info.Registry)
	}
	// Linked skills have no worktree, commit, or branch — `qvr link` wires a
	// direct symlink to the source directory. Render a LinkTarget row for
	// those, suppress the empty Branch/Commit/Worktree rows entirely.
	if info.Source == "link" {
		if info.LinkTarget != "" {
			fmt.Fprintf(w, "LinkTarget:  %s\n", info.LinkTarget)
		}
	} else {
		if info.Branch != "" {
			fmt.Fprintf(w, "Branch:      %s\n", info.Branch)
		}
		if info.Commit != "" {
			fmt.Fprintf(w, "Commit:      %s\n", info.Commit)
		}
		if info.CommitDrift != "" {
			fmt.Fprintf(w, "  ✗ worktree HEAD is %s (lockfile commit field is out of date — see #73)\n", info.CommitDrift)
		}
		if info.Worktree != "" {
			fmt.Fprintf(w, "Worktree:    %s\n", info.Worktree)
		}
	}
	if info.Source != "" {
		fmt.Fprintf(w, "Source:      %s\n", info.Source)
	}
	if info.License != "" {
		fmt.Fprintf(w, "License:     %s\n", info.License)
	}
	if info.Compatibility != "" {
		fmt.Fprintf(w, "Compat:      %s\n", info.Compatibility)
	}
	if info.AllowedTools != "" {
		fmt.Fprintf(w, "Tools:       %s\n", info.AllowedTools)
	}

	if len(info.Metadata) > 0 {
		keys := make([]string, 0, len(info.Metadata))
		for k := range info.Metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintln(w, "Metadata:")
		for _, k := range keys {
			fmt.Fprintf(w, "  %s: %s\n", k, info.Metadata[k])
		}
	}

	if len(info.Targets) > 0 {
		fmt.Fprintln(w, "Targets:")
		for _, t := range info.Targets {
			marker := "✗"
			detail := t.Error
			if t.OK {
				marker = "✓"
				detail = t.Path
			}
			if detail == "" {
				detail = t.Path
			}
			fmt.Fprintf(w, "  %s %-9s %s\n", marker, t.Target, detail)
		}
	}

	if len(info.Files) > 0 {
		fmt.Fprintln(w, "Files:")
		for _, f := range info.Files {
			depth := strings.Count(f, "/")
			indent := strings.Repeat("  ", depth)
			fmt.Fprintf(w, "  %s%s\n", indent, filepath.Base(f))
		}
	}

	renderVerificationSection(w, info.Verification)
}

// renderVerificationSection prints the supply-chain signals block in text
// mode. Omitted entirely when nil (the default at install time, before any
// scan/signature/eval has been recorded) so the output stays tidy.
func renderVerificationSection(w io.Writer, v *model.VerificationRecord) {
	if v == nil || v.IsEmpty() {
		return
	}
	fmt.Fprintln(w, "Verification:")
	if v.Scan != nil {
		fmt.Fprintf(w, "  Scan:        %s — %s (critical=%d, high=%d, medium=%d, low=%d, info=%d)\n",
			v.Scan.Decision, v.Scan.ScannerVersion,
			v.Scan.Counts.Critical, v.Scan.Counts.High,
			v.Scan.Counts.Medium, v.Scan.Counts.Low, v.Scan.Counts.Info)
	}
	if v.Signature != nil {
		fmt.Fprintf(w, "  Signature:   %s (%s)\n", v.Signature.Path, v.Signature.Algorithm)
	}
	if v.Eval != nil {
		status := "failed"
		if v.Eval.Passed {
			status = "passed"
		}
		fmt.Fprintf(w, "  Eval:        %s — %s\n", status, v.Eval.HarnessVersion)
	}
	if v.Attestation != nil {
		fmt.Fprintf(w, "  Attestation: %s\n", v.Attestation.Path)
	}
	if v.SkillCard != nil {
		fmt.Fprintf(w, "  SkillCard:   %s\n", v.SkillCard.Path)
	}
}
