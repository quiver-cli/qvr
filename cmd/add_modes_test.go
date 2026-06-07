package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
)

// resetAddModeFlags zeroes every `qvr add` package-level flag and restores it
// on cleanup, so mode tests don't leak state into each other.
func resetAddModeFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		addTargets = nil
		addGlobal = false
		addForce = false
		addFrozen = false
		addNoScan = false
		addRegistry = ""
		addAs = ""
		addAll = false
		addLocal = ""
	})
}

func newAddCmd() *cobra.Command {
	c := &cobra.Command{Use: "add"}
	c.SetContext(context.Background())
	return c
}

// TestValidateAddModes exercises the mutual-exclusion matrix for --all/--local
// as a pure unit (no registry or filesystem needed).
func TestValidateAddModes(t *testing.T) {
	tests := []struct {
		name     string
		all      bool
		local    string
		registry string
		as       string
		frozen   bool
		wantErr  string
	}{
		{name: "plain add ok"},
		{name: "all with registry ok", all: true, registry: "acme"},
		{name: "local ok", local: "./skill"},
		{name: "all and local exclusive", all: true, local: "./x", registry: "acme", wantErr: "mutually exclusive"},
		{name: "all needs registry", all: true, wantErr: "--all requires --registry"},
		{name: "all rejects as", all: true, registry: "acme", as: "x", wantErr: "--as cannot be combined with --all"},
		{name: "local rejects as", local: "./x", as: "y", wantErr: "--as cannot be combined with --local"},
		{name: "local rejects registry", local: "./x", registry: "acme", wantErr: "--registry cannot be combined with --local"},
		{name: "local rejects frozen", local: "./x", frozen: true, wantErr: "--frozen cannot be combined with --local"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetAddModeFlags(t)
			addAll = tc.all
			addLocal = tc.local
			addRegistry = tc.registry
			addAs = tc.as
			addFrozen = tc.frozen

			err := validateAddModes()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateAddModes() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateAddModes() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want containing %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestAddArgs_ModesRejectSkillNames confirms the custom Args validator gives a
// mode-specific message (issue: generic cobra `unknown command "<arg>"`) when a
// skill name is passed alongside --all or --local.
func TestAddArgs_ModesRejectSkillNames(t *testing.T) {
	cases := []struct {
		name string
		all  bool
		loc  string
		want string
	}{
		{name: "all with skill", all: true, want: "--all installs every skill"},
		{name: "local with skill", loc: "./x", want: "--local takes the folder path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetAddModeFlags(t)
			addAll = tc.all
			addLocal = tc.loc
			err := addCmd.Args(addCmd, []string{"someskill"})
			if err == nil {
				t.Fatal("expected Args validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want containing %q", err.Error(), tc.want)
			}
		})
	}
}

// TestRunAddAll_InstallsEverySkill seeds a multi-skill registry and confirms
// `qvr add --all --registry <name>` lands every skill in the lock.
func TestRunAddAll_InstallsEverySkill(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetAddModeFlags(t)
	resetPrinter(t)

	repo := seedRegistryRepo(t, map[string]string{
		"skills/alpha/SKILL.md": "---\nname: alpha\ndescription: first skill\n---\n# alpha\n",
		"skills/beta/SKILL.md":  "---\nname: beta\ndescription: second skill\n---\n# beta\n",
		"skills/gamma/SKILL.md": "---\nname: gamma\ndescription: third skill\n---\n# gamma\n",
	})

	mgr := newRegistryManager(git.NewGoGitClient())
	if _, err := mgr.Add(context.Background(), "acme", "file://"+repo); err != nil {
		t.Fatalf("registry add: %v", err)
	}

	addAll = true
	addRegistry = "acme"
	if err := runAdd(newAddCmd(), nil); err != nil {
		t.Fatalf("runAdd --all: %v", err)
	}

	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if _, err := lock.Get(name); err != nil {
			t.Errorf("skill %q missing from lock after --all: %v", name, err)
		}
	}
	if got := len(lock.Skills); got != 3 {
		t.Errorf("lock has %d skills, want 3", got)
	}
}

// TestRunAddAll_UnconfiguredRegistry confirms --all on a registry that isn't
// configured fails with an actionable error rather than installing nothing.
func TestRunAddAll_UnconfiguredRegistry(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetAddModeFlags(t)
	resetPrinter(t)

	addAll = true
	addRegistry = "ghost"
	err := runAdd(newAddCmd(), nil)
	if err == nil {
		t.Fatal("expected error for --all on unconfigured registry")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("error = %q, want 'not configured'", err.Error())
	}
}

// TestRunAddLocal_ImmutableCopy confirms `qvr add --local <path>` installs a
// frozen copy (not a live symlink) and records a mode:local lock entry.
func TestRunAddLocal_ImmutableCopy(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetAddModeFlags(t)
	resetPrinter(t)

	src := filepath.Join(t.TempDir(), "my-skill")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: my-skill\ndescription: local dev skill\n---\n# my-skill\n"
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	addLocal = src
	if err := runAdd(newAddCmd(), nil); err != nil {
		t.Fatalf("runAdd --local: %v", err)
	}

	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("my-skill")
	if err != nil {
		t.Fatalf("my-skill missing from lock: %v", err)
	}
	if !entry.IsLocal() {
		t.Errorf("entry.IsLocal() = false; mode = %q", entry.Mode)
	}
	absSrc, _ := filepath.Abs(src)
	if entry.Source != absSrc {
		t.Errorf("entry.Source = %q, want %q", entry.Source, absSrc)
	}

	// The symlink must point at the immutable copy, not the live source.
	link := filepath.Join(project, ".claude/skills/my-skill")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target == absSrc {
		t.Errorf("symlink points at live source; expected an immutable copy")
	}

	// Editing the source must not change what the agent reads.
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(body+"\nmutated\n"), 0o644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	if string(got) != body {
		t.Errorf("installed copy changed after editing source — not immutable")
	}
}

// TestRunAddLocal_ReAddAfterEditRefreshes guards the content-addressing key:
// editing the source folder and re-running `qvr add --local` must land the new
// content in a fresh worktree dir (the symlink repoints), not silently reuse a
// stale dir keyed on a colliding short hash.
func TestRunAddLocal_ReAddAfterEditRefreshes(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetAddModeFlags(t)
	resetPrinter(t)

	src := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	v1 := "---\nname: demo\ndescription: version one\n---\n# demo v1\n"
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}

	addLocal = src
	if err := runAdd(newAddCmd(), nil); err != nil {
		t.Fatalf("first add --local: %v", err)
	}
	link := filepath.Join(func() string { p, _ := os.Getwd(); return p }(), ".claude/skills/demo")
	t1, _ := os.Readlink(link)

	// Edit the source and re-add (same path, changed content).
	v2 := "---\nname: demo\ndescription: version two\n---\n# demo v2 changed\n"
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	resetAddModeFlags(t)
	addLocal = src
	if err := runAdd(newAddCmd(), nil); err != nil {
		t.Fatalf("re-add --local: %v", err)
	}
	t2, _ := os.Readlink(link)

	if t1 == t2 {
		t.Errorf("re-add reused the same worktree dir %s; changed content must get a fresh dir", t2)
	}
	got, err := os.ReadFile(filepath.Join(t2, "SKILL.md"))
	if err != nil {
		t.Fatalf("read refreshed copy: %v", err)
	}
	if string(got) != v2 {
		t.Errorf("installed copy = %q, want refreshed v2 content", string(got))
	}
}
