package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// ErrGitNotFound is returned when the `git` binary is not on PATH.
var ErrGitNotFound = errors.New("git binary not found on PATH")

// ErrGitTooOld is returned when the detected git version is older than
// [minGitMajor].[minGitMinor].
var ErrGitTooOld = errors.New("git version too old")

const (
	minGitMajor = 2
	minGitMinor = 30
)

var (
	gitCheckOnce sync.Once
	gitCheckErr  error
)

// ensureGit verifies that the `git` binary exists on PATH and meets the
// minimum required version. Result is cached: the same error (or nil) is
// returned on every subsequent call within a single process.
//
// Requiring git >= 2.30 ensures credential-helper behaviours, `git ls-remote
// --symref` semantics, and the `GIT_TERMINAL_PROMPT` env var all work the way
// we expect.
func ensureGit() error {
	gitCheckOnce.Do(func() {
		gitCheckErr = checkGit()
	})
	return gitCheckErr
}

func checkGit() error {
	path, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("%w: install git or make sure it is on $PATH", ErrGitNotFound)
	}

	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGitNotFound, err)
	}

	maj, min, ok := parseGitVersion(string(out))
	if !ok {
		// Couldn't parse — don't block the user, trust that the binary works.
		return nil
	}
	if maj < minGitMajor || (maj == minGitMajor && min < minGitMinor) {
		return fmt.Errorf("%w: need >= %d.%d, found %d.%d",
			ErrGitTooOld, minGitMajor, minGitMinor, maj, min)
	}
	return nil
}

// parseGitVersion extracts (major, minor) from output like "git version 2.39.3".
// Returns false if parsing fails.
func parseGitVersion(s string) (major, minor int, ok bool) {
	fields := strings.Fields(s)
	if len(fields) < 3 {
		return 0, 0, false
	}
	parts := strings.SplitN(fields[2], ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return maj, min, true
}

// gitCommand builds an *exec.Cmd that invokes `git` with a scrubbed
// environment suitable for non-interactive use in a library context.
//
//   - GIT_TERMINAL_PROMPT=0 — fail fast instead of hanging on a credential
//     prompt when no helper is configured.
//   - GIT_ASKPASS=/bin/true — belt-and-suspenders for older git: if something
//     still tries to prompt, it gets an empty answer and fails quickly.
//   - GIT_DIR / GIT_WORK_TREE / GIT_INDEX_FILE are stripped so a parent
//     environment can't redirect our subprocess at the wrong repository.
//
// SSH-related env (SSH_AUTH_SOCK, SSH_ASKPASS, HOME, etc.) is inherited so
// SSH agents and keychains keep working.
func gitCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = scrubbedEnv(os.Environ())
	return cmd
}

// scrubbedEnv returns env with entries that could misdirect our subprocess
// removed, and with GIT_TERMINAL_PROMPT / GIT_ASKPASS set to disable
// interactive prompts.
func scrubbedEnv(env []string) []string {
	poisoned := map[string]struct{}{
		"GIT_DIR":              {},
		"GIT_WORK_TREE":        {},
		"GIT_INDEX_FILE":       {},
		"GIT_COMMON_DIR":       {},
		"GIT_NAMESPACE":        {},
		"GIT_OBJECT_DIRECTORY": {},
	}
	out := make([]string, 0, len(env)+2)
	seenPrompt := false
	seenAskpass := false
	for _, e := range env {
		before, _, ok := strings.Cut(e, "=")
		if !ok {
			out = append(out, e)
			continue
		}
		key := before
		if _, bad := poisoned[key]; bad {
			continue
		}
		if key == "GIT_TERMINAL_PROMPT" {
			seenPrompt = true
			out = append(out, "GIT_TERMINAL_PROMPT=0")
			continue
		}
		if key == "GIT_ASKPASS" {
			seenAskpass = true
			out = append(out, e)
			continue
		}
		out = append(out, e)
	}
	if !seenPrompt {
		out = append(out, "GIT_TERMINAL_PROMPT=0")
	}
	if !seenAskpass {
		out = append(out, "GIT_ASKPASS=/bin/true")
	}
	return out
}

// RunInDir executes `git -C dir <args...>` and returns stdout. Used by
// command-layer features that need direct git output (e.g. `qvr diff`) not
// modelled by the GitClient interface.
func RunInDir(ctx context.Context, dir string, args ...string) ([]byte, error) {
	full := make([]string, 0, len(args)+2)
	full = append(full, "-C", dir)
	full = append(full, args...)
	return runGit(ctx, full...)
}

// runGit executes `git <args...>` and returns (stdout, error). On failure,
// stderr is included in the returned error, but URLs in the stderr are
// sanitised so embedded credentials never end up in logs.
func runGit(ctx context.Context, args ...string) ([]byte, error) {
	if err := ensureGit(); err != nil {
		return nil, err
	}
	cmd := gitCommand(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), fmt.Errorf("%s", redactCreds(msg))
	}
	return stdout.Bytes(), nil
}

// Git-native signature verification statuses. These mirror the model-level
// constants (model.SignatureStatus*) but keep the git package free of a
// model import — the values are identical, so the command/installer layer
// assigns them straight through.
const (
	SigVerified = "verified" // git reported a good signature
	SigNone     = "none"     // no signature present (or unverifiable: missing key)
	SigInvalid  = "invalid"  // a signature is present but failed verification
)

