package security

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/quiver-cli/qvr/internal/model"
)

// DependencyCheckName is the [Check.Name] of the dependency check.
const DependencyCheckName = "dependencies"

// Ecosystem labels which package registry a dependency lives in. The
// labels mirror the values osv.dev uses, so a future online lookup can
// pass them straight through.
type Ecosystem string

const (
	EcosystemPyPI Ecosystem = "PyPI"
	EcosystemNPM  Ecosystem = "npm"
	EcosystemGo   Ecosystem = "Go"
)

// Dependency is a single (ecosystem, name, version) triple extracted
// from a manifest. Version is empty when the manifest pins by range or
// not at all — the check uses that to flag SC1.
type Dependency struct {
	Ecosystem Ecosystem
	Name      string
	Version   string
	File      string
	Line      int
	// Pinned reports whether Version is an exact pin (==, "1.2.3", or
	// a full semver). Range specifiers and "*"/"latest" are not pinned.
	Pinned bool
}

// VulnRecord is one entry in the offline vulnerability DB. The shape
// approximates an OSV record so the field names are stable when we
// later swap in a real osv.dev client.
type VulnRecord struct {
	Ecosystem   Ecosystem
	Name        string
	MaxAffected string // any version <= this is considered vulnerable
	ID          string // CVE / GHSA / OSV ID
	Summary     string
	Severity    Severity
}

// offlineVulnDB is the minimal known-bad list shipped with Quiver.
// Real CVE coverage requires the online OSV.dev client (planned); this
// list exists so the check fires deterministically in air-gapped
// environments and in tests. Each entry mirrors a real advisory.
var offlineVulnDB = []VulnRecord{
	{EcosystemPyPI, "pycrypto", "2.6.1", "CVE-2013-7459", "pycrypto is unmaintained — use pycryptodome", SeverityError},
	{EcosystemPyPI, "pyyaml", "5.3.1", "CVE-2020-14343", "pyyaml < 5.4 allows arbitrary code execution via yaml.load", SeverityCritical},
	{EcosystemPyPI, "urllib3", "1.26.4", "CVE-2021-33503", "urllib3 < 1.26.5 has a ReDoS in idna decoding", SeverityError},
	{EcosystemPyPI, "pillow", "8.3.1", "CVE-2021-25287", "Pillow < 8.3.2 has out-of-bounds read in J2K", SeverityError},
	{EcosystemPyPI, "requests", "2.19.0", "CVE-2018-18074", "requests < 2.20 leaks Authorization on redirect", SeverityError},
	{EcosystemPyPI, "django", "3.2.4", "CVE-2021-33203", "Django path traversal in admin static handler", SeverityError},
	{EcosystemNPM, "event-stream", "3.3.6", "GHSA-mh6f-8j2x-4483", "event-stream@3.3.6 was a malicious release", SeverityCritical},
	{EcosystemNPM, "ua-parser-js", "0.7.29", "GHSA-pjwm-rvh2-c87w", "ua-parser-js 0.7.29-0.7.31 shipped a cryptominer", SeverityCritical},
	{EcosystemNPM, "colors", "1.4.0", "GHSA-x6c4-q22v-7r92", "colors 1.4.1+ shipped sabotage (protestware)", SeverityError},
	{EcosystemNPM, "lodash", "4.17.20", "CVE-2021-23337", "lodash < 4.17.21 has a command-injection in template", SeverityError},
	{EcosystemNPM, "minimist", "1.2.5", "CVE-2021-44906", "minimist < 1.2.6 allows prototype pollution", SeverityError},
}

// abandonedPackages is the set of packages with no recent maintenance.
// Conservative — only well-known cases ship here so the check stays
// useful without becoming a nag.
var abandonedPackages = map[string]bool{
	"PyPI:pycrypto":   true,
	"PyPI:nose":       true,
	"PyPI:distribute": true,
	"PyPI:mimetools":  true,
	"npm:request":     true,
}

// popularPackages drives the typosquatting check. We compare each
// declared package name against this list and flag any that fall
// within edit distance 1 (and are not the exact name).
var popularPackages = map[Ecosystem][]string{
	EcosystemPyPI: {"requests", "numpy", "pandas", "flask", "django", "pytest", "pyyaml", "urllib3", "click", "boto3", "cryptography", "fastapi"},
	EcosystemNPM:  {"react", "lodash", "express", "axios", "chalk", "moment", "vue", "webpack", "typescript", "jest", "nodemon"},
}

