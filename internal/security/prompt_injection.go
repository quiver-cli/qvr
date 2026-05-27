package security

// PromptInjectionCheckName is the [Check.Name] of the prompt-injection
// check. The check is preserved as a distinct Check.Name() (rather than
// folded into the unified [PatternsCheckName] check) so historical
// consumers can keep filtering findings by `check == "prompt_injection"`.
const PromptInjectionCheckName = "prompt_injection"

// NewPromptInjectionCheck returns the prompt-injection / system-prompt-
// leakage check. It is a thin adapter over the unified rule engine that
// runs only the [CategoryPromptInjection] and
// [CategorySystemPromptLeakage] rule subsets.
//
// Findings come out with Check == "prompt_injection" so the existing
// JSON contract for the `qvr scan` command remains stable; the new
// taxonomy is surfaced via the per-finding Category and RuleID fields.
func NewPromptInjectionCheck() Check {
	rules := BuiltinRules().FilterByCategory(
		CategoryPromptInjection,
		CategorySystemPromptLeakage,
	)
	return NewRuleCheck(PromptInjectionCheckName, rules)
}
