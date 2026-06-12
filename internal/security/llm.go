// Package-level seam for LLM-driven semantic checks.
//
// The deterministic Check pipeline is the trust anchor — every finding
// from it is reproducible from the same input. Some classes of risk,
// though, only resolve with a model: description-vs-behavior mismatch
// (does the code do what the SKILL.md claims?), security-discovery
// (are there risks the static rules missed?), and quality-policy
// (does the skill conform to an org policy expressed in prose?).
//
// This file defines the integration shape for those checks. The
// concrete LLM transport is intentionally not in this repo: it would
// pull in provider SDKs that aren't needed for the static path, and
// the deterministic scan must keep working offline. A future package
// (`internal/security/llmproviders/<name>`) plugs in via
// [LLMProvider].

package security

import (
	"context"
	"os"

	"github.com/astra-sh/qvr/internal/model"
)

// LLMProvider is the transport layer for an LLM. Implementations
// return raw model output for a structured prompt; the calling
// [LLMCheck] is responsible for parsing that output into [Finding]s.
//
// Methods are kept minimal so a swap from one provider to another
// touches only this interface.
//
// Implementations must be safe for concurrent use.
type LLMProvider interface {
	// Name identifies the provider in scan output (so a user can tell
	// which model produced a finding). Example: "provider/model-name".
	Name() string

	// Complete sends prompt to the model and returns the raw response.
	// The caller is responsible for retry / rate-limit semantics.
	Complete(ctx context.Context, prompt string) (string, error)
}

// LLMCheck is the semantic counterpart to [Check]. Run receives an
// LLMProvider; if it is nil, the check should no-op and return nil so
// `qvr scan` stays usable on hosts with no LLM configured.
//
// The shape mirrors [Check] exactly except for the extra provider
// parameter — that keeps wiring trivial and lets the unified scanner
// pipeline treat semantic checks as plug-ins.
type LLMCheck interface {
	Name() string
	Run(ctx context.Context, provider LLMProvider, skill *model.Skill, files []FileEntry) []Finding
}

// LLMCategory tags semantic findings so reports can separate them
// from deterministic ones at a glance.
const LLMCategory Category = "semantic"

// EnvLLMProvider is the name of the env var the CLI inspects to pick
// an LLM. When unset, semantic checks no-op.
const EnvLLMProvider = "QVR_LLM_PROVIDER"

// LLMProviderFromEnv returns the provider named by the environment,
// or nil if no provider is configured. The actual resolution
// (provider-specific keys, models) is deferred to the provider
// package — this function exists so deterministic code can probe for
// "is the LLM seam available?" without importing transport code.
//
// The default impl is nil-returning. Provider implementations
// register themselves via [RegisterLLMProvider].
var LLMProviderFromEnv = func() LLMProvider {
	name := os.Getenv(EnvLLMProvider)
	if name == "" {
		return nil
	}
	if f, ok := llmRegistry[name]; ok {
		return f()
	}
	return nil
}

// llmRegistry holds named provider constructors. Provider packages
// (added separately) call [RegisterLLMProvider] from their init().
var llmRegistry = map[string]func() LLMProvider{}

// RegisterLLMProvider lets a provider package wire itself in without
// importing it from the main scanner package. Provider names are
// case-sensitive and matched verbatim against QVR_LLM_PROVIDER.
func RegisterLLMProvider(name string, ctor func() LLMProvider) {
	llmRegistry[name] = ctor
}

// ---- Built-in semantic check stubs ----
//
// Each stub names a semantic-analysis slot covered by an LLM analyzer.
// They emit no findings without a provider; with a provider, the
// check formats the prompt, calls Complete, and parses the response
// into Finding objects. Concrete prompt templates live in a follow-up
// PR alongside the first provider impl.

// SemanticSecurityDiscoveryCheck poses "what risks did the static
// rules miss?" to the LLM.
type SemanticSecurityDiscoveryCheck struct{}

// Name returns the check identifier used in scan output.
func (SemanticSecurityDiscoveryCheck) Name() string { return "semantic_security_discovery" }

// Run is a stub pending the first provider implementation; it emits no
// findings.
func (SemanticSecurityDiscoveryCheck) Run(_ context.Context, _ LLMProvider, _ *model.Skill, _ []FileEntry) []Finding {
	// Stub — see package doc. A concrete implementation will:
	//  1. Render skill body + frontmatter into a prompt.
	//  2. Ask the model for risk categories from the detection
	//     taxonomy with one-line evidence pointers.
	//  3. Parse response into Finding{Check: Name(), Category: LLMCategory}.
	return nil
}

// SemanticDeveloperIntentCheck asks the LLM whether the skill's
// stated intent matches its implementation.
type SemanticDeveloperIntentCheck struct{}

// Name returns the check identifier used in scan output.
func (SemanticDeveloperIntentCheck) Name() string { return "semantic_developer_intent" }

// Run is a stub pending the first provider implementation; it emits no
// findings.
func (SemanticDeveloperIntentCheck) Run(_ context.Context, _ LLMProvider, _ *model.Skill, _ []FileEntry) []Finding {
	return nil
}

// SemanticQualityPolicyCheck evaluates the skill against an
// org-supplied policy. Policy text is loaded from QVR_LLM_POLICY (file
// path) in the concrete impl.
type SemanticQualityPolicyCheck struct{}

// Name returns the check identifier used in scan output.
func (SemanticQualityPolicyCheck) Name() string { return "semantic_quality_policy" }

// Run is a stub pending the first provider implementation; it emits no
// findings.
func (SemanticQualityPolicyCheck) Run(_ context.Context, _ LLMProvider, _ *model.Skill, _ []FileEntry) []Finding {
	return nil
}

// DescriptionBehaviorMismatchCheck is the TP4 slot from the MCP
// tool-poisoning taxonomy. It is LLM-only because "does the description
// match the code?" requires semantic reasoning that a regex can't
// approximate.
type DescriptionBehaviorMismatchCheck struct{}

// Name returns the check identifier used in scan output.
func (DescriptionBehaviorMismatchCheck) Name() string { return "tp4_description_behavior_mismatch" }

// Run is a stub pending the first provider implementation; it emits no
// findings.
func (DescriptionBehaviorMismatchCheck) Run(_ context.Context, _ LLMProvider, _ *model.Skill, _ []FileEntry) []Finding {
	return nil
}

// BuiltinLLMChecks returns the full set of semantic checks defined in
// this seam. Callers wiring up the LLM path should pass these through
// their pipeline alongside the deterministic Check set.
func BuiltinLLMChecks() []LLMCheck {
	return []LLMCheck{
		SemanticSecurityDiscoveryCheck{},
		SemanticDeveloperIntentCheck{},
		SemanticQualityPolicyCheck{},
		DescriptionBehaviorMismatchCheck{},
	}
}
