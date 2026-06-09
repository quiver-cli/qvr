package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/ops"
)

// setupHome points $HOME and $QVR_HOME at temp dirs so Detect (which reads
// ~/.claude) and the backup helpers (which read $QUIVER_HOME) stay isolated.
// It creates ~/.claude so Detect reports Detected=true.
func setupHome(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("QVR_HOME", filepath.Join(home, ".quiver"))
	// Neutralize any ambient CLAUDE_CONFIG_DIR (the dev machine may have it set)
	// so these tests resolve to the temp ~/.claude.
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	return home
}

func readSettingsFile(t *testing.T, home string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal settings.json: %v", err)
	}
	return m
}

// seedSettings writes the given JSON into the temp ~/.claude/settings.json.
func seedSettings(t *testing.T, home, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(contents), 0o600); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}
}

// assertStatusInstalledValid fails unless Status reports installed+valid.
func assertStatusInstalledValid(t *testing.T, a *Adapter) {
	t.Helper()
	st, err := a.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Installed || !st.Valid {
		t.Errorf("Status = %+v, want installed+valid", st)
	}
}

func TestInstallUninstallRoundTrip(t *testing.T) {
	home := setupHome(t)
	a := &Adapter{}

	// Seed an existing settings.json so install has something to back up
	// and uninstall can restore it verbatim.
	seedSettings(t, home, `{"model":"opus"}`+"\n")

	// Install.
	res, err := a.Install(ops.InstallOptions{})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(res.HooksAdded) != len(hookTypes) {
		t.Errorf("HooksAdded = %d, want %d", len(res.HooksAdded), len(hookTypes))
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("settings.json not written: %v", err)
	}
	if !strings.Contains(string(raw), "_hook "+AgentName+" PostToolUse") {
		t.Errorf("settings.json missing quiver hook command:\n%s", raw)
	}

	// Status: installed + valid.
	assertStatusInstalledValid(t, a)

	// Re-install without force is a no-op warning.
	res2, err := a.Install(ops.InstallOptions{})
	if err != nil {
		t.Fatalf("re-Install: %v", err)
	}
	if len(res2.HooksAdded) != 0 {
		t.Errorf("re-install added hooks again: %v", res2.HooksAdded)
	}
	if len(res2.Warnings) == 0 {
		t.Error("re-install should warn that hooks already exist")
	}

	// Uninstall restores the (empty) pre-install backup.
	ures, err := a.Uninstall(ops.UninstallOptions{})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !ures.Restored {
		t.Error("Uninstall should have restored from backup")
	}
	st2, err := a.Status()
	if err != nil {
		t.Fatalf("Status after uninstall: %v", err)
	}
	if st2.Installed {
		t.Errorf("hooks still installed after uninstall: %+v", st2)
	}
}

func TestInstallPreservesUserHooks(t *testing.T) {
	home := setupHome(t)
	// Seed settings.json with a user hook and an unrelated top-level key.
	seed := `{
  "model": "opus",
  "hooks": {
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "my-own-script"}]}]
  }
}`
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(seed), 0o600); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}

	a := &Adapter{}
	if _, err := a.Install(ops.InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	settings := readSettingsFile(t, home)
	if _, ok := settings["model"]; !ok {
		t.Error("top-level 'model' key was dropped")
	}
	hooks := unmarshalHooks(t, settings["hooks"])
	// PreToolUse should contain both the user's matcher and ours.
	var sawUser, sawQuiver bool
	for _, m := range hooks["PreToolUse"] {
		for _, h := range m.Hooks {
			if h.Command == "my-own-script" {
				sawUser = true
			}
			if isQuiverCommand(h.Command) {
				sawQuiver = true
			}
		}
	}
	if !sawUser {
		t.Error("user's PreToolUse hook was lost")
	}
	if !sawQuiver {
		t.Error("quiver PreToolUse hook was not added")
	}

	// Uninstall (no backup of the seeded file existed before *this* install;
	// the install backed it up, so restore brings back exactly the seed).
	if _, err := a.Uninstall(ops.UninstallOptions{}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	after := readSettingsFile(t, home)
	hooksAfter := unmarshalHooks(t, after["hooks"])
	for _, m := range hooksAfter["PreToolUse"] {
		for _, h := range m.Hooks {
			if isQuiverCommand(h.Command) {
				t.Error("quiver hook survived uninstall")
			}
		}
	}
}

// unmarshalHooks decodes a settings "hooks" section into the per-type matcher
// map, failing the test on a decode error.
func unmarshalHooks(t *testing.T, raw json.RawMessage) map[string][]hookMatcher {
	t.Helper()
	var hooks map[string][]hookMatcher
	if err := json.Unmarshal(raw, &hooks); err != nil {
		t.Fatalf("unmarshal hooks: %v", err)
	}
	return hooks
}

// TestInstallHonorsConfigDirEnv verifies CLAUDE_CONFIG_DIR redirects install
// (and status/detect) at an isolated config dir, never touching ~/.claude.
func TestInstallHonorsConfigDirEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("QVR_HOME", filepath.Join(home, ".quiver"))
	// Live ~/.claude exists but must be left untouched.
	liveSettings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	if err := os.WriteFile(liveSettings, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("seed live settings.json: %v", err)
	}

	iso := filepath.Join(home, "iso")
	if err := os.MkdirAll(iso, 0o755); err != nil {
		t.Fatalf("mkdir iso: %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", iso)

	a := &Adapter{}
	if _, err := a.Install(ops.InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Hooks landed in the isolated dir...
	isoRaw, err := os.ReadFile(filepath.Join(iso, "settings.json"))
	if err != nil {
		t.Fatalf("isolated settings.json not written: %v", err)
	}
	if !strings.Contains(string(isoRaw), "_hook "+AgentName) {
		t.Errorf("isolated settings.json missing quiver hook:\n%s", isoRaw)
	}

	// ...and the live config was never touched.
	liveRaw, err := os.ReadFile(liveSettings)
	if err != nil {
		t.Fatalf("read live settings.json: %v", err)
	}
	if strings.Contains(string(liveRaw), "_hook") {
		t.Errorf("live ~/.claude/settings.json was mutated:\n%s", liveRaw)
	}

	st, err := a.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Installed || !st.Valid {
		t.Errorf("Status = %+v, want installed+valid against isolated dir", st)
	}
}

func TestDetectNotInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("QVR_HOME", filepath.Join(home, ".quiver"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	// No ~/.claude created.
	a := &Adapter{}
	det, err := a.Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if det.Detected {
		t.Error("Detect reported true with no ~/.claude")
	}
}
