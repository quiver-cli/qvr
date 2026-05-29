package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/security"
	"github.com/raks097/quiver/internal/skill"
)

// scannerVersion is the qvr release whose rule set produced a ScanRef.
// Stored on lockfile scan entries so a later `qvr lock verify` can detect
// drift even when the scanner has been upgraded since the install.
const scannerVersion = "0.6.1"

// scanGateOptions tunes a single ScanAndGate call.
type scanGateOptions struct {
	// Disabled forces the gate off regardless of cfg.Security.ScanOnInstall.
	// Set by `--no-scan` flags on add/registry/sync/publish.
	Disabled bool
	// Action labels the calling operation in surfaced output ("add", "registry
	// add", "sync restore", "publish"). Used in the rendered banner so the
	// user knows which command produced the findings.
	Action string
	// Subject is the skill name for the banner — e.g. "code-review".
	Subject string
	// WarnOnly tells the renderer to use the ⚠ "found N finding(s)" banner
	// even when findings meet the block threshold. Callers that do not act on
	// scanGateResult.Blocked (sync, which is restorative — the lock already
	// committed to these refs) set this so the surfaced wording matches what
	// the command will actually do. Without this, sync's critical findings
	// were rendered as `✗ scan blocked` even though the skill was restored
	// and linked anyway (bug #59).
	WarnOnly bool
}

// scanGateResult is the outcome of a single ScanAndGate call. Blocked is true
// when the scan ran and at least one finding meets/exceeds the configured
// block_severity threshold. Skipped is true when the gate did not run at all
// (disabled, scan_on_install=false, no skill loadable, etc.) — callers
// distinguish "scan ran clean" from "scan didn't happen" via this field.
type scanGateResult struct {
	Result    *security.ScanResult `json:"result,omitempty"`
	Blocked   bool                 `json:"blocked"`
	Skipped   bool                 `json:"skipped"`
	Threshold security.Severity    `json:"threshold,omitempty"`
}

// ScanAndGate runs the standard scanner against the skill at skillDir and
// applies the cfg.Security.BlockSeverity threshold. Findings are surfaced to
// stderr in text mode (regardless of the global --output) so users always see
// what was flagged, even when the command itself returns a JSON payload.
//
// Returns (result, error). When blocked is true the caller should refuse the
// operation; the surface already happened, so callers should not re-print
// findings.
//
// A nil cfg is treated as the zero SecurityConfig (no scan, no block). When
// opts.Disabled is true the gate is skipped entirely and the returned result
// has Skipped=true with no findings — used for the user-facing `--no-scan`
// path on add/registry/sync/publish.
func ScanAndGate(ctx context.Context, skillDir string, cfg *config.Config, opts scanGateOptions) (*scanGateResult, error) {
	out := &scanGateResult{Skipped: true}
	if opts.Disabled {
		return out, nil
	}
	if cfg == nil || !cfg.Security.ScanOnInstall {
		return out, nil
	}

	loaded, err := skill.LoadFromPath(skillDir)
	if err != nil {
		// A skill that won't load is reported by the validator elsewhere
		// (Install runs validateStagedSkill first). Returning skipped here
		// keeps the gate scoped to security findings — load failures are
		// not the gate's job to surface.
		return out, nil
	}

	threshold, perr := security.ParseSeverity(blockSeverityOrDefault(cfg))
	if perr != nil {
		// Misconfigured block_severity falls back to the safest setting
		// — `critical`. Better to err toward not blocking on bogus input
		// than to refuse every install over a config typo.
		threshold = security.SeverityCritical
	}
	out.Threshold = threshold

	scanner := security.New()
	if p := security.LLMProviderFromEnv(); p != nil {
		scanner = scanner.WithLLMProvider(p)
		for _, lc := range security.BuiltinLLMChecks() {
			scanner = scanner.AddLLM(lc)
		}
	}
	res, err := scanner.Scan(ctx, loaded, skillDir)
	if err != nil {
		return out, fmt.Errorf("scan %s: %w", opts.Subject, err)
	}
	out.Result = res
	out.Skipped = false
	out.Blocked = exceedsThreshold(res, threshold)

	// WarnOnly callers (sync) never block — render the warning template even
	// when the finding meets threshold so the surfaced wording matches what
	// the command will actually do (bug #59).
	renderBlocked := out.Blocked && !opts.WarnOnly
	renderGateFindings(opts, res, threshold, renderBlocked)
	return out, nil
}

// blockSeverityOrDefault returns the configured block severity, falling back
// to "critical" when unset.
func blockSeverityOrDefault(cfg *config.Config) string {
	if cfg != nil && cfg.Security.BlockSeverity != "" {
		return cfg.Security.BlockSeverity
	}
	return string(security.SeverityCritical)
}

