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
	Tool    SarifTool      `json:"tool"`
	Results []SarifResult  `json:"results"`
}

type SarifTool struct {
	Driver SarifDriver `json:"driver"`
}

type SarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version,omitempty"`
	InformationURI string      `json:"informationUri,omitempty"`
	Rules          []SarifRule `json:"rules,omitempty"`
}

type SarifRule struct {
	ID               string         `json:"id"`
	Name             string         `json:"name,omitempty"`
	ShortDescription SarifText      `json:"shortDescription,omitempty"`
	HelpURI          string         `json:"helpUri,omitempty"`
	Properties       map[string]any `json:"properties,omitempty"`
}

type SarifText struct {
	Text string `json:"text"`
}

type SarifResult struct {
	RuleID    string          `json:"ruleId,omitempty"`
	Level     string          `json:"level"`
	Message   SarifText       `json:"message"`
	Locations []SarifLocation `json:"locations,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

type SarifLocation struct {
	PhysicalLocation SarifPhysicalLocation `json:"physicalLocation"`
}

type SarifPhysicalLocation struct {
	ArtifactLocation SarifArtifactLocation `json:"artifactLocation"`
	Region           *SarifRegion          `json:"region,omitempty"`
}

type SarifArtifactLocation struct {
	URI string `json:"uri"`
}

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
			Properties: map[string]any{
				"check":      f.Check,
				"confidence": f.Confidence,
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
					InformationURI: "https://github.com/raks097/quiver",
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
