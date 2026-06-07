package skill

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSubtreeFrozen verifies the cheap "already frozen?" proxy that lets a warm
// reuse skip the recursive re-chmod: a read-only SKILL.md means frozen, a
// writable one means not, and a missing one is conservatively "not frozen" so
// the caller re-freezes rather than silently leaving a writable tree.
func TestSubtreeFrozen(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(md, []byte("---\nname: x\ndescription: y\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if subtreeFrozen(dir) {
		t.Errorf("writable SKILL.md (0o644) must not report frozen")
	}
	if err := os.Chmod(md, 0o444); err != nil {
		t.Fatal(err)
	}
	if !subtreeFrozen(dir) {
		t.Errorf("read-only SKILL.md (0o444) must report frozen")
	}
	// Executable-but-read-only (0o555) is still frozen (no owner-write bit).
	if err := os.Chmod(md, 0o555); err != nil {
		t.Fatal(err)
	}
	if !subtreeFrozen(dir) {
		t.Errorf("read-only executable SKILL.md (0o555) must report frozen")
	}
	if subtreeFrozen(t.TempDir()) {
		t.Errorf("missing SKILL.md must report not frozen (conservative re-freeze)")
	}
}
