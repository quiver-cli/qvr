package security

import (
	"context"
	"fmt"
	"regexp"

	"github.com/quiver-cli/qvr/internal/model"
)

// SignatureCheckName is the [Check.Name] of the signature check.
const SignatureCheckName = "signatures"

// SignatureFamily groups signatures by what they detect: malware,
// webshells, cryptominers, hacktools.
type SignatureFamily string

const (
	FamilyMalware     SignatureFamily = "malware"
	FamilyWebshell    SignatureFamily = "webshell"
	FamilyCryptominer SignatureFamily = "cryptominer"
	FamilyHackTool    SignatureFamily = "hack_tool"
)

// Signature is one compiled detection rule. Patterns run against full
// file contents (not per-line) so multi-line indicators (e.g. PHP
// backdoor preludes) still match. AllOf semantics: every regex in the
// slice must hit somewhere in the file for the signature to fire.
type Signature struct {
	ID          string
	Family      SignatureFamily
	Severity    Severity
	Confidence  float64
	Description string
	Patterns    []*regexp.Regexp
}

// builtinSignatures is the pure-Go signature set. Patterns target
// well-known malicious-code families and are chosen for high
// specificity. Each Signature has a one-line Description that surfaces
// in finding messages.
var builtinSignatures = []Signature{
	// ---- Webshells ----
	{
		ID:          "YR2_php_eval_shell",
		Family:      FamilyWebshell,
		Severity:    SeverityCritical,
		Confidence:  0.9,
		Description: "PHP webshell — eval over request input",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)<\?php`),
			regexp.MustCompile(`(?i)eval\s*\(\s*\$_(?:GET|POST|REQUEST|COOKIE)\s*\[`),
		},
	},
	{
		ID:          "YR2_php_system_passthru",
		Family:      FamilyWebshell,
		Severity:    SeverityCritical,
		Confidence:  0.9,
		Description: "PHP webshell — system/passthru/shell_exec over request input",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)<\?php`),
			regexp.MustCompile(`(?i)(?:system|passthru|shell_exec|exec)\s*\(\s*\$_(?:GET|POST|REQUEST|COOKIE)`),
		},
	},
	{
		ID:          "YR2_jsp_runtime_exec",
		Family:      FamilyWebshell,
		Severity:    SeverityCritical,
		Confidence:  0.85,
		Description: "JSP webshell — Runtime.exec over request parameter",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)Runtime\.getRuntime\(\)\.exec`),
			regexp.MustCompile(`(?i)request\.getParameter\(`),
		},
	},
	{
		ID:          "YR2_aspx_eval",
		Family:      FamilyWebshell,
		Severity:    SeverityCritical,
		Confidence:  0.85,
		Description: "ASPX webshell — Eval/Request mash-up",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)<%@\s*Page\b`),
			regexp.MustCompile(`(?i)(?:Eval|Execute)\s*\(\s*Request\b`),
		},
	},
	{
		ID:          "YR2_python_request_exec",
		Family:      FamilyWebshell,
		Severity:    SeverityCritical,
		Confidence:  0.85,
		Description: "Python webshell — exec/eval over Flask/FastAPI request",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)from\s+flask\s+import|fastapi`),
			regexp.MustCompile(`(?i)(?:exec|eval)\s*\(\s*request\.(?:args|form|json|values)`),
		},
	},

	// ---- Reverse shells / backdoors / malware ----
	{
		ID:          "YR1_python_reverse_shell",
		Family:      FamilyMalware,
		Severity:    SeverityCritical,
		Confidence:  0.95,
		Description: "Python reverse shell — socket + dup2 + /bin/sh",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)import\s+socket`),
			regexp.MustCompile(`(?i)(?:dup2|fdopen)\s*\(`),
			regexp.MustCompile(`(?i)/bin/(?:ba)?sh`),
		},
	},
	{
		ID:          "YR1_bash_reverse_shell",
		Family:      FamilyMalware,
		Severity:    SeverityCritical,
		Confidence:  0.95,
		Description: "Bash reverse shell — /dev/tcp redirect",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)/dev/tcp/\S+/\d+`),
			regexp.MustCompile(`(?i)0>&1|>&\s*\d`),
		},
	},
	{
		ID:          "YR1_nc_reverse_shell",
		Family:      FamilyMalware,
		Severity:    SeverityCritical,
		Confidence:  0.9,
		Description: "Netcat reverse shell",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\bn(?:et)?c(?:at)?\s+-[a-z]*e[a-z]*\s+/bin/(?:ba)?sh`),
		},
	},
	{
		ID:          "YR1_powershell_downloader",
		Family:      FamilyMalware,
		Severity:    SeverityCritical,
		Confidence:  0.9,
		Description: "PowerShell downloader cradle",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)(?:IEX|Invoke-Expression)\s*\(`),
			regexp.MustCompile(`(?i)(?:New-Object\s+(?:Net\.)?WebClient|DownloadString|DownloadFile)`),
		},
	},
	{
		ID:          "YR1_node_eval_fetch",
		Family:      FamilyMalware,
		Severity:    SeverityError,
		Confidence:  0.85,
		Description: "Node.js loader — eval(remote fetch)",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)require\(\s*['"]https?\b`),
			regexp.MustCompile(`(?i)\beval\s*\(`),
		},
	},

	// ---- Cryptominers ----
	{
		ID:          "YR3_stratum_protocol",
		Family:      FamilyCryptominer,
		Severity:    SeverityError,
		Confidence:  0.85,
		Description: "Cryptocurrency mining — stratum+tcp protocol reference",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)stratum\+(?:tcp|ssl)://`),
		},
	},
	{
		ID:          "YR3_xmrig",
		Family:      FamilyCryptominer,
		Severity:    SeverityError,
		Confidence:  0.9,
		Description: "Cryptocurrency mining — XMRig invocation",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\bxmrig\b`),
			regexp.MustCompile(`(?i)(?:--donate-level|--cpu-priority|cryptonight)`),
		},
	},
	{
		ID:          "YR3_browser_miner",
		Family:      FamilyCryptominer,
		Severity:    SeverityError,
		Confidence:  0.8,
		Description: "Cryptocurrency mining — known browser miner library",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)(?:coinhive|jsecoin|cryptoloot|webminerpool|coin-?have)\.com`),
		},
	},

	// ---- Hack tools / offensive ----
	{
		ID:          "YR4_mimikatz",
		Family:      FamilyHackTool,
		Severity:    SeverityError,
		Confidence:  0.95,
		Description: "Offensive tooling — Mimikatz reference",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\bmimikatz\b`),
		},
	},
	{
		ID:          "YR4_meterpreter",
		Family:      FamilyHackTool,
		Severity:    SeverityError,
		Confidence:  0.9,
		Description: "Offensive tooling — Meterpreter payload reference",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\bmeterpreter\b|metsrv\.dll|msfvenom\s`),
		},
	},
	{
		ID:          "YR4_cobalt_strike",
		Family:      FamilyHackTool,
		Severity:    SeverityError,
		Confidence:  0.9,
		Description: "Offensive tooling — Cobalt Strike beacon string",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)cobalt[\s_-]?strike|beacon\.dll|teamserver`),
		},
	},
	{
		ID:          "YR4_sqlmap",
		Family:      FamilyHackTool,
		Severity:    SeverityWarning,
		Confidence:  0.75,
		Description: "Offensive tooling — sqlmap invocation",
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\bsqlmap\b\s+(?:-u|--url|--data)`),
		},
	},
}

