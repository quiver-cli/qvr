package opencode

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/quiver-cli/qvr/internal/ops"
)

//go:embed quiver.js
var pluginJS []byte

const (
	pluginFileName = "quiver.js"
	commandMarker  = "__QUIVER_COMMAND__"
)

// capturedEvents is the set of OpenCode events the plugin forwards. Listed
// for Status/Install reporting; the plugin file itself is the source of truth.
var capturedEvents = []string{
	"tool.execute.before",
	"tool.execute.after",
	"session.created",
	"session.idle",
	"session.error",
}

// configDir returns ~/.config/opencode (honoring XDG_CONFIG_HOME).
func configDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode"), nil
}

func pluginPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "plugins", pluginFileName), nil
}

// processedPlugin returns the embedded plugin with the command placeholder
// replaced by the resolved qvr binary path.
func processedPlugin() []byte {
	return bytes.ReplaceAll(pluginJS, []byte(commandMarker), []byte(ops.BinaryPath()))
}

// Detect reports whether OpenCode's config dir exists.
func (a *Adapter) Detect() (ops.DetectionResult, error) {
	dir, err := configDir()
	if err != nil {
		return ops.DetectionResult{}, err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ops.DetectionResult{
			Detected: false,
			Message:  "OpenCode not detected (~/.config/opencode not found)",
		}, nil
	}
	return ops.DetectionResult{Detected: true, ConfigPath: dir}, nil
}

// installed reports whether the Quiver plugin file is present.
func installed(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Install writes the embedded plugin to ~/.config/opencode/plugins/quiver.js.
func (a *Adapter) Install(opts ops.InstallOptions) (ops.InstallResult, error) {
	var res ops.InstallResult

	det, err := a.Detect()
	if err != nil {
		return res, err
	}
	if !det.Detected {
		return res, fmt.Errorf("opencode not detected")
	}
	path, err := pluginPath()
	if err != nil {
		return res, err
	}

	if !opts.Force && installed(path) {
		res.Warnings = append(res.Warnings, "quiver plugin already installed (use --force to reinstall)")
		return res, nil
	}
	if opts.DryRun {
		res.HooksAdded = append([]string(nil), capturedEvents...)
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

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return res, fmt.Errorf("create plugins dir: %w", err)
	}
	if err := os.WriteFile(path, processedPlugin(), 0o644); err != nil {
		return res, fmt.Errorf("write plugin: %w", err)
	}
	res.HooksAdded = append([]string(nil), capturedEvents...)
	return res, nil
}

// Uninstall removes the plugin file, restoring a pre-install backup when one
// exists (so a user's own quiver.js, if any, is preserved).
func (a *Adapter) Uninstall(opts ops.UninstallOptions) (ops.UninstallResult, error) {
	var res ops.UninstallResult

	path, err := pluginPath()
	if err != nil {
		return res, err
	}
	if !installed(path) {
		return res, nil
	}
	if opts.DryRun {
		res.HooksRemoved = append([]string(nil), capturedEvents...)
		return res, nil
	}

	if backupDir, bErr := ops.LatestBackupDir(AgentName); bErr == nil && backupDir != "" {
		bak := filepath.Join(backupDir, pluginFileName+".bak")
		if _, sErr := os.Stat(bak); sErr == nil {
			if err := ops.CopyFile(bak, path); err != nil {
				return res, fmt.Errorf("restore backup: %w", err)
			}
			res.Restored = true
			res.HooksRemoved = append([]string(nil), capturedEvents...)
			return res, nil
		}
	}

	if err := os.Remove(path); err != nil {
		return res, fmt.Errorf("remove plugin: %w", err)
	}
	res.HooksRemoved = append([]string(nil), capturedEvents...)
	return res, nil
}

// Status reports whether the plugin is present and references qvr.
func (a *Adapter) Status() (ops.HookStatus, error) {
	var st ops.HookStatus
	path, err := pluginPath()
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
	st.Installed = true
	st.Hooks = append([]string(nil), capturedEvents...)
	// Valid when the plugin forwards to our hook command.
	if bytes.Contains(data, []byte(`"opencode"`)) && bytes.Contains(data, []byte("_hook")) {
		st.Valid = true
	} else {
		st.Issues = append(st.Issues, "plugin present but does not reference qvr _hook opencode")
	}
	return st, nil
}
