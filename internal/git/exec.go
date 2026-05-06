package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
		eq := strings.IndexByte(e, '=')
		if eq < 0 {
			out = append(out, e)
			continue
		}
		key := e[:eq]
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
