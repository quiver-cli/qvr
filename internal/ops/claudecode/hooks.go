package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/astra-sh/qvr/internal/ops"
)

// hookTypes are the Claude Code hook events Quiver captures. Order is the
// install order; PreToolUse/PostToolUse/PostToolUseFailure additionally
// carry a "*" matcher so every tool is observed.
var hookTypes = []string{
	"PreToolUse",
	"PostToolUse",
	"PostToolUseFailure",
	"SessionStart",
	"SessionEnd",
	"Notification",
	"SubagentStart",
	"SubagentStop",
}

// hookCommand is one `{"type":"command","command":"qvr _hook ..."}` entry.
type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// hookMatcher is one matcher block under a hook type.
type hookMatcher struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks"`
}

// usesMatcher reports whether a hook type filters by tool (and thus needs
// the "*" matcher).
func usesMatcher(hookType string) bool {
	switch hookType {
	case "PreToolUse", "PostToolUse", "PostToolUseFailure":
		return true
	}
	return false
}

// hookCommandFor is the exact command string Quiver installs for a hook
// type, e.g. `/abs/path/qvr _hook claude-code PostToolUse`.
func hookCommandFor(hookType string) string {
	return fmt.Sprintf("%s _hook %s %s", ops.BinaryPath(), AgentName, hookType)
}

// isQuiverCommand reports whether cmd is one Quiver installed (identified
// by the `_hook claude-code` marker, independent of the binary path).
func isQuiverCommand(cmd string) bool {
	return strings.Contains(cmd, "_hook "+AgentName)
}

// quiverMatcher builds the matcher block Quiver installs for a hook type.
func quiverMatcher(hookType string) hookMatcher {
	m := hookMatcher{Hooks: []hookCommand{{Type: "command", Command: hookCommandFor(hookType)}}}
	if usesMatcher(hookType) {
		m.Matcher = "*"
	}
	return m
}

// readSettings reads settings.json into an ordered-agnostic top-level map
// that preserves every key. A missing file yields an empty map.
func readSettings(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	return m, nil
}

// writeSettings writes the top-level map back as indented JSON (0600).
func writeSettings(path string, settings map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// hooksSection decodes the "hooks" key into a per-type matcher map,
// preserving unmanaged hook types as raw JSON so we never clobber a user's
// hooks for events Quiver doesn't manage.
func hooksSection(settings map[string]json.RawMessage) (map[string][]hookMatcher, error) {
	out := map[string][]hookMatcher{}
	raw, ok := settings["hooks"]
	if !ok || len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse hooks section: %w", err)
	}
	return out, nil
}

// putHooks re-encodes the hooks map back into settings under "hooks",
// deleting the key entirely when empty.
func putHooks(settings map[string]json.RawMessage, hooks map[string][]hookMatcher) error {
	if len(hooks) == 0 {
		delete(settings, "hooks")
		return nil
	}
	data, err := json.Marshal(hooks)
	if err != nil {
		return err
	}
	settings["hooks"] = data
	return nil
}

// Install wires Quiver's hooks into ~/.claude/settings.json.
func (a *Adapter) Install(opts ops.InstallOptions) (ops.InstallResult, error) {
	var res ops.InstallResult

	det, err := a.Detect()
	if err != nil {
		return res, err
	}
	if !det.Detected {
		return res, fmt.Errorf("claude-code not detected")
	}
	path, err := settingsPath()
	if err != nil {
		return res, err
	}

	settings, err := readSettings(path)
	if err != nil {
		return res, err
	}
	hooks, err := hooksSection(settings)
	if err != nil {
		return res, err
	}

	if !opts.Force && hasQuiverHooks(hooks) {
		res.Warnings = append(res.Warnings, "quiver hooks already installed (use --force to reinstall)")
		return res, nil
	}

	if opts.DryRun {
		res.HooksAdded = append([]string(nil), hookTypes...)
		return res, nil
	}

	// Back up the existing settings file before mutating.
	if _, statErr := os.Stat(path); statErr == nil {
		dir, dErr := ops.NewBackupDir(AgentName)
		if dErr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("backup skipped: %v", dErr))
		} else if bp, cErr := ops.CopyFileInto(path, dir); cErr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("backup skipped: %v", cErr))
		} else {
			res.BackupPath = bp
		}
	}

	for _, ht := range hookTypes {
		// Drop any prior Quiver entries for this type, then add ours.
		hooks[ht] = append(stripQuiver(hooks[ht]), quiverMatcher(ht))
		res.HooksAdded = append(res.HooksAdded, ht)
	}

	if err := putHooks(settings, hooks); err != nil {
		return res, err
	}
	if err := writeSettings(path, settings); err != nil {
		return res, fmt.Errorf("write settings.json: %w", err)
	}
	return res, nil
}

