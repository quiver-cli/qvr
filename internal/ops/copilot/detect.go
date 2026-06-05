package copilot

import (
	"os"
	"path/filepath"

	"github.com/quiver-cli/qvr/internal/ops"
)

// copilotHome returns the Copilot CLI config dir, honoring COPILOT_HOME.
func copilotHome() (string, error) {
	if env := os.Getenv("COPILOT_HOME"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".copilot"), nil
}

func hooksFilePath() (string, error) {
	dir, err := copilotHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hooks", "quiver.json"), nil
}

// Detect reports whether the Copilot CLI config dir exists.
func (a *Adapter) Detect() (ops.DetectionResult, error) {
	dir, err := copilotHome()
	if err != nil {
		return ops.DetectionResult{}, err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ops.DetectionResult{
			Detected: false,
			Message:  "GitHub Copilot CLI not detected (~/.copilot not found)",
		}, nil
	}
	return ops.DetectionResult{Detected: true, ConfigPath: dir}, nil
}
