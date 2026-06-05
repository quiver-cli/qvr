package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quiver-cli/qvr/internal/manifest"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/output"
)

// seedExportLock writes a v5 lock with a deterministic mix of entries:
// two normal (one with a canonical/alias mapping), one link, one edit.
// Returns the project root the lock lives in.
func seedExportLock(t *testing.T) string {
	t.Helper()
	t.Setenv("QUIVER_HOME", t.TempDir())
	project := t.TempDir()
	t.Chdir(project)

	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	lock.Put(&model.LockEntry{
		Name:        "code-review",
		Registry:    "raks",
		Source:      "https://github.com/quiver-cli/qvr_playground.git",
		Ref:         "v0.2.0",
		Commit:      "94e539be7d6a01774d723a7c25513af0f070de7b",
		SubtreeHash: "sha256:aaa",
		Targets:     []string{"claude"},
		InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	lock.Put(&model.LockEntry{
		Name:        "cr-old",
		Canonical:   "code-review",
		Registry:    "raks",
		Source:      "https://github.com/quiver-cli/qvr_playground.git",
		Ref:         "v0.1.0",
		Commit:      "deadbeef0000000000000000000000000000beef",
		SubtreeHash: "sha256:bbb",
		Targets:     []string{"claude", "cursor"},
		InstalledAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	lock.Put(&model.LockEntry{
		Name:        "deploy-to-cloud",
		Registry:    "acme-labs/agent-skills",
		Source:      "https://github.com/acme-labs/agent-skills.git",
		Ref:         "main",
		Commit:      "feedface0000000000000000000000000000face",
		SubtreeHash: "sha256:ccc",
		Targets:     []string{"claude"},
		InstalledAt: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
	})
	// Link install — must be skipped by export.
	lock.Put(&model.LockEntry{
		Name:        "local-helper",
		Source:      "/tmp/local",
		Ref:         "local",
		SubtreeHash: "sha256:ddd",
		Targets:     []string{"claude"},
		InstalledAt: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC),
	})
	// Edit install — must also be skipped (lives in the project).
	lock.Put(&model.LockEntry{
		Name:        "in-progress",
		Source:      "https://github.com/quiver-cli/qvr_playground.git",
		Ref:         "main",
		Mode:        model.ModeEdit,
		EditPath:    ".claude/skills/in-progress",
		Commit:      "abc1234",
		SubtreeHash: "sha256:eee",
		Targets:     []string{"claude"},
		InstalledAt: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	return project
}

// resetExportFlags pins the export command's package-level flags so a prior
// test's settings don't bleed in.
func resetExportFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		exportOutputFile = ""
		exportFrozen = false
		exportIncludeAliases = false
		exportIncludeLocal = false
		exportGlobal = false
	})
	exportOutputFile = ""
	exportFrozen = false
	exportIncludeAliases = false
	exportIncludeLocal = false
	exportGlobal = false
}

// runExportCapturing runs the export command and returns stdout + stderr.
func runExportCapturing(t *testing.T) (string, string) {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: stdout, Err: stderr, Format: output.FormatText}
	t.Cleanup(func() { printer = prev })

	// Re-route cmd output to the same stdout buffer so the rendered manifest
	// (which writes via cmd.OutOrStdout) lands somewhere the test can read.
	exportCmd.SetOut(stdout)
	t.Cleanup(func() { exportCmd.SetOut(os.Stdout) })

	if err := runExport(exportCmd, nil); err != nil {
		t.Fatalf("runExport: %v", err)
	}
	return stdout.String(), stderr.String()
}

func TestRunExport_PortableSkillsOnly_SkipsLinkAndEdit(t *testing.T) {
	seedExportLock(t)
	resetExportFlags(t)

	stdout, stderr := runExportCapturing(t)

	// Lock has 3 portable + 1 link + 1 edit. The text manifest should list
	// only the 3 portable ones; the link + edit get a stderr warning.
	entries, perrs, err := manifest.Parse(strings.NewReader(stdout))
	if err != nil {
		t.Fatalf("parse exported manifest: %v", err)
	}
	if len(perrs) != 0 {
		t.Fatalf("export emitted unparseable lines: %v", perrs)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d: %#v", len(entries), entries)
	}
	if !strings.Contains(stderr, "excluded 2 non-portable") {
		t.Errorf("expected exclusion warning on stderr; got %q", stderr)
	}
	if !strings.Contains(stderr, "local-helper") || !strings.Contains(stderr, "in-progress") {
		t.Errorf("exclusion warning should name the skipped skills; got %q", stderr)
	}
}

func TestRunExport_FrozenPinsCommit(t *testing.T) {
	seedExportLock(t)
	resetExportFlags(t)
	exportFrozen = true

	stdout, _ := runExportCapturing(t)

	entries, _, err := manifest.Parse(strings.NewReader(stdout))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Every entry must carry a non-empty commit pin.
	for _, e := range entries {
		if e.Commit == "" {
			t.Errorf("entry %s missing --commit pin under --frozen", e.Skill)
		}
	}
}

func TestRunExport_Default_NoCommitPins(t *testing.T) {
	seedExportLock(t)
	resetExportFlags(t)

	stdout, _ := runExportCapturing(t)

	entries, _, err := manifest.Parse(strings.NewReader(stdout))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, e := range entries {
		if e.Commit != "" {
			t.Errorf("entry %s got --commit=%q without --frozen", e.Skill, e.Commit)
		}
	}
}

func TestRunExport_IncludeAliases_EmitsRegistryAlias(t *testing.T) {
	seedExportLock(t)
	resetExportFlags(t)
	exportIncludeAliases = true

	stdout, _ := runExportCapturing(t)

	if !strings.Contains(stdout, "--registry-alias=raks") {
		t.Errorf("--include-aliases should emit registry-alias= flag; got %q", stdout)
	}
}

func TestRunExport_AliasInstall_EmitsAsFlag(t *testing.T) {
	seedExportLock(t)
	resetExportFlags(t)

	stdout, _ := runExportCapturing(t)
	// The "cr-old" entry has Canonical="code-review" — export must surface
	// this as --as=cr-old on the canonical line.
	if !strings.Contains(stdout, "--as=cr-old") {
		t.Errorf("expected --as=cr-old for the canonical/alias-mapped entry; got %q", stdout)
	}
}

func TestRunExport_IncludeLocal_EmitsCommentedLines(t *testing.T) {
	seedExportLock(t)
	resetExportFlags(t)
	exportIncludeLocal = true

	stdout, _ := runExportCapturing(t)
	if !strings.Contains(stdout, "# local: local-helper") {
		t.Errorf("--include-local should emit # local: line for link install; got %q", stdout)
	}
	if !strings.Contains(stdout, "# local: in-progress") {
		t.Errorf("--include-local should emit # local: line for edit install; got %q", stdout)
	}
	// The comment lines must not be parsed as entries by manifest.Parse.
	entries, perrs, err := manifest.Parse(strings.NewReader(stdout))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(perrs) != 0 {
		t.Fatalf("--include-local block introduced parse errors: %v", perrs)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 portable entries, got %d", len(entries))
	}
}

func TestRunExport_OutputFile_WritesToPath(t *testing.T) {
	project := seedExportLock(t)
	resetExportFlags(t)
	out := filepath.Join(project, "skills.txt")
	exportOutputFile = out

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: stdout, Err: stderr, Format: output.FormatText}
	t.Cleanup(func() { printer = prev })
	exportCmd.SetOut(stdout)
	t.Cleanup(func() { exportCmd.SetOut(os.Stdout) })

	if err := runExport(exportCmd, nil); err != nil {
		t.Fatalf("runExport: %v", err)
	}
	// Stdout must be empty (manifest went to the file).
	if stdout.Len() > 0 {
		t.Errorf("--output-file should keep stdout empty; got %q", stdout.String())
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	entries, _, err := manifest.Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parse output file: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("file expected 3 entries, got %d", len(entries))
	}
}

func TestRunExport_JSON_EmitsStructuredEntries(t *testing.T) {
	seedExportLock(t)
	resetExportFlags(t)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: stdout, Err: stderr, Format: output.FormatJSON}
	t.Cleanup(func() { printer = prev })
	exportCmd.SetOut(stdout)
	t.Cleanup(func() { exportCmd.SetOut(os.Stdout) })

	if err := runExport(exportCmd, nil); err != nil {
		t.Fatalf("runExport: %v", err)
	}
	var got exportPayload
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, stdout.String())
	}
	if len(got.Entries) != 3 {
		t.Fatalf("expected 3 portable entries; got %d", len(got.Entries))
	}
	if len(got.Excluded) != 2 {
		t.Fatalf("expected 2 excluded; got %v", got.Excluded)
	}
}
