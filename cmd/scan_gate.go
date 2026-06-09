package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/security"
	"github.com/astra-sh/qvr/internal/skill"
)

// scannerVersion is the qvr release whose rule set produced a ScanRef.
// Stored on lockfile scan entries so a later `qvr lock verify` can detect
// drift even when the scanner has been upgraded since the install.
const scannerVersion = "0.7.0"

var errScanRequired = errors.New("security.require_scan is true; --no-scan is not allowed")

func enforceScanPolicy(cfg *config.Config, disabled bool) error {
	if disabled && cfg != nil && cfg.Security.RequireScan {
		return errScanRequired
	}
	return nil
}

// scanGateOptions tunes a single ScanAndGate call.
type scanGateOptions struct {
	// Disabled forces the gate off regardless of cfg.Security.ScanOnInstall.
	// Set by `--no-scan` flags on add/sync/publish.
	Disabled bool
	// Action labels the calling operation in surfaced output ("add", "registry
	// add", "sync restore", "publish"). Used in the rendered banner so the
	// user knows which command produced the findings.
	Action string
	// Subject is the skill name for the banner — e.g. "code-review".
	Subject string
	// WarnOnly tells the renderer to use the `warning: … found N findings`
	// banner even when findings meet the block threshold. Callers that do not
	// act on scanGateResult.Blocked (sync, which is restorative — the lock
	// already committed to these refs) set this so the surfaced wording
	// matches what the command will actually do. Without this, sync's critical
	// findings were rendered as `error: … scan blocked` even though the skill
	// was restored and linked anyway (bug #59).
	WarnOnly bool
	// Quiet suppresses the per-finding detail lines when the gate's
	// decision is "allowed", showing only the one-line banner. Used by
	// `qvr add` so a first-time install with N benign LOW findings doesn't
	// dump N WARNING lines and make new users think something is wrong.
	// Blocked decisions ignore Quiet — the user needs the full picture
	// when something refuses to install. OSS-readiness finding.
	Quiet bool
	// QuietHint overrides the follow-up text shown when Quiet suppresses
	// per-finding details. Registry entries are not necessarily installed yet,
	// so their hint differs from the default `qvr scan <skill>` path.
	QuietHint string
	// ReportOnlyBlocked suppresses rendering when a scan ran but no finding met
	// the block threshold. Registry add uses this to keep source registration
	// output focused on entries that would matter at install time.
	ReportOnlyBlocked bool
}