// renderGateFindings prints a compact, human-readable summary of any scan
// findings to stderr. We deliberately render to stderr — never stdout —
// because callers may be in JSON mode and stdout is reserved for the
// structured payload.
//
// Clean scans are silent so successful installs read normally. Any finding
// triggers a banner, a table of findings, and (if blocked) a "refusing to
// proceed" hint with the --no-scan escape hatch.
func renderGateFindings(opts scanGateOptions, res *security.ScanResult, threshold security.Severity, blocked bool) {
	if res == nil || len(res.Findings) == 0 {
		return
	}
	action := opts.Action
	if action == "" {
		action = "scan"
	}
	subject := opts.Subject
	if subject == "" {
		subject = res.Skill
	}
	banner := fmt.Sprintf("⚠ %s %s: scan found %d finding(s) (max %s; block threshold %s)",
		action, subject, res.Summary.Total(), res.Summary.MaxSeverity(), threshold)
	if blocked {
		banner = fmt.Sprintf("✗ %s %s: scan blocked (max %s ≥ threshold %s)",
			action, subject, res.Summary.MaxSeverity(), threshold)
	}
	fmt.Fprintln(printer.Err, banner)
	// Render only findings at or above the threshold when blocking; otherwise
	// show everything so the user has a complete picture of what was flagged.
	display := res.Findings
	if blocked {
		display = security.Filter(res.Findings, threshold)
	}
	for _, f := range display {
		loc := f.File
		if f.Line > 0 {
			loc = loc + ":" + strconv.Itoa(f.Line)
		}
		fmt.Fprintf(printer.Err, "  [%s] %s — %s",
			strings.ToUpper(string(f.Severity)), f.Check, f.Message)
		if loc != "" {
			fmt.Fprintf(printer.Err, " (%s)", loc)
		}
		fmt.Fprintln(printer.Err)
		if f.Remediation != "" {
			fmt.Fprintf(printer.Err, "    → %s\n", f.Remediation)
		}
	}
	if blocked {
		fmt.Fprintln(printer.Err, "  Pass --no-scan to override, or `qvr config set security.block_severity <higher>` to relax the gate.")
	}
}

// blockedScanError is the typed error returned when a gate blocks an
// operation. Callers (cmd-level) can wrap it with rollback information.
type blockedScanError struct {
	Subject   string
	Threshold security.Severity
	Result    *security.ScanResult
}

func (e *blockedScanError) Error() string {
	if e == nil {
		return "scan blocked"
	}
	max := security.Severity("")
	if e.Result != nil {
		max = e.Result.Summary.MaxSeverity()
	}
	return fmt.Sprintf("scan blocked %s (max severity %s ≥ threshold %s)",
		e.Subject, max, e.Threshold)
}

// gateAvailable reports whether the gate would do anything for this
// configuration. Useful when callers want to short-circuit expensive
// preparation work (e.g. registry add's per-skill temp worktree
// materialization).
func gateAvailable(cfg *config.Config, disabled bool) bool {
	if disabled {
		return false
	}
	if cfg == nil {
		return false
	}
	return cfg.Security.ScanOnInstall
}

// ensure the output import is retained even if all renderers use printer.Err
// directly.
var _ output.Format = output.FormatText

// toScanRef condenses a scanGateResult into the compact form persisted on a
// lock entry's verification block. Returns nil when the gate was skipped
// (scan_on_install=false, --no-scan, etc.) — the caller treats that as "no
// scan signal to record" and leaves entry.Verification untouched.
func toScanRef(gate *scanGateResult) *model.ScanRef {
	if gate == nil || gate.Skipped || gate.Result == nil {
		return nil
	}
	decision := "allowed"
	if gate.Blocked {
		decision = "blocked"
	}
	return &model.ScanRef{
		ReportSHA:      hashScanResult(gate.Result),
		ScannerVersion: scannerVersion,
		Counts:         severityCountsFromSummary(gate.Result.Summary),
		Decision:       decision,
	}
}

// hashScanResult fingerprints the structured ScanResult so `qvr lock verify`
// can detect drift without re-running the scan. Uses encoding/json which
// sorts map keys, so the same content always produces the same digest.
// Marshal failure returns an empty string — ScanResult is JSON-clean by
// construction, so this is defensive only.
func hashScanResult(result *security.ScanResult) string {
	data, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// severityCountsFromSummary maps the scanner's 4-rung severity ladder
// (info/warning/error/critical) onto the lockfile's 5-rung shape
// (info/low/medium/high/critical). The scanner has no "low" equivalent so
// that bucket stays zero; the rest map straight across.
func severityCountsFromSummary(s security.Summary) model.SeverityCounts {
	return model.SeverityCounts{
		Critical: s.Critical,
		High:     s.Error,
		Medium:   s.Warning,
		Info:     s.Info,
	}
}

// recordScanResult writes the gate's result into the named entry's
// verification.scan slot on the lockfile at lockPath. No-op when the gate
// produced no recordable signal (skipped, no result). Should be called
// inside the same WithLock window that performed the install — otherwise a
// concurrent qvr command could see the lock briefly without the scan
// record.
func recordScanResult(lockPath, name string, gate *scanGateResult) error {
	scan := toScanRef(gate)
	if scan == nil {
		return nil
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return fmt.Errorf("read lock for scan record: %w", err)
	}
	entry, err := lock.Get(name)
	if err != nil {
		return fmt.Errorf("locate %s in lock: %w", name, err)
	}
	if entry.Verification == nil {
		entry.Verification = &model.VerificationRecord{}
	}
	entry.Verification.Scan = scan
	lock.Put(entry)
	if err := lock.Write(); err != nil {
		return fmt.Errorf("write lock for scan record: %w", err)
	}
	return nil
}
