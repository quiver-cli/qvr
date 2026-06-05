package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quiver-cli/qvr/internal/git"
)

// gitInRepo runs a git subcommand in dir with a deterministic identity, failing
// the test on error. Used to build small unsigned repos for the signature
// classification tests.
func gitInRepo(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// TestVerifySignature_UnsignedReportsNone confirms that an unsigned commit and
// an unsigned annotated tag both classify as SigNone (not SigInvalid) and
// return no operational error. This is the common case — unsigned skills must
// never look like tampering, and must never block an install.
func TestVerifySignature_UnsignedReportsNone(t *testing.T) {
	dir := t.TempDir()
	gitInRepo(t, dir, "init", "-b", "main")
	gitInRepo(t, dir, "commit", "--allow-empty", "-m", "init")
	gitInRepo(t, dir, "tag", "-a", "v1.0.0", "-m", "release") // annotated, unsigned

	ctx := context.Background()

	status, signer, err := git.VerifyCommitSignature(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("verify-commit: unexpected operational error: %v", err)
	}
	if status != git.SigNone {
		t.Errorf("unsigned commit status = %q, want %q", status, git.SigNone)
	}
	if signer != "" {
		t.Errorf("unsigned commit signer = %q, want empty", signer)
	}

	status, _, err = git.VerifyTagSignature(ctx, dir, "v1.0.0")
	if err != nil {
		t.Fatalf("verify-tag: unexpected operational error: %v", err)
	}
	if status != git.SigNone {
		t.Errorf("unsigned tag status = %q, want %q", status, git.SigNone)
	}
}

// TestVerifyTagSignature_TamperedReportsInvalid is the #128 regression guard:
// a signed annotated tag whose content or signature blob was altered after
// signing must classify as SigInvalid (present-but-bad → tampering), so the
// installer refuses it. Pre-fix the SSH "Signature verification failed:
// incorrect signature" / "Could not verify signature." output didn't contain
// the GPG-only "BAD signature" marker, so a tampered tag silently degraded to
// SigNone and installed. A cryptographically valid signature from a signer we
// don't hold (unknown key) must still report SigNone — unknown ≠ tampered.
//
// Uses SSH signing because it needs no external keyserver/agent and works
// wherever ssh-keygen exists; skipped otherwise.
func TestVerifyTagSignature_TamperedReportsInvalid(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "id")
	if out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", key, "-q", "-C", "signer@test").CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatalf("read pubkey: %v", err)
	}
	allowed := filepath.Join(dir, "allowed_signers")
	if err := os.WriteFile(allowed, []byte("signer@test "+string(pub)), 0o644); err != nil {
		t.Fatalf("write allowed_signers: %v", err)
	}

	gitInRepo(t, dir, "init", "-b", "main")
	gitInRepo(t, dir, "config", "user.name", "signer")
	gitInRepo(t, dir, "config", "user.email", "signer@test")
	gitInRepo(t, dir, "config", "gpg.format", "ssh")
	gitInRepo(t, dir, "config", "user.signingkey", key+".pub")
	gitInRepo(t, dir, "config", "gpg.ssh.allowedSignersFile", allowed)
	gitInRepo(t, dir, "commit", "--allow-empty", "-m", "init")
	gitInRepo(t, dir, "tag", "-s", "v1.0.0", "-m", "release")
	gitInRepo(t, dir, "commit", "--allow-empty", "-m", "second")

	ctx := context.Background()

	// Sanity: the untampered signed tag verifies.
	if status, _, err := git.VerifyTagSignature(ctx, dir, "v1.0.0"); err != nil || status != git.SigVerified {
		t.Fatalf("signed tag status = %q (err %v), want %q", status, err, git.SigVerified)
	}

	// Tamper: rewrite the tag object to point at the second commit, keeping the
	// now-mismatched signature. The signed content no longer matches the blob.
	c1 := strings.TrimSpace(runGitOut(t, dir, "rev-parse", "v1.0.0^{commit}"))
	c2 := strings.TrimSpace(runGitOut(t, dir, "rev-parse", "HEAD"))
	tagBody := runGitOut(t, dir, "cat-file", "tag", "v1.0.0")
	tampered := strings.Replace(tagBody, c1, c2, 1)
	tamperedHash := strings.TrimSpace(hashObject(t, dir, "tag", tampered))
	gitInRepo(t, dir, "update-ref", "refs/tags/tampered", tamperedHash)

	if status, _, err := git.VerifyTagSignature(ctx, dir, "tampered"); err != nil || status != git.SigInvalid {
		t.Fatalf("tampered tag status = %q (err %v), want %q", status, err, git.SigInvalid)
	}

	// A valid signature from a key we don't hold (empty allowed-signers) is
	// "none", not "invalid": unknown ≠ tampered, and unknown must never block.
	if err := os.WriteFile(allowed, []byte("\n"), 0o644); err != nil {
		t.Fatalf("truncate allowed_signers: %v", err)
	}
	if status, _, err := git.VerifyTagSignature(ctx, dir, "v1.0.0"); err != nil || status != git.SigNone {
		t.Fatalf("unknown-signer tag status = %q (err %v), want %q", status, err, git.SigNone)
	}
}

// runGitOut runs a git subcommand in dir and returns stdout, failing on error.
func runGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// hashObject writes a git object of the given type from stdin and returns its
// SHA. Used to forge a tampered tag object in-place.
func hashObject(t *testing.T, dir, typ, body string) string {
	t.Helper()
	cmd := exec.Command("git", "hash-object", "-t", typ, "-w", "--stdin")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(body)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git hash-object: %v", err)
	}
	return string(out)
}