// Uninstall removes Quiver's hooks, restoring from the newest backup when
// one exists; otherwise it surgically strips Quiver entries in place.
func (a *Adapter) Uninstall(opts ops.UninstallOptions) (ops.UninstallResult, error) {
	var res ops.UninstallResult

	path, err := settingsPath()
	if err != nil {
		return res, err
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return res, nil // nothing to do
	}

	if opts.DryRun {
		return uninstallDryRun(path)
	}

	// Prefer restoring the pre-install backup verbatim.
	if restored, ok, rErr := restoreFromBackup(path); rErr != nil || ok {
		return restored, rErr
	}

	// No backup — strip Quiver entries surgically.
	return stripQuiverInPlace(path)
}

// uninstallDryRun reports which hook types a real uninstall would touch,
// without modifying settings.json.
func uninstallDryRun(path string) (ops.UninstallResult, error) {
	var res ops.UninstallResult
	settings, rErr := readSettings(path)
	if rErr != nil {
		return res, rErr
	}
	hooks, hErr := hooksSection(settings)
	if hErr != nil {
		return res, hErr
	}
	for ht, matchers := range hooks {
		if len(matchers) != len(stripQuiver(matchers)) {
			res.HooksRemoved = append(res.HooksRemoved, ht)
		}
	}
	return res, nil
}

// restoreFromBackup restores settings.json verbatim from the newest backup
// when one exists. ok reports whether a backup was found and applied.
func restoreFromBackup(path string) (ops.UninstallResult, bool, error) {
	var res ops.UninstallResult
	if backupDir, bErr := ops.LatestBackupDir(AgentName); bErr == nil && backupDir != "" {
		bak := filepath.Join(backupDir, "settings.json.bak")
		if _, sErr := os.Stat(bak); sErr == nil {
			if err := ops.CopyFile(bak, path); err != nil {
				return res, true, fmt.Errorf("restore backup: %w", err)
			}
			res.Restored = true
			res.HooksRemoved = append([]string(nil), hookTypes...)
			return res, true, nil
		}
	}
	return res, false, nil
}

// stripQuiverInPlace surgically removes Quiver entries from settings.json,
// used when no pre-install backup is available.
func stripQuiverInPlace(path string) (ops.UninstallResult, error) {
	var res ops.UninstallResult
	settings, err := readSettings(path)
	if err != nil {
		return res, err
	}
	hooks, err := hooksSection(settings)
	if err != nil {
		return res, err
	}
	for ht, matchers := range hooks {
		stripped := stripQuiver(matchers)
		if len(stripped) != len(matchers) {
			res.HooksRemoved = append(res.HooksRemoved, ht)
		}
		if len(stripped) == 0 {
			delete(hooks, ht)
		} else {
			hooks[ht] = stripped
		}
	}
	if err := putHooks(settings, hooks); err != nil {
		return res, err
	}
	if err := writeSettings(path, settings); err != nil {
		return res, fmt.Errorf("write settings.json: %w", err)
	}
	return res, nil
}

// Status reports whether Quiver's hooks are present and complete.
func (a *Adapter) Status() (ops.HookStatus, error) {
	var st ops.HookStatus

	path, err := settingsPath()
	if err != nil {
		return st, err
	}
	settings, err := readSettings(path)
	if err != nil {
		st.Issues = append(st.Issues, err.Error())
		return st, nil
	}
	hooks, err := hooksSection(settings)
	if err != nil {
		st.Issues = append(st.Issues, err.Error())
		return st, nil
	}

	present := map[string]bool{}
	for ht, matchers := range hooks {
		if len(matchers) != len(stripQuiver(matchers)) {
			present[ht] = true
			st.Hooks = append(st.Hooks, ht)
		}
	}
	if len(present) == 0 {
		return st, nil // not installed; Valid stays false
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

// hasQuiverHooks reports whether any managed hook type already carries a
// Quiver command.
func hasQuiverHooks(hooks map[string][]hookMatcher) bool {
	for _, matchers := range hooks {
		if len(matchers) != len(stripQuiver(matchers)) {
			return true
		}
	}
	return false
}

// stripQuiver returns matchers with every Quiver-installed command removed.
// A matcher whose hooks become empty is dropped entirely.
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
