package security

import (
	"regexp"
	"strings"
)

// Capability is one detectable behavior class a skill's files may
// exhibit. Capabilities are inferred conservatively from code patterns
// — false negatives are preferred over noisy false positives, since
// MCP least-privilege findings are user-facing.
type Capability string

const (
	CapShell     Capability = "shell"
	CapNetwork   Capability = "network"
	CapFileRead  Capability = "file_read"
	CapFileWrite Capability = "file_write"
	CapEnvAccess Capability = "env_access"
	CapExec      Capability = "exec"
)

// capabilityPattern pairs a Capability label with a regex that fires
// when that capability is exercised in code. Multiple patterns may map
// to the same capability — DetectCapabilities deduplicates.
type capabilityPattern struct {
	cap Capability
	re  *regexp.Regexp
}

// codeFileSuffix is the set of file extensions where capability
// inference is meaningful. Markdown / text files are excluded — we
// don't want to flag a skill for "network capability" because the
// SKILL.md mentions `curl` in prose.
var codeFileSuffix = []string{
	".py", ".js", ".ts", ".tsx", ".jsx",
	".sh", ".bash", ".zsh",
	".go", ".rb", ".php",
}

func isCodeFile(p string) bool {
	for _, s := range codeFileSuffix {
		if strings.HasSuffix(p, s) {
			return true
		}
	}
	return false
}

var capabilityPatterns = []capabilityPattern{
	{CapShell, regexp.MustCompile(`(?i)\b(?:subprocess|Popen|os\.system|os\.popen|execv\w*|spawnl\w*|exec_command|child_process\.(?:exec|spawn)|shell_exec|sh\s+-c)\b`)},
	{CapShell, regexp.MustCompile(`(?i)(?:^|\s)(?:curl|wget|bash|sh|zsh|fish|sudo)\s+\S`)},
	{CapNetwork, regexp.MustCompile(`(?i)\b(?:requests\.|httpx\.|urllib\.request|http\.client|net/http|fetch\s*\(|axios\.|XMLHttpRequest|socket\.(?:send|recv|connect)|net\.Dial)`)},
	{CapNetwork, regexp.MustCompile(`https?://[^\s'"<>]+`)},
	{CapFileRead, regexp.MustCompile(`(?i)\b(?:open\s*\(|readFile\s*\(|os\.ReadFile|ioutil\.ReadFile|Path\s*\([^)]*\)\.read_text|fs\.read|fs\.openSync)`)},
	{CapFileWrite, regexp.MustCompile(`(?i)\b(?:open\s*\([^)]*['"][wax])|writeFile|os\.WriteFile|ioutil\.WriteFile|\.write_text\b|\.write_bytes\b|fs\.write(?:File|Sync)?`)},
	{CapEnvAccess, regexp.MustCompile(`(?i)\b(?:os\.environ|os\.getenv|process\.env|os\.LookupEnv|os\.Getenv)\b`)},
	{CapExec, regexp.MustCompile(`(?i)\b(?:eval|exec|compile)\s*\(`)},
}

// DetectCapabilities walks the file set and returns the unique
// capabilities exercised by code files. Documentation files are
// skipped so a skill that talks about subprocess in prose doesn't get
// flagged as exercising shell.
//
// The returned slice is sorted for stable output, since downstream
// findings include capability lists verbatim.
func DetectCapabilities(files []FileEntry) []Capability {
	hit := make(map[Capability]bool, 6)
	for _, f := range files {
		if !isCodeFile(f.Path) || f.Content == "" {
			continue
		}
		for _, pat := range capabilityPatterns {
			if hit[pat.cap] {
				continue
			}
			if pat.re.MatchString(f.Content) {
				hit[pat.cap] = true
			}
		}
	}
	out := make([]Capability, 0, len(hit))
	for _, c := range []Capability{CapShell, CapNetwork, CapFileRead, CapFileWrite, CapEnvAccess, CapExec} {
		if hit[c] {
			out = append(out, c)
		}
	}
	return out
}

// DetectCapabilityLocations returns a map from capability to one
// representative (file, line) where it was detected. Used by the MCP
// least-privilege check to attribute findings to a concrete line of
// code rather than reporting them at file 0.
func DetectCapabilityLocations(files []FileEntry) map[Capability]CapabilitySite {
	out := make(map[Capability]CapabilitySite, 6)
	for _, f := range files {
		if !isCodeFile(f.Path) || f.Content == "" {
			continue
		}
		lines := strings.Split(f.Content, "\n")
		for lineIdx, line := range lines {
			for _, pat := range capabilityPatterns {
				if _, seen := out[pat.cap]; seen {
					continue
				}
				if pat.re.MatchString(line) {
					out[pat.cap] = CapabilitySite{File: f.Path, Line: lineIdx + 1}
				}
			}
		}
	}
	return out
}

// CapabilitySite is the first observation of a capability — used in
// finding messages so a reviewer can jump straight to the code.
type CapabilitySite struct {
	File string
	Line int
}
