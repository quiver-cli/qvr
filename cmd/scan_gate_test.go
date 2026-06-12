package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/security"
)

// writeCleanSkill writes a minimal valid skill that scans clean.
func writeCleanSkill(t *testing.T, dir, name string) string {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: clean skill for gate tests\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return skillDir
}

// writeSkillWithSecret writes a skill whose SKILL.md contains a known AKIA
// AWS access key pattern — this trips the secrets check at SeverityCritical
// per internal/security/secrets.go.
func writeSkillWithSecret(t *testing.T, dir, name string) string {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `---
name: ` + name + `
description: skill that leaks a credential to trip the gate
---
# ` + name + `

Internal credential reference (please ignore): AKIAIOSFODNN7EXAMPLE
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return skillDir
}

func TestScanAndGate_DisabledByFlag(t *testing.T) {
	resetPrinter(t)
	dir := writeSkillWithSecret(t, t.TempDir(), "dangerous")
	cfg := &config.Config{Security: config.SecurityConfig{ScanOnInstall: true, BlockSeverity: "critical"}}

	got, err := scanAndGate(context.Background(), dir, cfg, scanGateOptions{Disabled: true})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !got.Skipped {
		t.Error("expected Skipped=true when Disabled")
	}
	if got.Blocked {
		t.Error("expected Blocked=false when Disabled")
	}
}

func TestScanAndGate_RequireScanRejectsNoScan(t *testing.T) {
	resetPrinter(t)
	dir := writeSkillWithSecret(t, t.TempDir(), "dangerous")
	cfg := &config.Config{Security: config.SecurityConfig{
		ScanOnInstall: true,
		RequireScan:   true,
		BlockSeverity: "critical",
	}}

	got, err := scanAndGate(context.Background(), dir, cfg, scanGateOptions{Disabled: true})
	if err == nil {
		t.Fatal("expected require_scan to reject --no-scan")
	}
	if !strings.Contains(err.Error(), "security.require_scan") {
		t.Fatalf("error = %q, want security.require_scan context", err.Error())
	}
	if got == nil || !got.Skipped {
		t.Fatalf("gate result = %+v, want skipped result", got)
	}
}

func TestScanAndGate_DisabledByConfig(t *testing.T) {
	resetPrinter(t)
	dir := writeSkillWithSecret(t, t.TempDir(), "dangerous")
	cfg := &config.Config{Security: config.SecurityConfig{ScanOnInstall: false, BlockSeverity: "critical"}}

	got, err := scanAndGate(context.Background(), dir, cfg, scanGateOptions{})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !got.Skipped {
		t.Error("scan_on_install=false should skip the gate")
	}
}

func TestScanAndGate_BlocksAtCritical(t *testing.T) {
	resetPrinter(t)
	dir := writeSkillWithSecret(t, t.TempDir(), "leaky")
	cfg := &config.Config{Security: config.SecurityConfig{ScanOnInstall: true, BlockSeverity: "critical"}}

	got, err := scanAndGate(context.Background(), dir, cfg, scanGateOptions{
		Action:  "add",
		Subject: "leaky",
	})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if got.Skipped {
		t.Fatal("gate should have run")
	}
	if !got.Blocked {
		t.Fatalf("expected block on critical finding, got summary=%+v", got.Result.Summary)
	}
	if got.Threshold != security.SeverityCritical {
		t.Errorf("threshold = %s, want critical", got.Threshold)
	}
}

func TestScanAndGate_ProceedsWhenBelowThreshold(t *testing.T) {
	resetPrinter(t)
	// Plant a critical finding but set the threshold to a value above it —
	// there is no severity above critical, so we use a permissive threshold.
	// Easier: scan a clean skill with critical threshold; expect not-blocked.
	dir := writeCleanSkill(t, t.TempDir(), "clean")
	cfg := &config.Config{Security: config.SecurityConfig{ScanOnInstall: true, BlockSeverity: "critical"}}

	got, err := scanAndGate(context.Background(), dir, cfg, scanGateOptions{})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if got.Skipped {
		t.Fatal("gate should have run")
	}
	if got.Blocked {
		t.Errorf("clean skill should not be blocked: %+v", got.Result.Summary)
	}
}

func TestScanAndGate_SurfacesFindingsToStderr(t *testing.T) {
	resetPrinter(t)
	dir := writeSkillWithSecret(t, t.TempDir(), "noisy")
	cfg := &config.Config{Security: config.SecurityConfig{ScanOnInstall: true, BlockSeverity: "critical"}}

	if _, err := scanAndGate(context.Background(), dir, cfg, scanGateOptions{
		Action:  "add",
		Subject: "noisy",
	}); err != nil {
		t.Fatalf("gate: %v", err)
	}
	stderr, ok := printer.Err.(interface{ String() string })
	if !ok {
		t.Skip("printer.Err is not a string buffer; nothing to assert")
	}
	out := stderr.String()
	if !strings.Contains(out, "noisy") {
		t.Errorf("stderr should mention subject %q, got: %s", "noisy", out)
	}
	if !strings.Contains(strings.ToUpper(out), "CRITICAL") {
		t.Errorf("stderr should surface a CRITICAL finding, got: %s", out)
	}
}

// TestScanAndGate_WarnOnlyUsesWarningTemplateForCriticalFindings pins the
// fix for bug #59: sync's gate sets WarnOnly=true because sync never blocks
// (the lock already committed to these refs), so even critical findings
// should render with the `warning: … scan found` template — not the
// `error: … scan blocked` template that misled users into thinking the
// restore was aborted.
//
// The gate still reports Blocked=true so callers can decide what to do; only
// the rendered wording changes.
func TestScanAndGate_WarnOnlyUsesWarningTemplateForCriticalFindings(t *testing.T) {
	resetPrinter(t)
	dir := writeSkillWithSecret(t, t.TempDir(), "leaky")
	cfg := &config.Config{Security: config.SecurityConfig{ScanOnInstall: true, BlockSeverity: "critical"}}

	got, err := scanAndGate(context.Background(), dir, cfg, scanGateOptions{
		Action:   "sync",
		Subject:  "leaky",
		WarnOnly: true,
	})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !got.Blocked {
		t.Fatal("gate should still mark Blocked=true so callers can decide")
	}
	stderr, ok := printer.Err.(interface{ String() string })
	if !ok {
		t.Skip("printer.Err is not a string buffer")
	}
	out := stderr.String()
	if strings.Contains(out, "scan blocked") {
		t.Errorf("WarnOnly should NOT use the blocked template, got:\n%s", out)
	}
	if !strings.Contains(out, "scan found") {
		t.Errorf("WarnOnly should use the warning template, got:\n%s", out)
	}
	// The remediation hint about --no-scan / block_severity is for the
	// blocking path — sync shouldn't print it because it doesn't block.
	if strings.Contains(out, "pass --no-scan to override") {
		t.Errorf("WarnOnly should suppress the blocking remediation hint, got:\n%s", out)
	}
}

func TestScanAndGate_BogusThresholdFallsBackToCritical(t *testing.T) {
	resetPrinter(t)
	dir := writeSkillWithSecret(t, t.TempDir(), "noisy")
	// Intentionally bogus block_severity. The gate must NOT crash and must
	// fall back to critical so it doesn't accidentally block-on-everything.
	cfg := &config.Config{Security: config.SecurityConfig{ScanOnInstall: true, BlockSeverity: "potato"}}

	got, err := scanAndGate(context.Background(), dir, cfg, scanGateOptions{})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if got.Threshold != security.SeverityCritical {
		t.Errorf("bogus threshold should fall back to critical, got %s", got.Threshold)
	}
}
