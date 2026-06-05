package cursor

import (
	"os"
	"path/filepath"

	"github.com/quiver-cli/qvr/internal/ops"
)

func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cursor"), nil
}

func hooksPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hooks.json"), nil
}

// Detect reports whether Cursor's config dir exists.
func (a *Adapter) Detect() (ops.DetectionResult, error) {
	dir, err := configDir()
	if err != nil {
		return ops.DetectionResult{}, err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ops.DetectionResult{
			Detected: false,
			Message:  "Cursor not detected (~/.cursor not found)",
		}, nil
	}
	return ops.DetectionResult{Detected: true, ConfigPath: dir}, nil
}
