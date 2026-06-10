package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/registry"
)

func writeFullSkill(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	body := "---\n" +
		"name: " + name + "\n" +
		"description: detailed test skill\n" +
		"license: MIT\n" +
		"metadata:\n" +
		"  author: test-org\n" +
		"  tags: deploy,demo\n" +
		"---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "run.sh"), []byte("echo hi"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "spec.md"), []byte("ref"), 0o644); err != nil {
		t.Fatalf("write ref: %v", err)
	}
	return dir
}

func TestBuildSkillInfo_FullSkill(t *testing.T) {
	wt := writeFullSkill(t, "demo")
	project := t.TempDir()
	linkSkillInto(t, project, ".claude/skills", "demo", wt)

	// v5 link install: Source carries the absolute skill dir, Ref="local"
	// is the link marker so EffectiveTarget returns Source directly.
	entry := &model.LockEntry{
		Name:    "demo",
		Source:  wt,
		Ref:     "local",
		Targets: []string{"claude"},
	}

	info, err := buildSkillInfo(entry, project, false)
	if err != nil {
		t.Fatalf("buildSkillInfo: %v", err)
	}
	if info.Name != "demo" || info.Description != "detailed test skill" {
		t.Errorf("frontmatter not propagated: %+v", info)
	}
	if info.License != "MIT" {
		t.Errorf("license = %q, want MIT", info.License)
	}
	if info.Metadata["author"] != "test-org" || info.Metadata["tags"] != "deploy,demo" {
		t.Errorf("metadata not propagated: %v", info.Metadata)
	}
	wantFiles := []string{"SKILL.md", "references/spec.md", "scripts/run.sh"}
	gotFiles := strings.Join(info.Files, ",")
	for _, want := range wantFiles {
		if !strings.Contains(gotFiles, want) {
			t.Errorf("expected %q in files, got %v", want, info.Files)
		}
	}
	if len(info.Targets) != 1 || info.Targets[0] != "claude" {
		t.Errorf("expected one target 'claude', got %+v", info.Targets)
	}
	if len(info.TargetDetails) != 1 || info.TargetDetails[0].Target != "claude" || !info.TargetDetails[0].OK {
		t.Errorf("expected one OK target detail for claude, got %+v", info.TargetDetails)
	}
}

func TestBuildSkillInfo_BrokenSymlinkReportsError(t *testing.T) {
	intendedSrc := writeFullSkill(t, "demo")
	project := t.TempDir()

	// Symlink points at a *different* dir than the lock entry expects, so
	// the target-status check should flag a mismatch.
	otherSrc := writeFullSkill(t, "demo")
	linkSkillInto(t, project, ".claude/skills", "demo", otherSrc)

	entry := &model.LockEntry{
		Name:    "demo",
		Source:  intendedSrc,
		Ref:     "local",
		Targets: []string{"claude"},
	}
	info, err := buildSkillInfo(entry, project, false)
	if err != nil {
		t.Fatalf("buildSkillInfo: %v", err)
	}
	if len(info.TargetDetails) != 1 {
		t.Fatalf("expected 1 target detail, got %d", len(info.TargetDetails))
	}
	if info.TargetDetails[0].OK {
		t.Errorf("symlink mismatch should not be OK: %+v", info.TargetDetails[0])
	}
	if info.TargetDetails[0].Error == "" {
		t.Errorf("expected an error message, got empty string")
	}
}

// Mirrors the real layout of a registry-installed skill: a bare worktree root
// with SKILL.md living under a `skills/<name>/` sub-path. Issue #16: info was
// calling LoadFromPath(worktree) instead of joining entry.Path, so frontmatter
// came back empty for every multi-skill registry.
func TestBuildSkillInfo_LoadsFrontmatterFromSkillPath(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	reg, name, commit := "acme", "deploy-to-cloud", "abc1234"
	worktree := registry.WorktreePath(reg, name, registry.ShortSHA(commit))
	skillRel := filepath.Join("skills", "deploy-to-cloud")
	skillDir := filepath.Join(worktree, skillRel)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: deploy-to-cloud\ndescription: Deploy to Acme\n---\n# deploy\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	entry := &model.LockEntry{
		Name:     name,
		Registry: reg,
		Source:   "git@example.test:" + reg + ".git",
		Ref:      "main",
		Commit:   commit,
		Path:     skillRel,
		Targets:  []string{"claude"},
	}
	info, err := buildSkillInfo(entry, t.TempDir(), false)
	if err != nil {
		t.Fatalf("buildSkillInfo: %v", err)
	}
	if info.Description != "Deploy to Acme" {
		t.Errorf("description = %q, want %q", info.Description, "Deploy to Acme")
	}
}