// signerPatterns extract a best-effort signer identity from git/gpg/ssh
// verification output. GPG: `Good signature from "Name <email>"`. SSH:
// `Good "git" signature for principal@example.com`.
var signerPatterns = []*regexp.Regexp{
	regexp.MustCompile(`Good signature from "([^"]+)"`),
	regexp.MustCompile(`Good "[^"]*" signature for (\S+)`),
}

// VerifyTagSignature runs `git verify-tag <tag>` in the repo at repoPath and
// classifies the result. Returns (SigVerified, signer, nil) on a good
// signature, (SigInvalid, "", nil) when a signature is present but bad
// (tampering), and (SigNone, "", nil) when no verifiable signature exists
// (unsigned, lightweight tag, or signature by a key we don't hold). The
// error return is reserved for operational failures (git missing). This is
// the optional, git-native provenance surface — only SigInvalid should gate
// an install.
func VerifyTagSignature(ctx context.Context, repoPath, tag string) (status, signer string, err error) {
	return verifyGitSignature(ctx, repoPath, "verify-tag", tag)
}

// VerifyCommitSignature runs `git verify-commit <ref>` in the repo at
// repoPath. Semantics match VerifyTagSignature — used as a fallback when the
// resolved ref is a branch/commit rather than an annotated tag.
func VerifyCommitSignature(ctx context.Context, repoPath, ref string) (status, signer string, err error) {
	return verifyGitSignature(ctx, repoPath, "verify-commit", ref)
}

func verifyGitSignature(ctx context.Context, repoPath, verb, ref string) (status, signer string, err error) {
	if err := ensureGit(); err != nil {
		return SigNone, "", err
	}
	// git writes both the human "Good signature ..." line (which we parse for
	// the signer) and the failure reasons to stderr; classification keys off
	// those strings since git has no stable machine-readable exit code that
	// separates "tampered" from "can't check".
	cmd := gitCommand(ctx, "-C", repoPath, verb, ref)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	out := stderr.String()

	if runErr == nil {
		return SigVerified, extractSigner(out), nil
	}

	// A cryptographically valid SSH signature whose signer we don't hold in
	// allowed-signers prints the "Good \"git\" signature ..." line and THEN a
	// trust error ("No principal matched."). That is not tampering — the
	// signature checks out, we just can't attribute it — so it's "none" (the
	// spec's "missing key" bucket), never a block. Checked before the
	// bad-signature markers because a corrupt blob ALSO emits "No principal
	// matched", but without the preceding good-signature line.
	if strings.Contains(out, "No principal matched") && strings.Contains(out, "Good \"") {
		return SigNone, "", nil
	}

	// A present-but-bad signature is the one status that signals tampering and
	// blocks an install. Markers across signing backends:
	//   - GPG content mismatch:        "BAD signature"
	//   - SSH content tamper:          "Signature verification failed: incorrect signature"
	//   - SSH corrupt signature blob:  "Could not verify signature." (+ parse noise)
	if strings.Contains(out, "BAD signature") ||
		strings.Contains(out, "incorrect signature") ||
		strings.Contains(out, "Could not verify signature") {
		return SigInvalid, "", nil
	}

	// Everything else — no signature, lightweight tag, missing public key
	// (GPG "Can't check signature: No public key"), or the signing backend
	// (gpg/ssh-keygen) is absent — is "none": not proven bad, so it must not
	// block an install.
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return SigNone, "", nil
	}
	// Non-exit failure (couldn't launch git, repo unreadable): surface it so
	// callers can distinguish "no signature" from "couldn't check".
	return SigNone, "", fmt.Errorf("%s: %s", verb, redactCreds(strings.TrimSpace(out)))
}

func extractSigner(out string) string {
	for _, re := range signerPatterns {
		if m := re.FindStringSubmatch(out); len(m) == 2 {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// IsAncestor reports whether ancestorSHA is an ancestor of (or equal to)
// descendantSHA in the repo at repoPath, using `git merge-base --is-ancestor`.
// Returns (true, nil) on exit 0, (false, nil) on exit 1, and (false, err) on
// any other failure mode (missing repo, unknown SHA, git binary missing).
//
// Used by the publish-time silent-heal and the lock-verify drift check
// (issues #73, #74, #99). System git is more reliable than go-git's
// CommitObject.IsAncestor on freshly-init'd repos — the eject dir is a
// fresh `git init` whose commit graph go-git sometimes misreads.
func IsAncestor(ctx context.Context, repoPath, ancestorSHA, descendantSHA string) (bool, error) {
	if err := ensureGit(); err != nil {
		return false, err
	}
	cmd := gitCommand(ctx, "-C", repoPath, "merge-base", "--is-ancestor", ancestorSHA, descendantSHA)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// merge-base --is-ancestor exits 1 specifically to say "not an
		// ancestor". Any other non-zero exit is a real error (bad ref,
		// missing repo, etc.) and we surface it.
		if exitErr.ExitCode() == 1 {
			return false, nil
		}
	}
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		msg = err.Error()
	}
	return false, fmt.Errorf("merge-base: %s", redactCreds(msg))
}
