package skill_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
)

// seedRepoAt creates a non-bare git repo at dir with a skill at
// skills/<name>/SKILL.md. Returns nothing — dir already known to caller.
func seedRepoAt(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	skillRel := filepath.Join("skills", name)
	skillAbs := filepath.Join(dir, skillRel)
	if err := os.MkdirAll(skillAbs, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillAbs, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := wt.Add(filepath.Join(skillRel, "SKILL.md")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// commitOnTop adds a new commit with body to existing skill at skills/<name>/SKILL.md.
func commitOnTop(t *testing.T, dir, name, body string) {
	t.Helper()
	skillAbs := filepath.Join(dir, "skills", name, "SKILL.md")
	if err := os.WriteFile(skillAbs, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add(filepath.Join("skills", name, "SKILL.md")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("update", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(1, 0).UTC()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// commitPathAs adds/updates skills/<name>/SKILL.md authored by a specific
// identity — used to build a multi-author registry where the branch tip and
// the skill-subtree author differ (issue #171).
func commitPathAs(t *testing.T, dir, name, body, authorName, authorEmail string, when time.Time) string {
	t.Helper()
	skillAbs := filepath.Join(dir, "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillAbs), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(skillAbs, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add(filepath.Join("skills", name, "SKILL.md")); err != nil {
		t.Fatalf("add: %v", err)
	}
	h, err := wt.Commit("commit "+name, &gogit.CommitOptions{
		Author: &object.Signature{Name: authorName, Email: authorEmail, When: when},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return h.String()
}

// SkillCommit resolves the commit that last touched a skill's subtree — the
// anchor for both author (#171) and signature (#173) provenance. The tip and
// root-layout fall back to the given commit.
func TestSkillCommit(t *testing.T) {
	dir := t.TempDir()
	if _, err := gogit.PlainInit(dir, false); err != nil {
		t.Fatalf("init: %v", err)
	}
	alphaSHA := commitPathAs(t, dir, "alpha", "---\nname: alpha\n---\nv1\n",
		"Alice", "alice@x", time.Unix(1, 0).UTC())
	tipSHA := commitPathAs(t, dir, "other", "---\nname: other\n---\nv1\n",
		"Bob", "bob@x", time.Unix(2, 0).UTC())

	if got := skill.SkillCommit(dir, tipSHA, "skills/alpha"); got != alphaSHA {
		t.Errorf("SkillCommit(alpha) = %q, want %q (alpha's own commit, not tip %q)", got, alphaSHA, tipSHA)
	}
	if got := skill.SkillCommit(dir, tipSHA, "skills/other"); got != tipSHA {
		t.Errorf("SkillCommit(other) = %q, want tip %q", got, tipSHA)
	}
	// Root-layout and unknown paths fall back to the given commit.
	if got := skill.SkillCommit(dir, tipSHA, "."); got != tipSHA {
		t.Errorf("SkillCommit(root) = %q, want tip %q", got, tipSHA)
	}
	if got := skill.SkillCommit(dir, tipSHA, "skills/missing"); got != tipSHA {
		t.Errorf("SkillCommit(missing) = %q, want tip fallback %q", got, tipSHA)
	}
}

// CommitAuthor attributes a skill to the author who last touched the skill's
// own subtree — not whoever made the branch-tip commit. Issue #171: a registry
// holds many skills on one branch, so the tip author (an unrelated later
// commit) must not stand in for a skill's real provenance.
func TestCommitAuthor_usesSubtreeAuthorNotTip(t *testing.T) {
	dir := t.TempDir()
	if _, err := gogit.PlainInit(dir, false); err != nil {
		t.Fatalf("init: %v", err)
	}
	// alpha is authored by Alice; a later, unrelated commit to a different
	// skill by Bob becomes the branch tip.
	alphaSHA := commitPathAs(t, dir, "alpha", "---\nname: alpha\n---\nv1\n",
		"Alice", "alice@x", time.Unix(1, 0).UTC())
	tipSHA := commitPathAs(t, dir, "other", "---\nname: other\n---\nv1\n",
		"Bob", "bob@x", time.Unix(2, 0).UTC())

	if got := skill.CommitAuthor(dir, tipSHA, "skills/alpha"); got != "Alice <alice@x>" {
		t.Errorf("alpha author = %q, want Alice <alice@x> (must not be the tip author)", got)
	}
	if got := skill.CommitAuthor(dir, tipSHA, "skills/other"); got != "Bob <bob@x>" {
		t.Errorf("other author = %q, want Bob <bob@x>", got)
	}
	// Root-layout (whole repo is the skill) falls back to the tip author.
	if got := skill.CommitAuthor(dir, tipSHA, "."); got != "Bob <bob@x>" {
		t.Errorf("root-layout author = %q, want tip Bob <bob@x>", got)
	}
	_ = alphaSHA
}

// AuthorMatchesPin must accept an email-only pin (the obvious thing a user
// reaches for) and a full identity, while still rejecting a different author
// and a bare handle that carries no email. Issue #172.
func TestAuthorMatchesPin(t *testing.T) {
	const author = "Alice Dev <alice@example.com>"
	cases := []struct {
		pin  string
		want bool
	}{
		{"Alice Dev <alice@example.com>", true}, // full identity
		{"alice@example.com", true},             // bare email
		{"<alice@example.com>", true},           // bracketed email
		{"ALICE@EXAMPLE.COM", true},             // email case-insensitive
		{"Different <alice@example.com>", true}, // same email, different name
		{"alice", false},                        // bare handle — no email
		{"Alice Dev", false},                    // bare name — no email
		{"bob@example.com", false},              // different email
		{"", false},
	}
	for _, c := range cases {
		if got := skill.AuthorMatchesPin(author, c.pin); got != c.want {
			t.Errorf("AuthorMatchesPin(%q, %q) = %v, want %v", author, c.pin, got, c.want)
		}
	}
}

// ValidAuthorPin gates `qvr trust pin`: a value that can never match a commit
// author must be rejected up front rather than recorded with a misleading ✓.
func TestValidAuthorPin(t *testing.T) {
	valid := []string{"alice@example.com", "a@x", "Alice <alice@example.com>", "<a@x>"}
	for _, v := range valid {
		if !skill.ValidAuthorPin(v) {
			t.Errorf("ValidAuthorPin(%q) = false, want true", v)
		}
	}
	invalid := []string{"alice", "Alice Dev", "", "   "}
	for _, v := range invalid {
		if skill.ValidAuthorPin(v) {
			t.Errorf("ValidAuthorPin(%q) = true, want false", v)
		}
	}
}

// DeclaredSignedBy reads metadata.signed_by from a skill's SKILL.md and is the
// source of the identity the verified signature must match. Issue #167.
func TestDeclaredSignedBy(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"declared", "---\nname: s\ndescription: d\nmetadata:\n  signed_by: alice@example.com\n---\nbody\n", "alice@example.com"},
		{"absent", "---\nname: s\ndescription: d\n---\nbody\n", ""},
		{"empty", "---\nname: s\ndescription: d\nmetadata:\n  signed_by: \"\"\n---\nbody\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(c.body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := skill.DeclaredSignedBy(dir); got != c.want {
				t.Errorf("DeclaredSignedBy = %q, want %q", got, c.want)
			}
		})
	}
	// Missing SKILL.md yields "" rather than an error.
	if got := skill.DeclaredSignedBy(t.TempDir()); got != "" {
		t.Errorf("DeclaredSignedBy(no SKILL.md) = %q, want empty", got)
	}
}

// gitEnv runs git in dir with a deterministic identity and an isolated config
// (no user/system gitconfig), failing the test on error.
func gitEnv(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Alice Dev", "GIT_AUTHOR_EMAIL=alice@example.com",
		"GIT_COMMITTER_NAME=Alice Dev", "GIT_COMMITTER_EMAIL=alice@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// TestCheckGitProvenance_usesSkillCommitNotTip is the #173 regression guard: on
// a registry where an unsigned skill is followed by an unrelated signed commit
// (the branch tip), the unsigned skill must report SignatureStatusNone — its
// own commit's status — not inherit the tip's verified signature. The signed
// skill, whose own commit carries the signature, must report verified.
func TestCheckGitProvenance_usesSkillCommitNotTip(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "id")
	if out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", key, "-q", "-C", "alice@example.com").CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatalf("read pubkey: %v", err)
	}
	allowed := filepath.Join(dir, "allowed_signers")
	if err := os.WriteFile(allowed, []byte("alice@example.com "+string(pub)), 0o644); err != nil {
		t.Fatalf("write allowed_signers: %v", err)
	}

	gitEnv(t, dir, "init", "-q", "-b", "main")
	gitEnv(t, dir, "config", "gpg.format", "ssh")
	gitEnv(t, dir, "config", "user.signingkey", key+".pub")
	gitEnv(t, dir, "config", "gpg.ssh.allowedSignersFile", allowed)

	writeSkill := func(name, body string) {
		p := filepath.Join(dir, "skills", name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		gitEnv(t, dir, "add", "-A")
	}

	// Signed skill first, then an unsigned skill becomes the branch tip.
	writeSkill("sgn", "---\nname: sgn\ndescription: d\n---\n#\n")
	gitEnv(t, dir, "commit", "-q", "-S", "-m", "signed sgn")
	writeSkill("uns", "---\nname: uns\ndescription: d\n---\n#\n")
	gitEnv(t, dir, "commit", "-q", "--no-gpg-sign", "-m", "unsigned uns (tip)")

	tip := strings.TrimSpace(runGitOutEnv(t, dir, "rev-parse", "HEAD"))

	// The unsigned skill must NOT inherit the signed tip.
	if prov := skill.CheckGitProvenance(dir, "main", tip, "skills/uns"); prov == nil || prov.SignatureStatus != model.SignatureStatusNone {
		t.Fatalf("uns provenance = %+v, want SignatureStatusNone (must not inherit the signed tip)", prov)
	}
	// The signed skill, whose own commit carries the signature, verifies.
	if prov := skill.CheckGitProvenance(dir, "main", tip, "skills/sgn"); prov == nil || prov.SignatureStatus != model.SignatureStatusVerified {
		t.Fatalf("sgn provenance = %+v, want SignatureStatusVerified", prov)
	}
}

// runGitOutEnv runs git in dir with an isolated config and returns stdout.
func runGitOutEnv(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// TestComputeSubtreeHash_returnsCanonicalHash exercises the small helper that
// installer.go uses to seal an install.
func TestComputeSubtreeHash_returnsCanonicalHash(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	wt := registry.WorktreePath("r", "foo", "abc1234")
	seedRepoAt(t, wt, "foo", "---\nname: foo\n---\nbody\n")

	got, err := skill.ComputeSubtreeHash(wt, "skills/foo")
	if err != nil {
		t.Fatalf("ComputeSubtreeHash: %v", err)
	}
	if got == "" {
		t.Error("expected non-empty SubtreeHash")
	}
}

// RefreshSubtreeHash rewrites entry.SubtreeHash to match the new on-disk
// state after a Pull / Switch / Upgrade. v5: no Verification side effects.
func TestRefreshSubtreeHash_updatesEntryInPlace(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	wt := registry.WorktreePath("r", "foo", "abc1234")
	seedRepoAt(t, wt, "foo", "---\nname: foo\n---\nv1\n")

	entry := &model.LockEntry{
		Name:     "foo",
		Registry: "r",
		Source:   "git@example.test:r.git",
		Path:     "skills/foo",
		Ref:      "v1.0.0",
		Commit:   "abc1234",
	}
	original, err := skill.ComputeSubtreeHash(wt, entry.Path)
	if err != nil {
		t.Fatalf("seed hash: %v", err)
	}
	entry.SubtreeHash = original

	commitOnTop(t, wt, "foo", "---\nname: foo\n---\nv2\n")

	if err := skill.RefreshSubtreeHash(entry); err != nil {
		t.Fatalf("RefreshSubtreeHash: %v", err)
	}
	if entry.SubtreeHash == original {
		t.Errorf("hash did not refresh after content change (still %q)", entry.SubtreeHash)
	}
}

// Link installs are skipped by RefreshSubtreeHash — they have no upstream
// subtree to re-derive.
func TestRefreshSubtreeHash_skipsLink(t *testing.T) {
	entry := &model.LockEntry{
		Name:        "linked",
		Source:      "/some/local/path",
		Ref:         "local",
		SubtreeHash: "sha256:original",
	}
	if err := skill.RefreshSubtreeHash(entry); err != nil {
		t.Fatalf("RefreshSubtreeHash on link: %v", err)
	}
	if entry.SubtreeHash != "sha256:original" {
		t.Errorf("link entry hash mutated: got %q", entry.SubtreeHash)
	}
}