// Linked skills have no worktree/branch/commit; info should carry LinkTarget
// and the render path should suppress the empty git-state rows rather than
// printing blank columns.
func TestBuildSkillInfo_LinkedSkill(t *testing.T) {
	src := writeFullSkill(t, "demo")
	project := t.TempDir()
	linkSkillInto(t, project, ".claude/skills", "demo", src)

	entry := &model.LockEntry{
		Name:    "demo",
		Source:  src,
		Ref:     "local",
		Targets: []string{"claude"},
	}
	info, err := buildSkillInfo(entry, project, false)
	if err != nil {
		t.Fatalf("buildSkillInfo: %v", err)
	}
	if info.Source != src {
		t.Errorf("LinkTarget = %q, want %q", info.Source, src)
	}
	if info.Ref != "" || info.Commit != "" || info.Worktree != "" {
		t.Errorf("link entry should have empty git state, got %+v", info)
	}
	if info.Description != "detailed test skill" {
		t.Errorf("description not loaded from link target: %q", info.Description)
	}
}

// v6: SubtreeHash lives at the top level of LockEntry; scan and provenance
// ride directly on the entry. Confirm both surface through buildSkillInfo's
// JSON output.
func TestBuildSkillInfo_PropagatesSubtreeHashAndScan(t *testing.T) {
	_ = writeFullSkill(t, "demo")
	project := t.TempDir()
	entry := &model.LockEntry{
		Name:        "demo",
		Registry:    "raks",
		Source:      "https://example.invalid/raks.git",
		Ref:         "v0.2.0",
		Targets:     []string{"claude"},
		SubtreeHash: "sha256:abc123",
		Scan: &model.ScanRef{
			ReportSHA:      "sha256:scan",
			ScannerVersion: "0.5.2",
			Decision:       "allowed",
			Counts:         model.SeverityCounts{High: 1},
		},
	}
	info, err := buildSkillInfo(entry, project, false)
	if err != nil {
		t.Fatalf("buildSkillInfo: %v", err)
	}
	if info.SubtreeHash != "sha256:abc123" {
		t.Errorf("SubtreeHash lost: %q", info.SubtreeHash)
	}
	if info.Scan == nil {
		t.Fatal("Scan dropped")
	}
	if info.Scan.Decision != "allowed" {
		t.Errorf("Scan.Decision lost: %q", info.Scan.Decision)
	}
}

func TestBuildSkillInfo_TargetWithNoSymlinkReportsError(t *testing.T) {
	src := writeFullSkill(t, "demo")
	project := t.TempDir()
	// No linkSkillInto — symlinks intentionally missing so the check fails.
	entry := &model.LockEntry{
		Name:    "demo",
		Source:  src,
		Ref:     "local",
		Targets: []string{"claude", "cursor"},
	}
	info, err := buildSkillInfo(entry, project, false)
	if err != nil {
		t.Fatalf("buildSkillInfo: %v", err)
	}
	if len(info.Targets) != 2 || info.Targets[0] != "claude" || info.Targets[1] != "cursor" {
		t.Fatalf("expected targets [claude cursor], got %v", info.Targets)
	}
	if len(info.TargetDetails) != 2 {
		t.Fatalf("expected 2 target details, got %d", len(info.TargetDetails))
	}
	for _, ts := range info.TargetDetails {
		if ts.OK {
			t.Errorf("no symlinks were created; %s should not be OK", ts.Target)
		}
		if ts.Error == "" {
			t.Errorf("expected error for %s", ts.Target)
		}
	}
}

// TestBuildSkillInfo_RefFieldNotBranch is the #123 regression. Pre-fix
// the skillInfo struct exposed a Branch field with json tag "branch",
// which mislabelled tag installs ("Branch: v0.2.0") in text and diverged
// from qvr list --output json's ref field. We now use Ref everywhere —
// matches the lockfile schema and stays kind-agnostic so semver tags
// don't read as branches.
func TestBuildSkillInfo_RefFieldNotBranch(t *testing.T) {
	wt := writeFullSkill(t, "tagged")
	project := t.TempDir()
	linkSkillInto(t, project, ".claude/skills", "tagged", wt)

	entry := &model.LockEntry{
		Name:    "tagged",
		Source:  "git@example.test:r.git",
		Ref:     "v0.2.0", // tag, not a branch
		Commit:  "abc1234",
		Targets: []string{"claude"},
	}
	info, err := buildSkillInfo(entry, project, false)
	if err != nil {
		t.Fatalf("buildSkillInfo: %v", err)
	}
	if info.Ref != "v0.2.0" {
		t.Errorf("info.Ref = %q, want %q", info.Ref, "v0.2.0")
	}

	// Render text mode and confirm we no longer print "Branch: v0.2.0".
	resetPrinter(t)
	renderInfoText(info)
	outBuf, ok := printer.Out.(interface{ String() string })
	if !ok {
		t.Fatalf("printer.Out is not a stringer; got %T", printer.Out)
	}
	got := outBuf.String()
	if strings.Contains(got, "Branch:") {
		t.Errorf("text output still uses Branch: label for a tagged install:\n%s", got)
	}
	if !strings.Contains(got, "Ref:") || !strings.Contains(got, "v0.2.0") {
		t.Errorf("text output missing Ref: row for tagged install:\n%s", got)
	}
}

