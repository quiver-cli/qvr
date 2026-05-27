package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var infoGlobal bool

// targetStatus reports whether the symlink for a given agent target points at
// the worktree we expect.
type targetStatus struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// skillInfo is the structured single-skill summary returned by `qvr info`.
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
	Worktree      string            `json:"worktree,omitempty"`
	LinkTarget    string            `json:"link_target,omitempty"`
	Source        string            `json:"source,omitempty"`
	Targets       []targetStatus    `json:"targets"`
	Files         []string          `json:"files"`
	// Verification mirrors the lockfile's VerificationRecord — same shape
	// `qvr switch --output json` emits, so supply-chain dashboards can
	// script against `qvr info` instead of parsing qvr.lock directly.
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
	rootCmd.AddCommand(infoCmd)
}

func runInfo(cmd *cobra.Command, args []string) error {
	name := args[0]
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lock, err := model.ReadLockFile(model.DefaultLockPath(projectRoot, config.Dir(), infoGlobal))
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	entry, err := lock.Get(name)
	if err != nil {
		return err
	}

	info, err := buildSkillInfo(entry, projectRoot)
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
func buildSkillInfo(entry *model.LockEntry, projectRoot string) (*skillInfo, error) {
	info := &skillInfo{
		Name:         entry.Name,
		Registry:     entry.Registry,
		Branch:       entry.Branch,
		Commit:       entry.Commit,
		Worktree:     entry.Worktree,
		LinkTarget:   entry.LinkTarget,
		Source:       entry.Source,
		Verification: entry.Verification,
	}

	skillDir := skill.EffectiveTarget(entry)
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
		linkPath, err := skill.ResolveTargetPath(t, entry.Name, projectRoot, entry.Global)
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

// renderVerificationSection prints the supply-chain provenance block in
// text mode. Omitted entirely when nil (legacy v2-loaded entries) so the
// output stays tidy for skills without recorded provenance.
func renderVerificationSection(w io.Writer, v *model.VerificationRecord) {
	if v == nil {
		return
	}
	fmt.Fprintln(w, "Verification:")
	if v.Status != "" {
		fmt.Fprintf(w, "  Status:      %s\n", v.Status)
	}
	if v.SubtreeHash != "" {
		fmt.Fprintf(w, "  SubtreeHash: %s\n", shortHashLabel(v.SubtreeHash))
	}
	// Provenance one-liner mirrors what installer.go records.
	if src := provenanceSummary(v.Provenance); src != "" {
		fmt.Fprintf(w, "  Source:      %s\n", src)
	}
	if v.Signature != nil {
		fmt.Fprintf(w, "  Signature:   %s (%s)\n", v.Signature.Path, v.Signature.Algorithm)
	}
	for _, warn := range v.Warnings {
		fmt.Fprintf(w, "  Warning:     %s\n", warn)
	}
	if !v.VerifiedAt.IsZero() {
		fmt.Fprintf(w, "  VerifiedAt:  %s\n", v.VerifiedAt.UTC().Format("2006-01-02T15:04:05Z"))
	}
}

// provenanceSummary collapses the four ProvenanceRef fields into a single
// line for the text view. Returns empty when nothing's recorded so the
// caller skips the row entirely.
func provenanceSummary(p model.ProvenanceRef) string {
	if p.RegistryName == "" && p.RegistryURL == "" && p.Ref == "" && p.Subpath == "" {
		return ""
	}
	target := p.RegistryName
	if p.RegistryURL != "" {
		if target == "" {
			target = p.RegistryURL
		} else {
			target = fmt.Sprintf("%s (%s)", target, p.RegistryURL)
		}
	}
	if p.Ref != "" {
		target = fmt.Sprintf("%s @ %s", target, p.Ref)
	}
	if p.Subpath != "" {
		target = fmt.Sprintf("%s → %s", target, p.Subpath)
	}
	return target
}
