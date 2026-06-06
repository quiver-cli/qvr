package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOrderedTargets_Default(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	got := orderedTargets()
	if len(got) == 0 || got[0] != "claude" {
		t.Errorf("expected canonical order with claude first, got %v", got)
	}
	if len(got) != 6 {
		t.Errorf("expected 6 targets, got %d", len(got))
	}
}

// linkSkillInto symlinks a skill directory into projectRoot/<target>/<name>,
// the on-disk shape `qvr add` produces. Shared by the command tests that
// exercise target resolution (ls, info, doctor, disable/enable).
func linkSkillInto(t *testing.T, projectRoot, target, name, src string) {
	t.Helper()
	linkParent := filepath.Join(projectRoot, target)
	if err := os.MkdirAll(linkParent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.Symlink(src, filepath.Join(linkParent, name)); err != nil {
		t.Fatalf("symlink: %v", err)
	}
}
