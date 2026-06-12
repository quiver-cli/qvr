package security

import "strings"

// SARIF 2.1.0 serialization for [ScanResult].
//
// SARIF (Static Analysis Results Interchange Format) is the OASIS
// standard most CI / IDE integrations understand. Emitting it lets
// `qvr scan --format sarif` plug directly into GitHub's code-scanning
// pipeline and most editor lint surfaces.
//
// We hand-roll a minimal subset of the schema rather than pulling in
// a SARIF SDK: the spec is large but only a slice of it actually
// affects rendering. The shape here matches what github.com/microsoft/
// sarif-tutorials calls the "minimum viable result" — driver name,
// rule list, results with locations.

const (
	sarifSchemaURI = "https://schemastore.azurewebsites.net/schemas/json/sarif-2.1.0-rtm.4.json"
	sarifVersion   = "2.1.0"
)

// SarifReport is the top-level SARIF log.
type SarifReport struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []SarifRun `json:"runs"`
}

// SarifRun is one analysis pass over the skill.
type SarifRun struct {
	Tool    SarifTool     `json:"tool"`
	Results []SarifResult `json:"results"`
}

// SarifTool wraps the driver descriptor for a run.
type SarifTool struct {
	Driver SarifDriver `json:"driver"`
}

// SarifDriver identifies the scanner that produced the run and lists
// the rules its results reference.
type SarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version,omitempty"`
	InformationURI string      `json:"informationUri,omitempty"`
	Rules          []SarifRule `json:"rules,omitempty"`
}

// SarifRule describes one rule referenced by results, deduplicated
// across findings.
type SarifRule struct {
	ID               string         `json:"id"`
	Name             string         `json:"name,omitempty"`
	ShortDescription SarifText      `json:"shortDescription"`
	HelpURI          string         `json:"helpUri,omitempty"`
	Properties       map[string]any `json:"properties,omitempty"`
}

// SarifText is SARIF's wrapper for plain message strings.
type SarifText struct {
	Text string `json:"text"`
}

// SarifResult is one finding: rule reference, level, message, and
// where it was found.
type SarifResult struct {
	RuleID     string          `json:"ruleId,omitempty"`
	Level      string          `json:"level"`
	Message    SarifText       `json:"message"`
	Locations  []SarifLocation `json:"locations,omitempty"`
	Properties map[string]any  `json:"properties,omitempty"`
}

// SarifLocation points a result at a physical location in the skill.
type SarifLocation struct {
	PhysicalLocation SarifPhysicalLocation `json:"physicalLocation"`
}

// SarifPhysicalLocation is a file (and optional line region) inside
// the scanned skill.
type SarifPhysicalLocation struct {
	ArtifactLocation SarifArtifactLocation `json:"artifactLocation"`
	Region           *SarifRegion          `json:"region,omitempty"`
}

// SarifArtifactLocation holds the skill-relative URI of the file a
// finding lives in.
type SarifArtifactLocation struct {
	URI string `json:"uri"`
}

// SarifRegion narrows a physical location to a starting line; emitted
// only when the finding carries a line number.
type SarifRegion struct {
	StartLine int `json:"startLine"`
}

// ToSARIF converts a ScanResult into a serializable SarifReport. The
// driver version is left empty here; the CLI overrides it with the
// build's version string before printing.
func ToSARIF(result *ScanResult) SarifReport {
	if result == nil {
		return SarifReport{Schema: sarifSchemaURI, Version: sarifVersion}
	}

	rules := make([]SarifRule, 0)
	seenRules := make(map[string]bool)
	results := make([]SarifResult, 0, len(result.Findings))

	for _, f := range result.Findings {
		ruleID := sarifRuleID(f)
		if !seenRules[ruleID] {
			rules = append(rules, SarifRule{
				ID:               ruleID,
				Name:             strings.ReplaceAll(string(f.Category), " ", "_"),
				ShortDescription: SarifText{Text: messageOrCheck(f)},
				Properties: map[string]any{
					"category":   string(f.Category),
					"confidence": f.Confidence,
				},
			})
			seenRules[ruleID] = true
		}
		results = append(results, SarifResult{
			RuleID:  ruleID,
			Level:   sarifLevel(f.Severity),
			Message: SarifText{Text: f.Message},
			Locations: []SarifLocation{
				{
					PhysicalLocation: SarifPhysicalLocation{
						ArtifactLocation: SarifArtifactLocation{URI: f.File},
						Region:           sarifRegion(f.Line),
					},
				},
			},
			// SARIF's `level` only has {error, warning, note, none},
			// so critical and error both collapse to "error" on the
			// wire. We carry the original qvr severity in two places
			// so the distinction survives a round-trip:
			//
			//   - properties.severity: the literal qvr severity name
			//     (matches the JSON wire shape)
			//   - properties.problem.severity: the convention GitHub
			//     code-scanning uses to colour-code rows
			//
			// Issue #41 — without this, 10 critical findings and 9
			// error findings appeared identical in the dashboard.
			Properties: map[string]any{
				"check":      f.Check,
				"category":   string(f.Category),
				"confidence": f.Confidence,
				"severity":   string(f.Severity),
				"problem": map[string]any{
					"severity": sarifProblemSeverity(f.Severity),
				},
			},
		})
	}

	return SarifReport{
		Schema:  sarifSchemaURI,
		Version: sarifVersion,
		Runs: []SarifRun{{
			Tool: SarifTool{
				Driver: SarifDriver{
					Name:           "qvr",
					InformationURI: "https://github.com/astra-sh/qvr",
					Rules:          rules,
				},
			},
			Results: results,
		}},
	}
}

func sarifRuleID(f Finding) string {
	if f.RuleID != "" {
		return f.RuleID
	}
	return f.Check
}

func messageOrCheck(f Finding) string {
	if f.Message != "" {
		return f.Message
	}
	return f.Check
}

func sarifRegion(line int) *SarifRegion {
	if line <= 0 {
		return nil
	}
	return &SarifRegion{StartLine: line}
}

// sarifLevel maps Quiver's four-step ladder to SARIF's three-step one.
// SARIF doesn't have a "critical" level; we collapse critical+error
// → error and use `level: none` for info findings (the SARIF idiom
// for "noteworthy but not a defect").
//
// The lossy collapse is recovered in result.properties.severity and
// .properties.problem.severity (issue #41).
func sarifLevel(s Severity) string {
	switch s {
	case SeverityCritical, SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	case SeverityInfo:
		return "note"
	}
	return "none"
}

// sarifProblemSeverity is the value GitHub code-scanning consumes
// to colour-code findings. It accepts {critical, high, medium,
// low, warning, recommendation, error, note}. We map qvr severity
// to the closest match so a critical credential leak surfaces as
// "critical" in the dashboard, not "error".
func sarifProblemSeverity(s Severity) string {
	switch s {
	case SeverityCritical:
		return "critical"
	case SeverityError:
		return "high"
	case SeverityWarning:
		return "medium"
	case SeverityInfo:
		return "low"
	}
	return "low"
}