type dependencyCheck struct{}

// NewDependencyCheck returns the dependency vulnerability check. It
// parses common manifest files (requirements.txt, pyproject.toml,
// package.json, go.mod) and flags:
//
//   - SC1: unpinned versions
//   - SC4: known-vulnerable versions (offline DB; OSV.dev online via
//     a future provider injection)
//   - SC5: abandoned packages
//   - SC6: typosquatting candidates (edit distance 1 to a popular pkg)
//
// Network access is not used in the default constructor — keep the
// check deterministic in CI and tests. A future OSV-backed
// implementation lives alongside in osv_provider.go.
func NewDependencyCheck() Check { return dependencyCheck{} }

func (dependencyCheck) Name() string { return DependencyCheckName }

func (dependencyCheck) Run(_ context.Context, _ *model.Skill, files []FileEntry) []Finding {
	var findings []Finding
	for _, f := range files {
		if f.Content == "" {
			continue
		}
		deps := parseManifest(f)
		for _, dep := range deps {
			findings = append(findings, evaluateDependency(dep)...)
		}
	}
	return findings
}

func evaluateDependency(d Dependency) []Finding {
	var out []Finding

	if !d.Pinned {
		out = append(out, Finding{
			Check:       DependencyCheckName,
			RuleID:      "SC1",
			Category:    CategorySupplyChain,
			Severity:    SeverityWarning,
			Confidence:  0.7,
			File:        d.File,
			Line:        d.Line,
			Message:     fmt.Sprintf("SC1: %s/%s is not pinned to an exact version", d.Ecosystem, d.Name),
			Remediation: "pin the dependency to an exact version (e.g. `==1.2.3` or a semver match in package.json/go.mod)",
		})
	}

	key := fmt.Sprintf("%s:%s", d.Ecosystem, d.Name)
	if abandonedPackages[key] {
		out = append(out, Finding{
			Check:       DependencyCheckName,
			RuleID:      "SC5",
			Category:    CategorySupplyChain,
			Severity:    SeverityWarning,
			Confidence:  0.8,
			File:        d.File,
			Line:        d.Line,
			Message:     fmt.Sprintf("SC5: %s/%s appears unmaintained", d.Ecosystem, d.Name),
			Remediation: "replace with a maintained alternative; abandoned packages no longer receive security fixes",
		})
	}

	for _, vuln := range offlineVulnDB {
		if vuln.Ecosystem != d.Ecosystem || vuln.Name != d.Name {
			continue
		}
		switch {
		case d.Version != "" && versionLE(d.Version, vuln.MaxAffected):
			out = append(out, Finding{
				Check:       DependencyCheckName,
				RuleID:      "SC4",
				Category:    CategorySupplyChain,
				Severity:    vuln.Severity,
				Confidence:  0.9,
				File:        d.File,
				Line:        d.Line,
				Message:     fmt.Sprintf("SC4: %s/%s@%s is affected by %s — %s", d.Ecosystem, d.Name, dispVersion(d.Version), vuln.ID, vuln.Summary),
				Remediation: fmt.Sprintf("upgrade %s to a release above %s", d.Name, vuln.MaxAffected),
			})
		case d.Version == "":
			out = append(out, Finding{
				Check:       DependencyCheckName,
				RuleID:      "SC4",
				Category:    CategorySupplyChain,
				Severity:    vuln.Severity,
				Confidence:  0.5,
				File:        d.File,
				Line:        d.Line,
				Message:     fmt.Sprintf("SC4: %s/%s@%s may be affected by %s — versions <= %s are vulnerable; resolved version cannot be determined without a pin — %s", d.Ecosystem, d.Name, dispVersion(d.Version), vuln.ID, vuln.MaxAffected, vuln.Summary),
				Remediation: fmt.Sprintf("pin %s to a release above %s", d.Name, vuln.MaxAffected),
			})
		}
	}

	if neighbour, ok := nearestPopular(d.Ecosystem, d.Name); ok {
		out = append(out, Finding{
			Check:       DependencyCheckName,
			RuleID:      "SC6",
			Category:    CategorySupplyChain,
			Severity:    SeverityError,
			Confidence:  0.8,
			File:        d.File,
			Line:        d.Line,
			Message:     fmt.Sprintf("SC6: %s/%s differs from popular package %q by one edit — possible typosquat", d.Ecosystem, d.Name, neighbour),
			Remediation: fmt.Sprintf("confirm the package name is intentional; if you meant %q, fix the spelling", neighbour),
		})
	}

	return out
}