type signatureCheck struct {
	sigs []Signature
}

// NewSignatureCheck returns the built-in YARA-lite signature engine.
// Custom signatures may be registered via [SignatureCheck.Register]; the
// default constructor returns the canonical set.
func NewSignatureCheck() Check {
	return &signatureCheck{sigs: builtinSignatures}
}

func (*signatureCheck) Name() string { return SignatureCheckName }

func (c *signatureCheck) Run(_ context.Context, _ *model.Skill, files []FileEntry) []Finding {
	var findings []Finding
	for _, f := range files {
		if f.Content == "" {
			continue
		}
		for _, sig := range c.sigs {
			line, ok := matchSignature(sig, f.Content)
			if !ok {
				continue
			}
			findings = append(findings, Finding{
				Check:       SignatureCheckName,
				RuleID:      sig.ID,
				Category:    CategoryYARAMatch,
				Severity:    sig.Severity,
				Confidence:  sig.Confidence,
				File:        f.Path,
				Line:        line,
				Message:     fmt.Sprintf("%s [%s]: %s", sig.ID, sig.Family, sig.Description),
				Remediation: remediationForFamily(sig.Family),
			})
		}
	}
	return findings
}

// matchSignature reports whether every pattern in sig hits in content,
// and returns the 1-indexed line of the first pattern's match for
// attribution.
func matchSignature(sig Signature, content string) (int, bool) {
	if len(sig.Patterns) == 0 {
		return 0, false
	}
	firstLine := 0
	for i, re := range sig.Patterns {
		loc := re.FindStringIndex(content)
		if loc == nil {
			return 0, false
		}
		if i == 0 {
			firstLine = lineNumberFor(content, loc[0])
		}
	}
	return firstLine, true
}

func remediationForFamily(f SignatureFamily) string {
	switch f {
	case FamilyWebshell:
		return "remove the webshell file; webshells provide unauthorised remote command execution"
	case FamilyMalware:
		return "remove the suspected malware code; audit the entire skill for additional persistence mechanisms"
	case FamilyCryptominer:
		return "remove cryptocurrency mining code; mining inside a skill is unauthorised resource abuse"
	case FamilyHackTool:
		return "remove offensive-security tool references; legitimate skills should not embed exploit frameworks"
	}
	return "audit the matched signature and remove if not justified"
}
