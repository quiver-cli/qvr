package claudecode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/quiver-cli/qvr/internal/ops"
)

// configDir returns the Claude Code config directory, honoring
// CLAUDE_CONFIG_DIR (the same env claude-code itself reads to locate its
// config) and falling back to ~/.claude. It does not check existence —
// Detect does that.
func configDir() (string, error) {
	if env := os.Getenv("CLAUDE_CONFIG_DIR"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// settingsPath is the file the hooks live in.
func settingsPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// Detect reports whether Claude Code is present (its config dir exists)
// and where its settings live. A best-effort `claude -v` fills Version.
func (a *Adapter) Detect() (ops.DetectionResult, error) {
	dir, err := configDir()
	if err != nil {
		return ops.DetectionResult{}, err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ops.DetectionResult{
			Detected: false,
			Message:  "Claude Code not detected (" + dir + " not found)",
		}, nil
	}
	return ops.DetectionResult{
		Detected:   true,
		ConfigPath: dir,
		Version:    detectVersion(),
	}, nil
}

// detectVersion runs `claude -v` best-effort. Returns "" if claude isn't
// on PATH or the call fails — version is informational only.
func detectVersion() string {
	out, err := exec.Command("claude", "-v").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