func dispVersion(v string) string {
	if v == "" {
		return "<unpinned>"
	}
	return v
}

// nearestPopular reports whether name is exactly one Levenshtein edit
// away from a popular package in the same ecosystem (and not the
// popular package itself). Returns the popular neighbour when so.
func nearestPopular(eco Ecosystem, name string) (string, bool) {
	for _, p := range popularPackages[eco] {
		if p == name {
			return "", false
		}
		if editDistance(p, name) == 1 {
			return p, true
		}
	}
	return "", false
}

// editDistance is a basic iterative Levenshtein. Inputs are short
// package names; allocation overhead is fine.
func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// versionLE reports whether a <= b under SemVer precedence. The core
// dotted segments compare numerically (non-numeric segments fall back
// to lexicographic). When cores match, a version with no pre-release
// tag outranks one with a pre-release; otherwise per-identifier rules
// from SemVer §11 apply. Build metadata (after `+`) is ignored.
func versionLE(a, b string) bool {
	coreA, preA := versionParts(a)
	coreB, preB := versionParts(b)
	if c := compareCore(coreA, coreB); c != 0 {
		return c < 0
	}
	return comparePreRelease(preA, preB) <= 0
}

// versionParts splits s into its core dotted segments and its
// pre-release suffix (the substring after the first `-`, with build
// metadata after `+` discarded per SemVer).
func versionParts(s string) ([]string, string) {
	s = strings.SplitN(s, "+", 2)[0]
	parts := strings.SplitN(s, "-", 2)
	pre := ""
	if len(parts) == 2 {
		pre = parts[1]
	}
	return strings.Split(parts[0], "."), pre
}

func compareCore(a, b []string) int {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var ai, bi string
		if i < len(a) {
			ai = a[i]
		}
		if i < len(b) {
			bi = b[i]
		}
		na, ok1 := atoiSafe(ai)
		nb, ok2 := atoiSafe(bi)
		if ok1 && ok2 {
			if na == nb {
				continue
			}
			if na < nb {
				return -1
			}
			return 1
		}
		if ai == bi {
			continue
		}
		if ai < bi {
			return -1
		}
		return 1
	}
	return 0
}

// comparePreRelease compares two pre-release strings per SemVer §11.
// An empty pre-release (i.e. a "normal" version) outranks any
// non-empty pre-release.
func comparePreRelease(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	ai := strings.Split(a, ".")
	bi := strings.Split(b, ".")
	n := len(ai)
	if len(bi) < n {
		n = len(bi)
	}
	for i := 0; i < n; i++ {
		na, okA := atoiSafe(ai[i])
		nb, okB := atoiSafe(bi[i])
		switch {
		case okA && okB:
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
		case okA:
			return -1
		case okB:
			return 1
		default:
			if ai[i] != bi[i] {
				if ai[i] < bi[i] {
					return -1
				}
				return 1
			}
		}
	}
	if len(ai) == len(bi) {
		return 0
	}
	if len(ai) < len(bi) {
		return -1
	}
	return 1
}

