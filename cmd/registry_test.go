package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/spf13/cobra"
)

// TestRejectWebURL covers the GitHub/GitLab/Bitbucket web-browse URLs
// (`/tree/<ref>/<path>` and `/blob/<ref>/<path>`) that look clone-shaped
// but can't actually be cloned. Each rejection has to carry the
// "register the repo, then `qvr add <skill>`" hint so the user knows
// the v4 flow.
func TestRejectWebURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantErr  bool
		wantHint string
	}{
		{
			name:     "github tree URL rejected with registry-add hint",
			url:      "https://github.com/acme/skills/tree/main/skills/foo",
			wantErr:  true,
			wantHint: "qvr registry add",
		},
		{
			name:     "github blob URL rejected with registry-add hint",
			url:      "https://github.com/acme/skills/blob/main/skills/foo/SKILL.md",
			wantErr:  true,
			wantHint: "qvr registry add",
		},
		{
			name:    "gitlab tree URL rejected",
			url:     "https://gitlab.com/owner/repo/tree/main/sub",
			wantErr: true,
		},
		{
			name:    "bitbucket blob URL rejected",
			url:     "https://bitbucket.org/owner/repo/blob/main/sub",
			wantErr: true,
		},
		{
			name:    "plain clone URL passes through",
			url:     "https://github.com/owner/repo.git",
			wantErr: false,
		},
		{
			name:    "scp-style ssh URL passes through",
			url:     "git@github.com:owner/repo.git",
			wantErr: false,
		},
		{
			name:    "non-host path passes through (no host means rejectWebURL bails)",
			url:     "/tmp/local/bare.git",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rejectWebURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("rejectWebURL(%q) err=%v, wantErr=%v", tt.url, err, tt.wantErr)
			}
			if tt.wantHint != "" && !strings.Contains(err.Error(), tt.wantHint) {
				t.Errorf("error %q missing hint %q", err.Error(), tt.wantHint)
			}
		})
	}
}

func TestRegistryTrustSummary(t *testing.T) {
	reg := &model.Registry{Name: "acme/skills", SkillCount: 3}
	cfg := &config.Config{Security: config.SecurityConfig{
		ScanOnInstall: true,
		RequireScan:   true,
		RequireSigned: true,
	}}
	signals := registryOwnerSignals{AccountAge: "2020-01-02", LastActivity: "2026-06-01", Followers: "42", PublicRepos: "7"}
	got := registryTrustSummary(reg, cfg, signals)
	for _, want := range []string{"owner acme", "account age 2020-01-02", "last activity 2026-06-01", "followers 42", "public repos 7", "skills 3", "scans required", "signatures required"} {
		if !strings.Contains(got, want) {
			t.Fatalf("registryTrustSummary() = %q, want %q", got, want)
		}
	}
}

func TestRegistryAddKeepsRegistryWhenScanFlagsSkill(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	resetPrinter(t)

	cfg := config.Default()
	cfg.Security.ScanOnInstall = true
	cfg.Security.BlockSeverity = "critical"
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	repo := seedRegistryRepo(t, map[string]string{
		"skills/leaky/SKILL.md": `---
name: leaky
description: trips the registry advisory scan
---
# Leaky

Fixture credential: AKIAIOSFODNN7EXAMPLE
`,
	})

	registryAddName = "local/leaky"
	registryAddNoScan = false
	t.Cleanup(func() {
		registryAddName = ""
		registryAddNoScan = false
	})

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := runRegistryAdd(cmd, []string{"file://" + repo}); err != nil {
		t.Fatalf("registry add should keep advisory-scan registries: %v", err)
	}

	mgr := newRegistryManager(git.NewGoGitClient())
	regs, err := mgr.List()
	if err != nil {
		t.Fatalf("list registries: %v", err)
	}
	if len(regs) != 1 || regs[0].Name != "local/leaky" || regs[0].SkillCount != 1 {
		t.Fatalf("registry not kept with indexed skill: %+v", regs)
	}

	stringer, ok := printer.Err.(interface{ String() string })
	if !ok {
		t.Fatalf("printer.Err is not a String()-capable buffer")
	}
	stderr := stringer.String()
	if !strings.Contains(stderr, "scan flagged 1 skill") {
		t.Fatalf("expected advisory scan warning, got stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "registry kept") {
		t.Fatalf("expected registry-kept wording, got stderr:\n%s", stderr)
	}
}

func seedRegistryRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		if _, err := wt.Add(rel); err != nil {
			t.Fatalf("add %s: %v", rel, err)
		}
	}
	_, err = wt.Commit("initial", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), head.Hash(),
	)); err != nil {
		t.Fatalf("set main: %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName("main"),
	)); err != nil {
		t.Fatalf("set HEAD: %v", err)
	}
	return dir
}
