package cmd

import (
	"strings"
	"testing"

	"github.com/raks097/quiver/internal/config"
)

// TestConfigValidator_BlockSeverityAcceptsScannerVocab pins the alignment
// from bug #58: scanner emits info|warning|error|critical, so config must
// accept those values (and only those — no "none/high/medium/low" decoys).
func TestConfigValidator_BlockSeverityAcceptsScannerVocab(t *testing.T) {
	v, ok := configValueValidators["security.block_severity"]
	if !ok {
		t.Fatal("no validator for security.block_severity")
	}
	for _, sev := range []string{"info", "warning", "error", "critical"} {
		if _, err := v(sev); err != nil {
			t.Errorf("%q should validate: %v", sev, err)
		}
	}
	// Old vocab from before #58 — these must now be rejected.
	for _, sev := range []string{"high", "medium", "low", "none"} {
		if _, err := v(sev); err == nil {
			t.Errorf("%q should be rejected (legacy vocab from before #58)", sev)
		}
	}
}

// TestConfigValidator_BlockSeverityErrorMessageListsScannerVocab makes sure
// the rejection message tells the user the right values to try.
func TestConfigValidator_BlockSeverityErrorMessageListsScannerVocab(t *testing.T) {
	v := configValueValidators["security.block_severity"]
	_, err := v("nope")
	if err == nil {
		t.Fatal("expected error on bogus severity")
	}
	for _, want := range []string{"critical", "error", "warning", "info"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message %q should mention %q", err.Error(), want)
		}
	}
	for _, leg := range []string{"high", "medium", "low"} {
		if strings.Contains(err.Error(), leg) {
			t.Errorf("error message %q should NOT mention legacy vocab %q", err.Error(), leg)
		}
	}
}

func TestConfigRead_SecurityPolicyKeysSurface(t *testing.T) {
	cfg := &config.Config{Security: config.SecurityConfig{
		RequireScan:   true,
		RequireSigned: true,
	}}
	cases := []struct {
		key string
		set func() bool
	}{
		{"security.require_scan", func() bool { return cfg.Security.RequireScan }},
		{"security.require_signed", func() bool { return cfg.Security.RequireSigned }},
	}
	for _, tc := range cases {
		if got := configRead(cfg, tc.key); got != "true" {
			t.Errorf("configRead(%s) = %q, want true", tc.key, got)
		}
		if err := configWrite(cfg, tc.key, "false"); err != nil {
			t.Fatalf("configWrite(%s): %v", tc.key, err)
		}
		if tc.set() {
			t.Fatalf("configWrite should set %s=false", tc.key)
		}
		if _, ok := configValueValidators[tc.key]; !ok {
			t.Fatalf("%s missing validator", tc.key)
		}
		var found bool
		for _, k := range knownConfigKeys {
			if k == tc.key {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s missing from knownConfigKeys", tc.key)
		}
	}
}

// TestConfigRead_OpsKeysSurface is the #124 regression. Pre-fix, the
// text view of `qvr config get` iterated only 8 dotted keys (security/
// output/cache/default_*), so `ops.enabled` — the telemetry switch —
// was hidden from the human-readable view but visible in --output json.
// A user dumping their config in the terminal couldn't see what
// telemetry their install was doing. Fix exposes the ops keys through
// configRead so the text loop renders them.
func TestConfigRead_OpsKeysSurface(t *testing.T) {
	cfg := &config.Config{
		Ops: config.OpsConfig{
			Enabled: true,
			DBPath:  "/var/log/qvr.db",
		},
	}
	cases := map[string]string{
		"ops.enabled": "true",
		"ops.db_path": "/var/log/qvr.db",
	}
	for k, want := range cases {
		if got := configRead(cfg, k); got != want {
			t.Errorf("configRead(%q) = %q, want %q", k, got, want)
		}
	}
	for _, k := range []string{"ops.enabled", "ops.db_path"} {
		var found bool
		for _, kk := range knownConfigKeys {
			if kk == k {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q missing from knownConfigKeys — text-mode loop won't print it", k)
		}
	}
}

// TestConfigRead_OpsDisabledStillSurfaces is the partner check: even
// with telemetry disabled, the text view must show `ops.enabled = false`
// so the user sees their privacy posture explicitly. Empty values are
// suppressed by the print loop, but "false" is a real value.
func TestConfigRead_OpsDisabledStillSurfaces(t *testing.T) {
	cfg := &config.Config{} // zero-value: Ops.Enabled = false
	if got := configRead(cfg, "ops.enabled"); got != "false" {
		t.Errorf("configRead(ops.enabled) on zero-cfg = %q, want \"false\" — must render in text view", got)
	}
}
