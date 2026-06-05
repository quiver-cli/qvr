package cmd

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/output"
)

// writeSingleSkillLock seeds a lock file at path with a single skill entry.
func writeSingleSkillLock(t *testing.T, path, name string) {
	t.Helper()
	lock := model.NewLockFile(path)
	lock.Put(&model.LockEntry{Name: name, Registry: "raks", Source: "git@example.test:raks.git", Ref: "main", Commit: "abc1234"})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock %s: %v", path, err)
	}
}

// TestEdit_GlobalSkillRefusedWithGuidance verifies `qvr edit` does not eject a
// globally installed skill in place; instead it steers the user to the
// publish → re-add-globally workflow (#edit-local-only).
func TestEdit_GlobalSkillRefusedWithGuidance(t *testing.T) {
	quiverHome := t.TempDir()
	projectRoot := t.TempDir()
	t.Setenv("QVR_HOME", quiverHome)
	t.Chdir(projectRoot)

	// Skill lives only in the global lock; the project lock is absent.
	writeSingleSkillLock(t, filepath.Join(quiverHome, model.LockFileName), "code-review")

	withCapturingPrinter(t, output.FormatText)
	err := runEdit(editCmd, []string{"code-review"})
	if err == nil {
		t.Fatal("expected edit of a global-only skill to fail")
	}
	msg := err.Error()
	for _, want := range []string{"installed globally", "--global"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should guide to the global workflow; missing %q in:\n%s", want, msg)
		}
	}
}

// TestEdit_UnknownSkillReturnsPlainMissing verifies a skill that exists in
// neither lock still returns the plain not-found sentinel, not the global hint.
func TestEdit_UnknownSkillReturnsPlainMissing(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	withCapturingPrinter(t, output.FormatText)
	err := runEdit(editCmd, []string{"nope"})
	if !errors.Is(err, model.ErrLockSkillMissing) {
		t.Fatalf("expected ErrLockSkillMissing, got %v", err)
	}
	if strings.Contains(err.Error(), "installed globally") {
		t.Errorf("a skill in no lock should not claim it is installed globally: %v", err)
	}
}

// TestEdit_NoGlobalFlag verifies the --global flag was removed: edit is
// project-local only, so cobra must reject the flag.
func TestEdit_NoGlobalFlag(t *testing.T) {
	if editCmd.Flags().Lookup("global") != nil {
		t.Error("qvr edit must not expose a --global flag (edit is project-local only)")
	}
}
