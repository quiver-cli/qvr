package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/quiver-cli/qvr/internal/ops"
)

// hookTypes are the Codex CLI hook events Quiver captures.
var hookTypes = []string{
	"SessionStart",
	"PreToolUse",
	"PostToolUse",
	"UserPromptSubmit",
	"Stop",
}

type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type hookMatcher struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks"`
}

// hooksConfig is the whole ~/.codex/hooks.json file.
type hooksConfig struct {
	Hooks map[string][]hookMatcher `json:"hooks"`
}

func usesMatcher(hookType string) bool {
	return hookType == "PreToolUse" || hookType == "PostToolUse"
}

func hookCommandFor(hookType string) string {
	return fmt.Sprintf("%s _hook %s %s", ops.BinaryPath(), AgentName, hookType)
}

func isQuiverCommand(cmd string) bool {
	return strings.Contains(cmd, "_hook "+AgentName)
}

func quiverMatcher(hookType string) hookMatcher {
	m := hookMatcher{Hooks: []hookCommand{{Type: "command", Command: hookCommandFor(hookType), Timeout: 30}}}
	if usesMatcher(hookType) {
		m.Matcher = "*"
	}
	return m
}

func readConfig(path string) (*hooksConfig, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) || (err == nil && len(data) == 0) {
		return &hooksConfig{Hooks: map[string][]hookMatcher{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var c hooksConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse hooks.json: %w", err)
	}
	if c.Hooks == nil {
		c.Hooks = map[string][]hookMatcher{}
	}
	return &c, nil
}

func writeConfig(path string, c *hooksConfig) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func hasQuiverHooks(c *hooksConfig) bool {
	for _, matchers := range c.Hooks {
		if len(matchers) != len(stripQuiver(matchers)) {
			return true
		}
	}
	return false
}

func stripQuiver(matchers []hookMatcher) []hookMatcher {
	out := make([]hookMatcher, 0, len(matchers))
	for _, m := range matchers {
		kept := make([]hookCommand, 0, len(m.Hooks))
		for _, h := range m.Hooks {
			if !isQuiverCommand(h.Command) {
				kept = append(kept, h)
			}
		}
		if len(kept) == 0 {
			continue
		}
		m.Hooks = kept
		out = append(out, m)
	}
	return out
}

// Install writes Quiver's hooks into ~/.codex/hooks.json.
func (a *Adapter) Install(opts ops.InstallOptions) (ops.InstallResult, error) {
	var res ops.InstallResult

	det, err := a.Detect()
	if err != nil {
		return res, err
	}
	if !det.Detected {
		return res, fmt.Errorf("codex not detected")
	}
	path, err := hooksPath()
	if err != nil {
		return res, err
	}

	c, err := readConfig(path)
	if err != nil {
		return res, err
	}
	if !opts.Force && hasQuiverHooks(c) {
		res.Warnings = append(res.Warnings, "quiver hooks already installed (use --force to reinstall)")
		return res, nil
	}
	if opts.DryRun {
		res.HooksAdded = append([]string(nil), hookTypes...)
		return res, nil
	}

	if _, statErr := os.Stat(path); statErr == nil {
		if dir, dErr := ops.NewBackupDir(AgentName); dErr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("backup skipped: %v", dErr))
		} else if bp, cErr := ops.CopyFileInto(path, dir); cErr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("backup skipped: %v", cErr))
		} else {
			res.BackupPath = bp
		}
	}

	for _, ht := range hookTypes {
		c.Hooks[ht] = append(stripQuiver(c.Hooks[ht]), quiverMatcher(ht))
		res.HooksAdded = append(res.HooksAdded, ht)
	}
	if err := writeConfig(path, c); err != nil {
		return res, fmt.Errorf("write hooks.json: %w", err)
	}
	return res, nil
}

// Uninstall restores from the newest backup, else strips Quiver entries.
func (a *Adapter) Uninstall(opts ops.UninstallOptions) (ops.UninstallResult, error) {
	var res ops.UninstallResult

	path, err := hooksPath()
	if err != nil {
		return res, err
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return res, nil
	}

	if opts.DryRun {
		c, rErr := readConfig(path)
		if rErr != nil {
			return res, rErr
		}
		for ht, matchers := range c.Hooks {
			if len(matchers) != len(stripQuiver(matchers)) {
				res.HooksRemoved = append(res.HooksRemoved, ht)
			}
		}
		return res, nil
	}

	if backupDir, bErr := ops.LatestBackupDir(AgentName); bErr == nil && backupDir != "" {
		bak := filepath.Join(backupDir, "hooks.json.bak")
		if _, sErr := os.Stat(bak); sErr == nil {
			if err := ops.CopyFile(bak, path); err != nil {
				return res, fmt.Errorf("restore backup: %w", err)
			}
			res.Restored = true
			res.HooksRemoved = append([]string(nil), hookTypes...)
			return res, nil
		}
	}

	c, err := readConfig(path)
	if err != nil {
		return res, err
	}
	for ht, matchers := range c.Hooks {
		stripped := stripQuiver(matchers)
		if len(stripped) != len(matchers) {
			res.HooksRemoved = append(res.HooksRemoved, ht)
		}
		if len(stripped) == 0 {
			delete(c.Hooks, ht)
		} else {
			c.Hooks[ht] = stripped
		}
	}
	if err := writeConfig(path, c); err != nil {
		return res, fmt.Errorf("write hooks.json: %w", err)
	}
	return res, nil
}

// Status reports whether Quiver's hooks are present and complete.
func (a *Adapter) Status() (ops.HookStatus, error) {
	var st ops.HookStatus
	path, err := hooksPath()
	if err != nil {
		return st, err
	}
	c, err := readConfig(path)
	if err != nil {
		st.Issues = append(st.Issues, err.Error())
		return st, nil
	}
	present := map[string]bool{}
	for ht, matchers := range c.Hooks {
		if len(matchers) != len(stripQuiver(matchers)) {
			present[ht] = true
			st.Hooks = append(st.Hooks, ht)
		}
	}
	if len(present) == 0 {
		return st, nil
	}
	st.Installed = true
	st.Valid = true
	for _, ht := range hookTypes {
		if !present[ht] {
			st.Valid = false
			st.Issues = append(st.Issues, fmt.Sprintf("%s: hook not configured", ht))
		}
	}
	return st, nil
}
