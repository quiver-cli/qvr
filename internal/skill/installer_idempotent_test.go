package skill_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/skill"
)

// TestInstaller_ReAddIsIdempotent is the regression guard for issue #77: a
// no-op re-add of an already-installed skill at the same ref must NOT rewrite
// installedAt or otherwise change the lockfile bytes. Without this, every
// re-add churns the file and downstream tools see false-positive diffs.
func TestInstaller_ReAddIsIdempotent(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"code-review": codeReviewSkill,
	})
	h.addRegistry(t, "acme", remote)

	lockPath := filepath.Join(h.project, model.LockFileName)
	req := skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		LockPath:    lockPath,
	}
	if _, err := h.installer.Install(req); err != nil {
		t.Fatalf("first install: %v", err)
	}

	beforeBytes, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock after first install: %v", err)
	}
	before := sha256.Sum256(beforeBytes)

	// Allow the wall clock to advance so any inadvertent time.Now() stamp
	// produces a different value than the first install — without this the
	// test wouldn't actually exercise the idempotency property on systems
	// where the two installs land in the same nanosecond.
	time.Sleep(20 * time.Millisecond)

	if _, err := h.installer.Install(req); err != nil {
		t.Fatalf("second install: %v", err)
	}

	afterBytes, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock after second install: %v", err)
	}
	after := sha256.Sum256(afterBytes)

	if before != after {
		t.Errorf("lockfile bytes changed across a no-op re-add (issue #77):\n  before sha256: %s\n  after  sha256: %s",
			hex.EncodeToString(before[:]), hex.EncodeToString(after[:]))
		// Hint diagnostics: dump the two payloads.
		t.Logf("before:\n%s", string(beforeBytes))
		t.Logf("after:\n%s", string(afterBytes))
	}
}
