package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/skill"
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
// skillInfo is the structured single-skill summary returned by `qvr info`.
//
// Issue #116: the field names and shape are aligned with
// `qvr list --output json` (which surfaces the LockEntry directly):
//   - All camelCase (no snake_case stragglers like `subtree_hash`).
//   - Targets carries the lockfile's `[]string` array so a consumer
//     walking list→info gets the same shape. The richer per-target
//     link status moved to TargetDetails (`targetDetails` in JSON),
//     keeping the info-only enrichment without colliding with list.
//   - Lockfile fields previously absent from info (`mode`, `editPath`,
//     `installCommit`, `installedAt`, `sourceUpstream`, `path`) are
//     present here too.
type skillInfo struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	AllowedTools  string            `json:"allowedTools,omitempty"`
	Registry      string            `json:"registry,omitempty"`
	// Ref is the install-time version label — branch, tag, or "local" for
	// link installs. JSON field name matches the lockfile schema (and
	// `qvr list --output json`) so consumers walking list-then-info don't
	// learn the divergence the hard way. Pre-#123 this was `"branch"`,
	// which mislabelled every tagged install as a branch.
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
	// CommitDrift, when non-empty, is the worktree's actual HEAD SHA when
	// it differs from the recorded `commit` field. Populated by `qvr info`
	// for issue #73 so tampered or unhealed lockfile entries are visible
	// next to the recorded commit instead of buried behind `qvr lock verify`.
	CommitDrift    string `json:"commitDrift,omitempty"`
	Worktree       string `json:"worktree,omitempty"`
	LinkTarget     string `json:"linkTarget,omitempty"`
	Source         string `json:"source,omitempty"`
	SourceUpstream string `json:"sourceUpstream,omitempty"`
	SubtreeHash    string `json:"subtreeHash,omitempty"`
	TreeOID        string `json:"treeOID,omitempty"`
	// Lockfile-side fields previously absent from info but present on
	// list. Surfaced here so list→info walkers don't have to read the
	// raw lockfile to recover them. Issue #116.
	Mode          string    `json:"mode,omitempty"`
	EditPath      string    `json:"editPath,omitempty"`
	Path          string    `json:"path,omitempty"`
	InstallCommit string    `json:"installCommit,omitempty"`
	InstalledAt   time.Time `json:"installedAt"`
	// Targets mirrors `list`'s `targets: ["claude", …]` — the canonical
	// LockEntry shape. TargetDetails carries the info-only enrichment
	// (path + symlink-OK status) under a distinct key so the two
	// commands' `targets` fields aren't subtly incompatible. Issue #116.
	Targets       []string       `json:"targets"`
	TargetDetails []targetStatus `json:"targetDetails,omitempty"`
	Files         []string       `json:"files"`
	// Scan and Provenance surface the lock entry's supply-chain signals
	// when present (v6: inline on the entry, no verification wrapper).
	Scan       *model.ScanRef       `json:"scan,omitempty"`
	Provenance *model.ProvenanceRef `json:"provenance,omitempty"`
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

// populateInfoGitState fills the ref/commit/worktree (or link-target) block.
// Link installs carry no upstream git state — Branch/Commit/Worktree stay
// blank so consumers don't render placeholder columns, and LinkTarget is
// surfaced instead. For everything else, entry.Commit is cross-checked
// against the worktree HEAD so a tampered lockfile entry surfaces here next
// to the recorded commit (issue #73); the warning is suppressed when HEAD
// descends from entry.Commit — the normal "user committed locally; lockfile
// catches up at publish time" pattern (issue #99), not tamper. HEAD-read
// errors are non-fatal — info is read-only.
func populateInfoGitState(info *skillInfo, entry *model.LockEntry, projectRoot string) {
	if entry.IsLink() {
		info.LinkTarget = entry.Source
		return
	}
	info.Ref = entry.Ref
	info.Commit = entry.Commit
	info.Worktree = skill.EntryWorktreePath(entry)
	if entry.Commit == "" {
		return
	}
	if head, hErr := skill.ResolveEntryHeadCommit(entry, projectRoot); hErr == nil && head != "" && head != entry.Commit {
		if ancestor, _ := skill.EntryCommitIsAncestorOfHead(entry, projectRoot); !ancestor {
			info.CommitDrift = head
		}
	}
}

