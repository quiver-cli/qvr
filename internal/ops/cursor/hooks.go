package cursor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/astra-sh/qvr/internal/ops"
)

// hookTypes are the Cursor hooks Quiver installs — the file, command,
// MCP, prompt, and session events that carry attributable activity.
var hookTypes = []string{
	"beforeReadFile",
	"afterFileEdit",
	"beforeShellExecution",
	"afterShellExecution",
	"beforeMCPExecution",
	"afterMCPExecution",
	"beforeSubmitPrompt",
	"preToolUse",
	"postToolUse",
	"postToolUseFailure",
	"sessionStart",
	"sessionEnd",
	"stop",
}

// hookCommand is one entry under a Cursor hook type.
type hookCommand struct {
	Command string `json:"command"`
}

// hooksConfig is the ~/.cursor/hooks.json shape.
type hooksConfig struct {
	Version int                      `json:"version"`
	Hooks   map[string][]hookCommand `json:"hooks"`
}

func hookCommandFor(hookType string) string {
	return fmt.Sprintf("%s _hook %s %s", ops.BinaryPath(), AgentName, hookType)
}

func isQuiverCommand(cmd string) bool {
	return strings.Contains(cmd, "_hook "+AgentName)
}

// readConfig reads hooks.json. A missing or empty file yields a zero
// config with an initialised map.
func readConfig(path string) (*hooksConfig, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) || (err == nil && len(data) == 0) {
		return &hooksConfig{Version: 1, Hooks: map[string][]hookCommand{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var c hooksConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse hooks.json: %w", err)
	}
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Hooks == nil {
		c.Hooks = map[string][]hookCommand{}
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
	for _, cmds := range c.Hooks {
		if len(cmds) != len(stripQuiver(cmds)) {
			return true
		}
	}
	return false
}

func stripQuiver(cmds []hookCommand) []hookCommand {
	out := make([]hookCommand, 0, len(cmds))
	for _, c := range cmds {
		if !isQuiverCommand(c.Command) {
			out = append(out, c)
		}
	}
	return out
}

// Install writes Quiver's hooks into ~/.cursor/hooks.json.
func (a *Adapter) Install(opts ops.InstallOptions) (ops.InstallResult, error) {
	var res ops.InstallResult

	det, err := a.Detect()
	if err != nil {
		return res, err
	}
	if !det.Detected {
		return res, fmt.Errorf("cursor not detected")
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
		c.Hooks[ht] = append(stripQuiver(c.Hooks[ht]), hookCommand{Command: hookCommandFor(ht)})
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
		return uninstallDryRun(path)
	}

	if restored, ok, rErr := restoreFromBackup(path); rErr != nil || ok {
		return restored, rErr
	}

	return stripQuiverInPlace(path)
}

// uninstallDryRun reports which hook types a real uninstall would touch,
// without modifying hooks.json.
func uninstallDryRun(path string) (ops.UninstallResult, error) {
	var res ops.UninstallResult
	c, rErr := readConfig(path)
	if rErr != nil {
		return res, rErr
	}
	for ht, cmds := range c.Hooks {
		if len(cmds) != len(stripQuiver(cmds)) {
			res.HooksRemoved = append(res.HooksRemoved, ht)
		}
	}
	return res, nil
}

// restoreFromBackup restores hooks.json verbatim from the newest backup when
// one exists. ok reports whether a backup was found and applied.
func restoreFromBackup(path string) (ops.UninstallResult, bool, error) {
	var res ops.UninstallResult
	if backupDir, bErr := ops.LatestBackupDir(AgentName); bErr == nil && backupDir != "" {
		bak := filepath.Join(backupDir, "hooks.json.bak")
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

// stripQuiverInPlace surgically removes Quiver entries from hooks.json, used
// when no pre-install backup is available.
func stripQuiverInPlace(path string) (ops.UninstallResult, error) {
	var res ops.UninstallResult
	c, err := readConfig(path)
	if err != nil {
		return res, err
	}
	for ht, cmds := range c.Hooks {
		stripped := stripQuiver(cmds)
		if len(stripped) != len(cmds) {
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
	for ht, cmds := range c.Hooks {
		if len(cmds) != len(stripQuiver(cmds)) {
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