func atoiSafe(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// ---- Manifest parsers ----

var (
	reqLineRE       = regexp.MustCompile(`^([A-Za-z0-9_.\-]+)\s*([<>!=~]=?|===)?\s*(.*?)\s*(?:#.*)?$`)
	npmDepRE        = regexp.MustCompile(`"([^"]+)"\s*:\s*"([^"]+)"`)
	goRequireLineRE = regexp.MustCompile(`^\s*([a-z0-9.\-/]+)\s+v?([0-9][\w.\-]*)`)
	pyprojectDepRE  = regexp.MustCompile(`^\s*"?([A-Za-z0-9_.\-]+)"?\s*([<>!=~]=?|===)?\s*(\d[\w.\-]*)?`)
)

func parseManifest(f FileEntry) []Dependency {
	base := path.Base(f.Path)
	switch {
	case base == "requirements.txt" || strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt"):
		return parseRequirementsTxt(f)
	case base == "package.json":
		return parsePackageJSON(f)
	case base == "go.mod":
		return parseGoMod(f)
	case base == "pyproject.toml":
		return parsePyprojectToml(f)
	}
	return nil
}

func parseRequirementsTxt(f FileEntry) []Dependency {
	var out []Dependency
	for lineIdx, raw := range strings.Split(f.Content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		m := reqLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		dep := Dependency{
			Ecosystem: EcosystemPyPI,
			Name:      m[1],
			Version:   strings.TrimSpace(m[3]),
			File:      f.Path,
			Line:      lineIdx + 1,
			Pinned:    m[2] == "==" || m[2] == "===",
		}
		if dep.Version == "*" || dep.Version == "" {
			dep.Pinned = false
		}
		out = append(out, dep)
	}
	return out
}

func parsePackageJSON(f FileEntry) []Dependency {
	var out []Dependency
	// We deliberately don't reach for encoding/json — package.json
	// values are tiny and we need line attribution. The simple regex
	// captures the canonical `"name": "version"` form.
	inDeps := false
	for lineIdx, raw := range strings.Split(f.Content, "\n") {
		trimmed := strings.TrimSpace(raw)
		if strings.Contains(trimmed, `"dependencies"`) || strings.Contains(trimmed, `"devDependencies"`) {
			inDeps = true
			continue
		}
		if strings.HasPrefix(trimmed, "}") {
			inDeps = false
			continue
		}
		if !inDeps {
			continue
		}
		m := npmDepRE.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		name := m[1]
		ver := m[2]
		pinned := isExactNPMVersion(ver)
		out = append(out, Dependency{
			Ecosystem: EcosystemNPM,
			Name:      name,
			Version:   strings.TrimPrefix(strings.TrimPrefix(ver, "^"), "~"),
			File:      f.Path,
			Line:      lineIdx + 1,
			Pinned:    pinned,
		})
	}
	return out
}

func isExactNPMVersion(v string) bool {
	if v == "" || v == "*" || strings.EqualFold(v, "latest") {
		return false
	}
	if strings.HasPrefix(v, "^") || strings.HasPrefix(v, "~") || strings.HasPrefix(v, ">") || strings.HasPrefix(v, "<") {
		return false
	}
	// Plain semver: digits.digits.digits, no operators.
	for _, r := range v {
		if !isPlainSemverRune(r) {
			return false
		}
	}
	return strings.Count(v, ".") >= 1
}

// isPlainSemverRune reports whether r is allowed in a plain semver string
// (digits, dots, dashes, ASCII letters for pre-release identifiers).
func isPlainSemverRune(r rune) bool {
	if r >= '0' && r <= '9' {
		return true
	}
	if r >= 'a' && r <= 'z' {
		return true
	}
	if r >= 'A' && r <= 'Z' {
		return true
	}
	return r == '.' || r == '-'
}

func parseGoMod(f FileEntry) []Dependency {
	var out []Dependency
	inBlock := false
	lines := strings.Split(f.Content, "\n")
	for lineIdx, raw := range lines {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "//") || line == "" {
			continue
		}
		if strings.HasPrefix(line, "require (") {
			inBlock = true
			continue
		}
		if inBlock && line == ")" {
			inBlock = false
			continue
		}
		var candidate string
		switch {
		case inBlock:
			candidate = line
		case strings.HasPrefix(line, "require "):
			candidate = strings.TrimPrefix(line, "require ")
		default:
			continue
		}
		m := goRequireLineRE.FindStringSubmatch(candidate)
		if m == nil {
			continue
		}
		out = append(out, Dependency{
			Ecosystem: EcosystemGo,
			Name:      m[1],
			Version:   m[2],
			File:      f.Path,
			Line:      lineIdx + 1,
			Pinned:    true, // go.mod versions are always exact pins
		})
	}
	return out
}

func parsePyprojectToml(f FileEntry) []Dependency {
	var out []Dependency
	inDeps := false
	for lineIdx, raw := range strings.Split(f.Content, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") {
			inDeps = strings.Contains(line, "dependencies") || strings.Contains(line, "tool.poetry")
			continue
		}
		if !inDeps || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := pyprojectDepRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		out = append(out, Dependency{
			Ecosystem: EcosystemPyPI,
			Name:      m[1],
			Version:   m[3],
			File:      f.Path,
			Line:      lineIdx + 1,
			Pinned:    m[2] == "==" || m[2] == "===" || (m[2] == "" && m[3] != ""),
		})
	}
	return out
}