// TestBuildSkillInfo_EditModeTargetIsCanonicalDir is the #117 follow-up
// regression on the info surface. For edit-mode entries (qvr create /
// qvr edit) the canonical target dir IS a real directory — the eject
// dir itself — not a symlink at the shared worktree. info used to run
// VerifyTarget on it, which expects a symlink, so every ejected
// canonical reported as
//
//	✗ claude  symlink path already exists and is not a symlink
//
// in text and `targetDetails[0].ok = false` in JSON. The fix mirrors
// the ejected-check path doctor already uses (cmd/doctor.go
// checkSymlink → VerifyDirContainsSkill).
func TestBuildSkillInfo_EditModeTargetIsCanonicalDir(t *testing.T) {
	project := t.TempDir()
	editRel := filepath.Join(".claude", "skills", "demo")
	editAbs := filepath.Join(project, editRel)
	if err := os.MkdirAll(editAbs, 0o755); err != nil {
		t.Fatalf("mkdir edit dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(editAbs, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: edit-mode info\n---\n# demo\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	entry := &model.LockEntry{
		Name:     "demo",
		Mode:     model.ModeEdit,
		EditPath: editRel,
		Ref:      "main",
		Targets:  []string{"claude"},
	}
	info, err := buildSkillInfo(entry, project, false)
	if err != nil {
		t.Fatalf("buildSkillInfo: %v", err)
	}
	if len(info.TargetDetails) != 1 {
		t.Fatalf("expected 1 target detail, got %d", len(info.TargetDetails))
	}
	td := info.TargetDetails[0]
	if !td.OK {
		t.Errorf("edit-mode canonical target detail OK=false (#117 follow-up): %+v", td)
	}
	if td.Error != "" {
		t.Errorf("edit-mode canonical target detail unexpected error: %q (#117 follow-up — info should mirror doctor's ejected check, not VerifyTarget)", td.Error)
	}
}

// TestRenderInfoText_EditModeSourceLabel is the #117 follow-up
// regression for the info text surface. Pre-fix `Source:` rendered the
// raw entry.Source value, so a `qvr edit`-ejected entry painted as
// `Source: https://github.com/foo/bar.git` — same misleading column
// as list before mode took precedence. Now `Source: edit` and the
// upstream URL moves to a dedicated `Upstream:` row so the provenance
// stays on the card without pretending the URL is still the source of
// truth.
func TestRenderInfoText_EditModeSourceLabel(t *testing.T) {
	resetPrinter(t)
	info := &skillInfo{
		Name:           "code-review",
		Registry:       "raks",
		Ref:            "v0.2.0",
		Commit:         "cf452e1b58135e55b3f9295fcf1e7c94c69ad6ee",
		Mode:           "edit",
		EditPath:       ".claude/skills/code-review",
		Source:         "https://github.com/astra-sh/qvr_playground.git",
		SourceUpstream: "https://github.com/astra-sh/qvr_playground.git",
	}
	renderInfoText(info)
	outBuf, ok := printer.Out.(interface{ String() string })
	if !ok {
		t.Fatalf("printer.Out is not a stringer; got %T", printer.Out)
	}
	got := outBuf.String()

	if strings.Contains(got, "Source:      https://github.com/astra-sh/qvr_playground.git") {
		t.Errorf("info text still shows upstream URL in Source row for edit-mode entry — issue #117 follow-up:\n%s", got)
	}
	if !strings.Contains(got, "Source:      edit") {
		t.Errorf("info text missing 'Source: edit' row for edit-mode entry:\n%s", got)
	}
	if !strings.Contains(got, "EditPath:    .claude/skills/code-review") {
		t.Errorf("info text missing EditPath row for edit-mode entry:\n%s", got)
	}
	if !strings.Contains(got, "Upstream:    https://github.com/astra-sh/qvr_playground.git") {
		t.Errorf("info text missing Upstream row preserving the original URL:\n%s", got)
	}
}

// TestSkillInfoJSONShape_MatchesListSchema is the #116 regression.
// Pre-fix info emitted snake_case (`subtree_hash`, `allowed_tools`,
// `link_target`, `commit_drift`) and a `targets: [{target,path,ok}]`
// shape, while `qvr list --output json` emitted camelCase + a plain
// `targets: ["claude"]` array. The fix aligns the field names and
// shape so a list→info walker can use one parser.
func TestSkillInfoJSONShape_MatchesListSchema(t *testing.T) {
	entry := &model.LockEntry{
		Name:          "demo",
		Registry:      "raks",
		Source:        "git@github.com:raks097/demo.git",
		Provenance:    &model.ProvenanceRef{Upstream: "git@github.com:raks097/demo.git"},
		Path:          "skills/demo",
		Ref:           "v0.2.0",
		Commit:        "abc1234567890abcdef1234567890abcdef12345",
		InstallCommit: "abc1234567890abcdef1234567890abcdef12345",
		SubtreeHash:   "sha256:deadbeef",
		Mode:          "",
		Targets:       []string{"claude"},
		InstalledAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	info, err := buildSkillInfo(entry, t.TempDir(), false)
	if err != nil {
		t.Fatalf("buildSkillInfo: %v", err)
	}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(b)

	// camelCase keys we want, snake_case keys we don't.
	wantKeys := []string{
		`"subtreeHash"`, `"sourceUpstream"`, `"installCommit"`,
		`"installedAt"`, `"ref"`,
	}
	for _, k := range wantKeys {
		if !strings.Contains(body, k) {
			t.Errorf("info JSON missing %s — list schema requires it (#116):\n%s", k, body)
		}
	}
	for _, k := range []string{
		`"subtree_hash"`, `"allowed_tools"`, `"link_target"`,
		`"commit_drift"`, `"branch"`,
	} {
		if strings.Contains(body, k) {
			t.Errorf("info JSON still emits legacy snake_case key %s — should be camelCase (#116):\n%s", k, body)
		}
	}

	// Targets is the plain string array (matching list); per-target
	// link status lives under targetDetails.
	type minimal struct {
		Targets       []string       `json:"targets"`
		TargetDetails []targetStatus `json:"targetDetails"`
	}
	var got minimal
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Targets) != 1 || got.Targets[0] != "claude" {
		t.Errorf("targets = %v, want [\"claude\"] (list schema)", got.Targets)
	}
	if len(got.TargetDetails) != 1 || got.TargetDetails[0].Target != "claude" {
		t.Errorf("targetDetails = %+v, want one entry for claude", got.TargetDetails)
	}
}

// TestBuildSkillInfo_RootLayoutHonorsAgentView is the #170 regression: a
// consumed root-layout skill (path=".") legitimately symlinks the sanitized
// .git/qvr-view, not the worktree root (issue #154). doctor/status/list/lock
// verify honor that redirect; info verified against the bare worktree and
// false-flagged every such install as "symlink target mismatch". buildSkillInfo
// must verify against AgentLinkTarget so the target reads OK, while still
// loading frontmatter from the real worktree root.
func TestBuildSkillInfo_RootLayoutHonorsAgentView(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	reg, name, commit := "acme", "root-demo", "abc1234"
	worktree := registry.WorktreePath(reg, name, registry.ShortSHA(commit))

	// Worktree root IS the skill (SKILL.md at top) — path=".".
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	body := "---\nname: root-demo\ndescription: Root layout skill\n---\n# root-demo\n"
	if err := os.WriteFile(filepath.Join(worktree, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	// The sanitized agent view the installer points the symlink at.
	view := filepath.Join(worktree, ".git", "qvr-view")
	if err := os.MkdirAll(view, 0o755); err != nil {
		t.Fatalf("mkdir view: %v", err)
	}
	if err := os.WriteFile(filepath.Join(view, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write view skill: %v", err)
	}

	entry := &model.LockEntry{
		Name:          name,
		Registry:      reg,
		Commit:        commit,
		InstallCommit: commit,
		Path:          ".",
		Ref:           "main",
		Targets:       []string{"claude"},
	}

	project := t.TempDir()
	linkSkillInto(t, project, ".claude/skills", name, view)

	info, err := buildSkillInfo(entry, project, false)
	if err != nil {
		t.Fatalf("buildSkillInfo: %v", err)
	}
	if len(info.TargetDetails) != 1 {
		t.Fatalf("expected 1 target detail, got %d", len(info.TargetDetails))
	}
	if !info.TargetDetails[0].OK {
		t.Errorf("root-layout target should verify against .git/qvr-view, got %+v (issue #170)", info.TargetDetails[0])
	}
	if info.Description != "Root layout skill" {
		t.Errorf("description = %q, want frontmatter loaded from the worktree root", info.Description)
	}
}
