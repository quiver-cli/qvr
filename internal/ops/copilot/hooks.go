package copilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/quiver-cli/qvr/internal/ops"
)

// hookTypes are the Copilot CLI hook events Quiver captures.
var hookTypes = []string{
	"sessionStart",
	"sessionEnd",
	"userPromptSubmitted",
	"preToolUse",
	"postToolUse",
	"errorOccurred",
	"agentStop",
}

// hookEntry is one command entry. Both bash and powershell point at the same
// cross-platform qvr binary (it reads stdin regardless of OS); Copilot picks
// the key matching the user's platform.
type hookEntry struct {
	Type       string `json:"type"`
	Bash       string `json:"bash"`
	Powershell string `json:"powershell"`
	TimeoutSec int    `json:"timeoutSec"`
}

// hooksFile is the standalone ~/.copilot/hooks/quiver.json shape.
type hooksFile struct {
	Version int                    `json:"version"`
	Hooks   map[string][]hookEntry `json:"hooks"`
}

func hookCommandFor(hookType string) string {
	return fmt.Sprintf("%s _hook %s %s", ops.BinaryPath(), AgentName, hookType)
}

// generateHooksFile builds the full quiver.json contents.
func generateHooksFile() *hooksFile {
	hf := &hooksFile{Version: 1, Hooks: map[string][]hookEntry{}}
	for _, ht := range hookTypes {
		cmd := hookCommandFor(ht)
		hf.Hooks[ht] = []hookEntry{{
			Type:       "command",
			Bash:       cmd,
			Powershell: cmd,
			TimeoutSec: 10,
		}}
	}
	return hf
}

func installed(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Install writes the standalone quiver.json hook file.
func (a *Adapter) Install(opts ops.InstallOptions) (ops.InstallResult, error) {
	var res ops.InstallResult

	det, err := a.Detect()
	if err != nil {
		return res, err
	}
	if !det.Detected {
		return res, fmt.Errorf("copilot not detected")
	}
	path, err := hooksFilePath()
	if err != nil {
		return res, err
	}

	if !opts.Force && installed(path) {
		res.Warnings = append(res.Warnings, "quiver hooks already installed (use --force to reinstall)")
		return res, nil
	}
	if opts.DryRun {
		res.HooksAdded = append([]string(nil), hookTypes...)
		return res, nil
	}

	if installed(path) {
		if dir, dErr := ops.NewBackupDir(AgentName); dErr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("backup skipped: %v", dErr))
		} else if bp, cErr := ops.CopyFileInto(path, dir); cErr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("backup skipped: %v", cErr))
		} else {
			res.BackupPath = bp
		}
	}

	data, err := json.MarshalIndent(generateHooksFile(), "", "  ")
	if err != nil {
		return res, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return res, fmt.Errorf("create hooks dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return res, fmt.Errorf("write quiver.json: %w", err)
	}
	res.HooksAdded = append([]string(nil), hookTypes...)
	return res, nil
}

// Uninstall removes quiver.json, restoring a pre-install backup if present.
func (a *Adapter) Uninstall(opts ops.UninstallOptions) (ops.UninstallResult, error) {
	var res ops.UninstallResult

	path, err := hooksFilePath()
	if err != nil {
		return res, err
	}
	if !installed(path) {
		return res, nil
	}
	if opts.DryRun {
		res.HooksRemoved = append([]string(nil), hookTypes...)
		return res, nil
	}

	if backupDir, bErr := ops.LatestBackupDir(AgentName); bErr == nil && backupDir != "" {
		bak := filepath.Join(backupDir, "quiver.json.bak")
		if _, sErr := os.Stat(bak); sErr == nil {
			if err := ops.CopyFile(bak, path); err != nil {
				return res, fmt.Errorf("restore backup: %w", err)
			}
			res.Restored = true
			res.HooksRemoved = append([]string(nil), hookTypes...)
			return res, nil
		}
	}

	if err := os.Remove(path); err != nil {
		return res, fmt.Errorf("remove quiver.json: %w", err)
	}
	res.HooksRemoved = append([]string(nil), hookTypes...)
	return res, nil
}

// Status reports whether quiver.json is present and complete.
func (a *Adapter) Status() (ops.HookStatus, error) {
	var st ops.HookStatus
	path, err := hooksFilePath()
	if err != nil {
		return st, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		st.Issues = append(st.Issues, err.Error())
		return st, nil
	}
	var hf hooksFile
	if err := json.Unmarshal(data, &hf); err != nil {
		st.Issues = append(st.Issues, "quiver.json is malformed")
		return st, nil
	}
	st.Installed = true
	st.Valid = true
	for _, ht := range hookTypes {
		if len(hf.Hooks[ht]) == 0 {
			st.Valid = false
			st.Issues = append(st.Issues, fmt.Sprintf("%s: hook not configured", ht))
		} else {
			st.Hooks = append(st.Hooks, ht)
		}
	}
	return st, nil
}