// buildSkillInfo gathers all the data for `qvr info` from local state only.
// Pulled out as a top-level function so tests can call it directly with a
// hand-built lock entry instead of going through Cobra.
func buildSkillInfo(entry *model.LockEntry, projectRoot string, global bool) (*skillInfo, error) {
	info := &skillInfo{
		Name:          entry.Name,
		Registry:      entry.Registry,
		Source:        entry.Source,
		SubtreeHash:   entry.SubtreeHash,
		TreeOID:       entry.TreeOID,
		Mode:          entry.Mode,
		EditPath:      entry.EditPath,
		Path:          entry.Path,
		InstallCommit: entry.InstallCommit,
		InstalledAt:   entry.InstalledAt,
		Scan:          entry.Scan,
		Provenance:    entry.Provenance,
	}
	if entry.Provenance != nil {
		info.SourceUpstream = entry.Provenance.Upstream
	}
	populateInfoGitState(info, entry, projectRoot)

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

	// Targets is the canonical lockfile shape (`["claude", "cursor", …]`).
	// TargetDetails carries the per-target link verification info
	// kept under a distinct key after #116.
	info.Targets = append([]string(nil), entry.Targets...)
	// The agent symlink for a consumed root-layout (path=".") skill legitimately
	// points at the sanitized .git/qvr-view, not the worktree root — verify
	// against AgentLinkTarget like doctor/status/list/lock verify do (issue
	// #170). skillDir (EffectiveTarget) stays the *content* path used above for
	// frontmatter/file loading; only the symlink-verification target differs.
	expectedTarget := skill.AgentLinkTarget(entry, projectRoot)
	for _, t := range entry.Targets {
		linkPath, err := skill.ResolveTargetPath(t, entry.Name, projectRoot, global)
		ts := targetStatus{Target: t, Path: linkPath}
		if err != nil {
			ts.Error = err.Error()
			info.TargetDetails = append(info.TargetDetails, ts)
			continue
		}
		// Edit-mode entries (qvr create / qvr edit): the canonical target
		// dir IS a real directory — the eject dir itself — not a symlink
		// pointing at the shared worktree. VerifyTarget expects a
		// symlink, so it flagged every ejected canonical as
		// "✗ symlink path already exists and is not a symlink". Mirror
		// doctor's ejected-check path (cmd/doctor.go checkSymlink) for
		// the canonical target; sibling targets remain symlinks pointing
		// at the canonical and still go through VerifyTarget. Issue #117.
		if expectedTarget != "" {
			var verr error
			if entry.IsEdit() && linkPath == expectedTarget {
				verr = skill.VerifyDirContainsSkill(linkPath)
			} else {
				verr = skill.VerifyTarget(linkPath, expectedTarget)
			}
			if verr != nil {
				ts.Error = verr.Error()
			} else {
				ts.OK = true
			}
		}
		info.TargetDetails = append(info.TargetDetails, ts)
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
	renderInfoVersionRows(w, info)
	renderInfoSourceRows(w, info)
	if info.License != "" {
		fmt.Fprintf(w, "License:     %s\n", info.License)
	}
	if info.Compatibility != "" {
		fmt.Fprintf(w, "Compat:      %s\n", info.Compatibility)
	}
	if info.AllowedTools != "" {
		fmt.Fprintf(w, "Tools:       %s\n", info.AllowedTools)
	}
	renderInfoMetadata(w, info)
	renderInfoTargets(w, info)
	renderInfoFiles(w, info)
	renderVerificationSection(w, info.Scan, info.Provenance)
}

// renderInfoVersionRows prints the link-target or ref/commit/worktree block.
// Linked skills have no worktree, commit, or branch — `qvr link` wires a direct
// symlink to the source directory — so only a LinkTarget row is rendered for
// them and the empty git rows are suppressed.
func renderInfoVersionRows(w io.Writer, info *skillInfo) {
	if info.Source == "link" {
		if info.LinkTarget != "" {
			fmt.Fprintf(w, "LinkTarget:  %s\n", info.LinkTarget)
		}
		return
	}
	if info.Ref != "" {
		fmt.Fprintf(w, "Ref:         %s\n", info.Ref)
	}
	if info.Commit != "" {
		fmt.Fprintf(w, "Commit:      %s\n", info.Commit)
	}
	if info.CommitDrift != "" {
		fmt.Fprintf(w, "  %s worktree HEAD is %s (lockfile commit field is out of date — see #73)\n",
			output.NewStyler(w).Red("✗"), info.CommitDrift)
	}
	if info.Worktree != "" {
		fmt.Fprintf(w, "Worktree:    %s\n", info.Worktree)
	}
}

// renderInfoSourceRows prints the Source block with #117 mode precedence: an
// edit-ejected entry shows Source: edit plus EditPath/Upstream rows; otherwise
// the raw Source URL.
func renderInfoSourceRows(w io.Writer, info *skillInfo) {
	switch {
	case info.Mode == "edit":
		fmt.Fprintf(w, "Source:      edit\n")
		if info.EditPath != "" {
			fmt.Fprintf(w, "EditPath:    %s\n", info.EditPath)
		}
		upstream := info.SourceUpstream
		if upstream == "" {
			upstream = info.Source
		}
		if upstream != "" {
			fmt.Fprintf(w, "Upstream:    %s\n", upstream)
		}
	case info.Source != "":
		fmt.Fprintf(w, "Source:      %s\n", info.Source)
	}
}

// renderInfoMetadata prints the sorted metadata key/value block.
func renderInfoMetadata(w io.Writer, info *skillInfo) {
	if len(info.Metadata) == 0 {
		return
	}
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

// renderInfoTargets prints the per-target link-verification block. Text view
// iterates TargetDetails (the link-verified shape); JSON consumers get both the
// plain `targets` array (#116) and the richer `targetDetails` block.
func renderInfoTargets(w io.Writer, info *skillInfo) {
	if len(info.TargetDetails) == 0 {
		return
	}
	fmt.Fprintln(w, "Targets:")
	style := output.NewStyler(w)
	for _, t := range info.TargetDetails {
		marker := style.Red("✗")
		detail := t.Error
		if t.OK {
			marker = style.Green("✓")
			detail = t.Path
		}
		if detail == "" {
			detail = t.Path
		}
		fmt.Fprintf(w, "  %s %-9s %s\n", marker, t.Target, detail)
	}
}

// renderInfoFiles prints the bundled file tree, indented by path depth.
func renderInfoFiles(w io.Writer, info *skillInfo) {
	if len(info.Files) == 0 {
		return
	}
	fmt.Fprintln(w, "Files:")
	for _, f := range info.Files {
		depth := strings.Count(f, "/")
		indent := strings.Repeat("  ", depth)
		fmt.Fprintf(w, "  %s%s\n", indent, filepath.Base(f))
	}
}

// renderVerificationSection prints the supply-chain signals block in text
// mode. Omitted entirely when neither scan nor provenance has been recorded
// (the default at install time) so the output stays tidy.
func renderVerificationSection(w io.Writer, scan *model.ScanRef, prov *model.ProvenanceRef) {
	if scan == nil && prov.IsEmpty() {
		return
	}
	fmt.Fprintln(w, "Verification:")
	if scan != nil {
		fmt.Fprintf(w, "  Scan:        %s — %s (critical=%d, high=%d, medium=%d, low=%d, info=%d)\n",
			scan.Decision, scan.ScannerVersion,
			scan.Counts.Critical, scan.Counts.High,
			scan.Counts.Medium, scan.Counts.Low, scan.Counts.Info)
	}
	if !prov.IsEmpty() {
		fmt.Fprintf(w, "  Provenance:  %s\n", provenanceLine(prov))
	}
}