// scanGateResult is the outcome of a single ScanAndGate call. Blocked is true
// when the scan ran and at least one finding meets/exceeds the configured
// block_severity threshold. Skipped is true when the gate did not run at all
// (disabled, scan_on_install=false, no skill loadable, etc.) — callers
// distinguish "scan ran clean" from "scan didn't happen" via this field.
//
// DisabledByFlag is true when the skip was a deliberate `--no-scan` opt-out
// (vs scan_on_install=false in config). The lockfile records the former as
// a "skipped" sentinel for attestation purposes (issue #78); the latter
// means scanning is just not configured and is not per-entry attested.
type scanGateResult struct {
	Result         *security.ScanResult `json:"result,omitempty"`
	Blocked        bool                 `json:"blocked"`
	Skipped        bool                 `json:"skipped"`
	DisabledByFlag bool                 `json:"disabledByFlag,omitempty"`
	Threshold      security.Severity    `json:"threshold,omitempty"`
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
	if err := enforceScanPolicy(cfg, opts.Disabled); err != nil {
		return out, err
	}
	if opts.Disabled {
		// Distinguish "user explicitly opted out for this call" from "scanning
		// isn't configured" so toScanRef can mint a "skipped" sentinel only
		// for the deliberate-flag case (issues #78, #71).
		out.DisabledByFlag = true
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
	res.Lint = lintReportFor(loaded)
	out.Result = res
	out.Skipped = false
	out.Blocked = exceedsThreshold(res, threshold)
	if opts.ReportOnlyBlocked && !out.Blocked {
		return out, nil
	}

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
	if res == nil {
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
	style := printer.StyleErr()
	// Lint is advisory and surfaced whether or not there are security findings,
	// so a non-conformant-but-safe skill still warns. Suppressed in Quiet mode
	// (batch `add --all`) to keep first-time installs uncluttered.
	if res.Lint != nil && !res.Lint.Valid && !opts.Quiet {
		fmt.Fprintf(printer.Err, "%s %s %s: lint found %s %s\n",
			style.BoldYellow("warning:"), action, subject,
			output.Plural(res.Lint.Count, "issue"),
			style.Dim(fmt.Sprintf("(advisory — install proceeds; run `qvr lint %s`)", subject)))
	}
	if len(res.Findings) == 0 {
		return
	}
	banner := fmt.Sprintf("%s %s %s: scan found %s %s",
		style.BoldYellow("warning:"), action, subject,
		output.Plural(res.Summary.Total(), "finding"),
		style.Dim(fmt.Sprintf("(max %s; block threshold %s)", res.Summary.MaxSeverity(), threshold)))
	if blocked {
		banner = fmt.Sprintf("%s %s %s: scan blocked %s",
			style.BoldRed("error:"), action, subject,
			style.Dim(fmt.Sprintf("(max %s ≥ threshold %s)", res.Summary.MaxSeverity(), threshold)))
	}
	fmt.Fprintln(printer.Err, banner)
	// Quiet+allowed → summary line only. `qvr add` sets this so a
	// first-time install with benign findings doesn't dump N WARNING
	// lines and scare users off. `qvr scan` keeps Quiet=false so the
	// full report still appears when the user explicitly asked for it.
	if opts.Quiet && !blocked {
		hint := opts.QuietHint
		if hint == "" {
			hint = fmt.Sprintf("run `qvr scan %s` to see the full report", subject)
		}
		fmt.Fprintf(printer.Err, "%s %s\n", style.BoldCyan("hint:"), hint)
		return
	}
	// Render only findings at or above the threshold when blocking; otherwise
	// show everything so the user has a complete picture of what was flagged.
	display := res.Findings
	if blocked {
		display = security.Filter(res.Findings, threshold)
	}
	renderScanFindingLines(display)
	if blocked {
		fmt.Fprintf(printer.Err, "%s pass --no-scan to override, or `qvr config set security.block_severity <higher>` to relax the gate\n",
			style.BoldCyan("hint:"))
	}
}

// severityTag renders a finding's severity as a colour-coded `[SEVERITY]`
// tag: critical/error red, warning yellow, everything else dim.
func severityTag(style output.Styler, sev security.Severity) string {
	tag := "[" + strings.ToUpper(string(sev)) + "]"
	switch sev {
	case security.SeverityCritical, security.SeverityError:
		return style.Red(tag)
	case security.SeverityWarning:
		return style.Yellow(tag)
	default:
		return style.Dim(tag)
	}
}

// renderScanFindingLines prints one stderr block per finding: severity/check/
// message, optional file:line location, and remediation hint.
func renderScanFindingLines(findings []security.Finding) {
	style := printer.StyleErr()
	for _, f := range findings {
		loc := f.File
		if f.Line > 0 {
			loc = loc + ":" + strconv.Itoa(f.Line)
		}
		fmt.Fprintf(printer.Err, "  %s %s — %s",
			severityTag(style, f.Severity), f.Check, f.Message)
		if loc != "" {
			fmt.Fprintf(printer.Err, " %s", style.Dim("("+loc+")"))
		}
		fmt.Fprintln(printer.Err)
		if f.Remediation != "" {
			fmt.Fprintf(printer.Err, "    %s\n", style.Dim("→ "+f.Remediation))
		}
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

// toScanRef condenses a scanGateResult into the compact form persisted on a
// lock entry's verification block.
//
// Returns nil when the gate didn't run for a non-deliberate reason
// (scan_on_install=false in config, skill failed to load, etc.) — the caller
// leaves entry.Verification untouched.
//
// Returns a "skipped" sentinel when the gate was deliberately disabled via
// `--no-scan`. The sentinel carries no ReportSHA/Counts but does set
// Reason="--no-scan" so a downstream attestation pipeline can distinguish
// "scanned and clean" (Decision=="allowed", non-zero counts possible) from
// "scan was deliberately skipped" (Decision=="skipped"). Issue #78.
func toScanRef(gate *scanGateResult) *model.ScanRef {
	if gate == nil {
		return nil
	}
	if gate.Skipped {
		if gate.DisabledByFlag {
			return &model.ScanRef{
				Decision: "skipped",
				Reason:   "--no-scan",
			}
		}
		return nil
	}
	if gate.Result == nil {
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
//
// Hashes a stripped view that omits ScannedAt: the wall-clock timestamp
// changes on every run and makes reportSHA non-deterministic for the same
// input, which violates the uv-parity idempotency invariant (a no-op
// `qvr add <already-installed-skill>` would otherwise rewrite the lockfile
// every time — issue #77). The Path field is also dropped because two
// machines scanning the same skill from different absolute paths would
// otherwise disagree on reportSHA.
//
// Marshal failure returns an empty string — ScanResult is JSON-clean by
// construction, so this is defensive only.
func hashScanResult(result *security.ScanResult) string {
	if result == nil {
		return ""
	}
	// Hash a snapshot view that omits machine-/run-specific fields.
	view := struct {
		Skill      string               `json:"skill"`
		Checks     []string             `json:"checks"`
		Components []security.Component `json:"components,omitempty"`
		Findings   []security.Finding   `json:"findings"`
		Summary    security.Summary     `json:"summary"`
	}{
		Skill:      result.Skill,
		Checks:     result.Checks,
		Components: result.Components,
		Findings:   result.Findings,
		Summary:    result.Summary,
	}
	data, err := json.Marshal(view)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// lintReportFor runs the advisory spec lint over a loaded skill and converts
// it into the security-local shape that travels on ScanResult.Lint. Lint never
// blocks — this only attaches the report so `qvr scan`, the install gate, and
// the dashboard can surface "is this skill well-formed?" next to the security
// findings. Returns nil for a nil skill so callers can attach unconditionally.
func lintReportFor(s *model.Skill) *security.LintReport {
	if s == nil {
		return nil
	}
	lr := skill.Lint(s)
	issues := make([]security.LintIssue, 0, len(lr.Errors))
	for _, e := range lr.Errors {
		issues = append(issues, security.LintIssue{
			Field:    e.Field,
			Message:  e.Message,
			Severity: string(e.Severity),
		})
	}
	return &security.LintReport{
		Valid:  lr.Valid,
		Count:  len(lr.Errors),
		Issues: issues,
	}
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

// applyScanToEntry writes the gate's result onto entry.Verification.Scan,
// REPLACING any existing scan block. Used by code paths (e.g. publish) where
// the entry's commit has just advanced and any prior scan attestation is
// stale — keeping the old block in place would attribute the old commit's
// findings to the new commit (issue #71).
//
// When toScanRef returns nil (gate ran for an unattested reason — config
// has scan_on_install=false), clears entry.Verification.Scan to nil so the
// stale block from a previous run doesn't leak forward. Also prunes
// entry.Verification entirely when no other signals remain so the lockfile
// stays compact.
func applyScanToEntry(entry *model.LockEntry, gate *scanGateResult) {
	if entry == nil {
		return
	}
	scan := toScanRef(gate)
	if scan == nil {
		// Nothing to record. Clear any prior block so it isn't attributed to
		// the just-advanced commit.
		if entry.Verification != nil {
			entry.Verification.Scan = nil
			if entry.Verification.IsEmpty() {
				entry.Verification = nil
			}
		}
		return
	}
	if entry.Verification == nil {
		entry.Verification = &model.VerificationRecord{}
	}
	entry.Verification.Scan = scan
}

// recordScanResult writes the gate's result into the named entry's
// verification.scan slot on the lockfile at lockPath. No-op when the gate
// produced no recordable signal (skipped, no result). Should be called
// inside the same WithLock window that performed the install — otherwise a
// concurrent qvr command could see the lock briefly without the scan
// record.
func recordScanResult(lockPath, name string, gate *scanGateResult) error {
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return fmt.Errorf("read lock for scan record: %w", err)
	}
	if err := recordScanResultInLock(lock, name, gate); err != nil {
		return err
	}
	if err := lock.Write(); err != nil {
		return fmt.Errorf("write lock for scan record: %w", err)
	}
	return nil
}

// recordScanResultInLock applies the gate's result to the named entry on an
// already-loaded, in-memory lock without persisting it — the batch add path
// records every skill's scan onto one shared lock and writes it once. Returns
// nil (no-op) when the gate produced no recordable signal or when the prior
// record already matches (the same idempotency / no-downgrade guards as the
// read-write recordScanResult). The caller owns lock.Write().
func recordScanResultInLock(lock *model.LockFile, name string, gate *scanGateResult) error {
	scan := toScanRef(gate)
	if scan == nil {
		return nil
	}
	entry, err := lock.Get(name)
	if err != nil {
		return fmt.Errorf("locate %s in lock: %w", name, err)
	}
	// Idempotency guard (issue #77): if the recorded scan already matches the
	// new one, skip the write. Also refuse to downgrade a real attestation to
	// a `--no-scan` sentinel — a no-op re-add with --no-scan should not
	// destroy the prior commit's clean-scan record.
	if entry.Verification != nil && entry.Verification.Scan != nil {
		prior := entry.Verification.Scan
		if scan.Decision == "skipped" && prior.Decision != "skipped" {
			return nil
		}
		if prior.Decision == scan.Decision &&
			prior.ReportSHA == scan.ReportSHA &&
			prior.ScannerVersion == scan.ScannerVersion &&
			prior.Reason == scan.Reason {
			return nil
		}
	}
	if entry.Verification == nil {
		entry.Verification = &model.VerificationRecord{}
	}
	entry.Verification.Scan = scan
	lock.Put(entry)
	return nil
}
