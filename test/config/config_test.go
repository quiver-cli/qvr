package configtests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/raks097/quiver/internal/config"
)

func TestDefault(t *testing.T) {
	cfg := config.Default()
	if cfg.DefaultTarget != "claude" {
		t.Errorf("default target = %q, want %q", cfg.DefaultTarget, "claude")
	}
	if !cfg.Security.ScanOnInstall {
		t.Error("scan_on_install should default to true")
	}
	if cfg.Security.BlockSeverity != "critical" {
		t.Errorf("block_severity = %q, want %q", cfg.Security.BlockSeverity, "critical")
	}
	if cfg.Registries == nil {
		t.Error("registries should be initialized")
	}
}

func TestSaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("QUIVER_HOME", tmpDir)

	cfg := config.Default()
	cfg.DefaultTarget = "cursor"
	cfg.Registries["test"] = config.RegistryConfig{URL: "https://example.com/test.git"}

	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filepath.Join(tmpDir, "config.yaml")); err != nil {
		t.Fatalf("config file not found: %v", err)
	}

	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if loaded.DefaultTarget != "cursor" {
		t.Errorf("loaded default_target = %q, want %q", loaded.DefaultTarget, "cursor")
	}
	if loaded.Registries["test"].URL != "https://example.com/test.git" {
		t.Errorf("loaded registry url = %q", loaded.Registries["test"].URL)
	}
}

func TestLoad_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("QUIVER_HOME", tmpDir)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.DefaultTarget != "claude" {
		t.Errorf("should return defaults, got target = %q", cfg.DefaultTarget)
	}
}

func TestDir_Override(t *testing.T) {
	t.Setenv("QUIVER_HOME", "/custom/path")
	if config.Dir() != "/custom/path" {
		t.Errorf("Dir() = %q, want /custom/path", config.Dir())
	}
}

// TestParseDefaultTargets covers the comma-list semantics that resolve
// the singular default_target / plural --target / TARGETS-column
// vocabulary mismatch flagged in qvr issues #5.
func TestParseDefaultTargets(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"claude", []string{"claude"}},
		{"claude,cursor", []string{"claude", "cursor"}},
		{" claude , cursor ", []string{"claude", "cursor"}},
		{"claude,,cursor", []string{"claude", "cursor"}},
		{",", nil},
	}
	for _, tc := range cases {
		got := config.ParseDefaultTargets(tc.in)
		if !equalSlices(got, tc.want) {
			t.Errorf("ParseDefaultTargets(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
