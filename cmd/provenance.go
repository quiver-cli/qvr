package cmd

import (
	"fmt"
	"os"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var (
	provenanceGlobal bool
	provenanceAll    bool
)

// provenanceView is the structured `qvr provenance` payload: where a skill
// came from, the immutable revision it's pinned to, whether that content was
// scanned, and whether optional git-native publisher signing evidence was
// present. Honest about what it can and can't assert — see the package
// docs and the "uv for agent skills" trust model.
type provenanceView struct {
	Name            string `json:"name"`
	Source          string `json:"source,omitempty"`
	Subdirectory    string `json:"subdirectory,omitempty"`
	Requested       string `json:"requested,omitempty"`   // the ref label asked for
	Resolved        string `json:"resolved,omitempty"`    // the immutable commit it locked to
	TreeOID         string `json:"treeOID,omitempty"`     // native git tree id of the subtree
	SubtreeHash     string `json:"subtreeHash,omitempty"` // canonical content digest (verify anchor)
	SignatureStatus string `json:"signatureStatus"`       // verified | none | invalid
	Signer          string `json:"signer,omitempty"`
	SignedRef       string `json:"signedRef,omitempty"`    // the tag the signature covers, when a tag
	ScanDecision    string `json:"scanDecision,omitempty"` // allowed | blocked | skipped | (empty: never scanned)
	ScannerVersion  string `json:"scannerVersion,omitempty"`
	Install         string `json:"install"` // how the bytes are materialized
	Status          string `json:"status"`  // one-word rollup
}

var provenanceCmd = &cobra.Command{
	Use:   "provenance <skill>",
	Short: "Show where an installed skill came from and what's known about it",
	Long: `Print the provenance of an installed skill: its Git source and subdirectory,
the immutable commit and tree it's pinned to, whether that exact content was
scanned, and whether optional Git signing evidence (a signed tag/commit) was
present and verified.

Provenance is honest about its limits. A "verified" signature means qvr ran
'git verify-tag'/'git verify-commit' and Git was satisfied — it does NOT by
itself mean the author is trusted. An unsigned skill installs unless
security.require_signed is enabled; an *invalid* signature is refused at install
time. Fast — reads local state and, when no signature status is recorded yet,
runs a local Git verification.`,
	Args: cobra.ExactArgs(1),
	RunE: runProvenance,
}

func init() {
	provenanceCmd.Flags().BoolVar(&provenanceGlobal, "global", false,
		"read the user-global lock file instead of the project lock")
	provenanceCmd.Flags().BoolVar(&provenanceAll, "all", false,
		"search both project and global locks (errors when both contain the skill)")
	rootCmd.AddCommand(provenanceCmd)
}

func runProvenance(cmd *cobra.Command, args []string) error {
	name := args[0]
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	locks, err := loadScopedLocks(projectRoot, provenanceGlobal, provenanceAll)
	if err != nil {
		return err
	}
	entry, _, err := findEntryAcrossLocks(name, locks)
	if err != nil {
		return err
	}

	view := buildProvenanceView(entry)

	if printer.Format == output.FormatJSON {
		return printer.JSON(view)
	}
	renderProvenanceText(view)
	return nil
}

// buildProvenanceView assembles the provenance card from the lock entry. When
// the entry has no recorded provenance (installed before signature checking, or
// the writer didn't run), it falls back to a live, best-effort Git verification
// against the installed worktree so `qvr provenance` is still informative.
func buildProvenanceView(entry *model.LockEntry) *provenanceView {
	v := &provenanceView{
		Name:         entry.Name,
		Source:       entry.Source,
		Subdirectory: entry.Path,
		Requested:    entry.Ref,
		Resolved:     entry.Commit,
		TreeOID:      entry.TreeOID,
		SubtreeHash:  entry.SubtreeHash,
		Install:      "symlinked from immutable cache",
	}
	if entry.IsLink() {
		v.Install = "linked from local path (editable — provenance not tracked)"
	} else if entry.IsEdit() {
		v.Install = "ejected for editing (local changes allowed — scan/provenance may be stale)"
	}

	prov := provenanceFor(entry)
	if prov != nil {
		v.SignatureStatus = prov.SignatureStatus
		v.Signer = prov.Signer
		v.SignedRef = prov.Tag
	} else {
		v.SignatureStatus = model.SignatureStatusNone
	}

	if entry.Scan != nil {
		v.ScanDecision = entry.Scan.Decision
		v.ScannerVersion = entry.Scan.ScannerVersion
	}

	v.Status = provenanceStatus(v)
	return v
}

// provenanceFor returns the recorded provenance block, or computes one live
// from the installed worktree when no signature status was recorded (a block
// holding only the commit author means the signature was never checked).
// Live checks are skipped for link/edit installs, which have no upstream git
// revision to verify.
func provenanceFor(entry *model.LockEntry) *model.ProvenanceRef {
	if entry.SignatureStatus() != "" {
		return entry.Provenance
	}
	if entry.IsLink() || entry.IsEdit() {
		return nil
	}
	worktree := skill.EntryWorktreePath(entry)
	if worktree == "" {
		return nil
	}
	return skill.CheckGitProvenance(worktree, entry.Ref, entry.Commit, entry.Path)
}

// provenanceStatus rolls the signals into the one-word state shown to users.
func provenanceStatus(v *provenanceView) string {
	scanned := v.ScanDecision == "allowed" || v.ScanDecision == "blocked"
	switch {
	case v.SignatureStatus == model.SignatureStatusVerified && scanned:
		return "signed + locked + scanned"
	case v.SignatureStatus == model.SignatureStatusVerified:
		return "signed + locked"
	case v.SignatureStatus == model.SignatureStatusInvalid && scanned:
		return "invalid + locked + scanned"
	case v.SignatureStatus == model.SignatureStatusInvalid:
		return "invalid + locked"
	case scanned:
		return "locked + scanned"
	default:
		return "locked"
	}
}

// recordedSigStatus returns the signature status recorded on the lock entry,
// without a live Git check (cheap — for table columns in list/outdated).
// Defaults to "none" when no provenance was recorded.
func recordedSigStatus(e *model.LockEntry) string {
	if s := e.SignatureStatus(); s != "" {
		return s
	}
	return model.SignatureStatusNone
}

// signedCol renders a signature status for a table column (list/outdated).
func signedCol(status string) string {
	switch status {
	case model.SignatureStatusVerified:
		return "✓ verified"
	case model.SignatureStatusInvalid:
		return "✗ invalid"
	case model.SignatureStatusNone:
		return "none"
	default:
		return "—"
	}
}

// provenanceLine renders a ProvenanceRef as a single human-readable status
// string. Shared with `qvr info`'s verification section.
func provenanceLine(p *model.ProvenanceRef) string {
	if p == nil {
		return "no git signature"
	}
	switch p.SignatureStatus {
	case model.SignatureStatusVerified:
		subject := "git commit"
		if p.Tag != "" {
			subject = "git tag " + p.Tag
		}
		if p.Signer != "" {
			return fmt.Sprintf("verified %s (%s)", subject, p.Signer)
		}
		return fmt.Sprintf("verified %s", subject)
	case model.SignatureStatusInvalid:
		return "invalid git signature"
	default:
		return "no git signature"
	}
}

func renderProvenanceText(v *provenanceView) {
	w := printer.Out
	fmt.Fprintf(w, "Skill        %s\n", v.Name)
	if v.Source != "" {
		fmt.Fprintf(w, "Source       %s\n", v.Source)
	}
	if v.Subdirectory != "" {
		fmt.Fprintf(w, "Subdirectory %s\n", v.Subdirectory)
	}
	if v.Requested != "" {
		fmt.Fprintf(w, "Requested    %s\n", v.Requested)
	}
	if v.Resolved != "" {
		fmt.Fprintf(w, "Resolved     %s\n", v.Resolved)
	}
	if v.TreeOID != "" {
		fmt.Fprintf(w, "Tree         %s\n", v.TreeOID)
	}
	sig := provenanceFromView(v)
	fmt.Fprintf(w, "Signature    %s\n", sig)
	if v.ScanDecision != "" {
		fmt.Fprintf(w, "Scan         %s on tree %s\n", v.ScanDecision, shortLabel(v.SubtreeHash))
	} else {
		fmt.Fprintf(w, "Scan         not recorded\n")
	}
	fmt.Fprintf(w, "Install      %s\n", v.Install)
	fmt.Fprintf(w, "Status       %s\n", v.Status)
}

// provenanceFromView renders the signature line for the text card from the
// flattened view fields.
func provenanceFromView(v *provenanceView) string {
	return provenanceLine(&model.ProvenanceRef{
		SignatureStatus: v.SignatureStatus,
		Signer:          v.Signer,
		Tag:             v.SignedRef,
	})
}

// shortLabel trims a "sha256:<hex>" or raw hex to a compact display form.
func shortLabel(s string) string {
	if s == "" {
		return "(none)"
	}
	const prefix = "sha256:"
	body := s
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		body = s[len(prefix):]
	}
	if len(body) > 12 {
		return body[:12]
	}
	return body
}
