package security

// BuiltinRules returns the deterministic detection rule set, mirroring
// SkillSpector's static_patterns_* analyzers in a single registry. The
// IDs follow SkillSpector's taxonomy so cross-tool reporting stays
// readable.
//
// Conventions:
//   - all multi-word phrases use `(?i)` for case-insensitivity
//   - regexes are RE2 (no lookarounds) — adapted from PCRE where needed
//   - severity reflects deployed risk; confidence is exposed on findings
//     for downstream weighting but does not feed --severity / --fail-on
//   - SkipPattern lets a rule stay quiet on doc-about-the-pattern lines
//   - Globs scopes a rule to filetypes when over-firing on prose is a
//     known false-positive risk
//
// The set is intentionally explicit (one Rule per ID) rather than
// indirected through tables so the rule definition diffs cleanly in
// review and a reviewer can grep for an ID and find its full shape.
func BuiltinRules() RuleSet {
	rules := make(RuleSet, 0, 128)
	rules = append(rules, promptInjectionRules...)
	rules = append(rules, systemPromptLeakageRules...)
	rules = append(rules, dataExfiltrationRules...)
	rules = append(rules, privilegeEscalationRules...)
	rules = append(rules, supplyChainPatternRules...)
	rules = append(rules, excessiveAgencyRules...)
	rules = append(rules, outputHandlingRules...)
	rules = append(rules, memoryPoisoningRules...)
	rules = append(rules, toolMisuseRules...)
	rules = append(rules, rogueAgentRules...)
	rules = append(rules, harmfulContentRules...)
	rules = append(rules, triggerAbuseRules...)
	return rules
}

// codeFileGlobs scopes a rule to source files where the regex would
// genuinely indicate a behavior, not just docs talking about it.
var codeFileGlobs = []string{
	"**/*.py", "**/*.js", "**/*.ts", "**/*.tsx", "**/*.jsx",
	"**/*.sh", "**/*.bash", "**/*.zsh",
	"**/*.go", "**/*.rb", "**/*.php",
	"**/*.yaml", "**/*.yml", "**/*.toml", "**/*.json",
}

// Pattern category: Prompt Injection (P-series).
//
// SkillSpector ships these as static_patterns_prompt_injection. Quiver
// already had a smaller prompt_injection check covering similar
// ground; the patterns here are additive and graded across the same
// taxonomy so findings can be filtered uniformly.
var promptInjectionRules = RuleSet{
	{
		ID: "P1", Category: CategoryPromptInjection, Severity: SeverityWarning, Confidence: 0.8,
		Pattern:     `(?i)(?:ignore|disregard|forget)\s+(?:(?:the|all|previous|prior|above|earlier|any)\s+)+(?:instructions?|prompts?|rules)`,
		SkipPattern: `(?i)(?:must|should|cannot|do\s+not|won't|never)\s+(?:ignore|disregard|forget)`,
		Hint:        "instruction-override phrasing",
		Remediation: "rewrite or remove text that asks the agent to ignore prior instructions",
	},
	{
		ID: "P2", Category: CategoryPromptInjection, Severity: SeverityWarning, Confidence: 0.7,
		Pattern:     `<!--[^>]*?(?i:system|instructions?|ignore|exfiltrate|send|transmit)[^>]*?-->`,
		Hint:        "hidden HTML-comment instruction",
		Remediation: "delete hidden comments embedded in skill text",
	},
	{
		ID: "P3", Category: CategoryPromptInjection, Severity: SeverityError, Confidence: 0.85,
		Pattern:     `(?i)(?:silently|quietly|secretly|covertly)\s+(?:send|transmit|log|record|forward)`,
		Hint:        "covert exfiltration instruction",
		Remediation: "remove instructions that conceal data transmission from the user",
	},
	{
		ID: "P4", Category: CategoryPromptInjection, Severity: SeverityWarning, Confidence: 0.7,
		Pattern:     `(?i)(?:subtly|gradually|slowly)\s+(?:steer|guide|push|nudge)\s+(?:the\s+)?(?:user|conversation)`,
		Hint:        "subtle behavior-steering phrasing",
		Remediation: "remove implicit-bias / steering instructions",
	},
	{
		// P9 covers chat-template delimiters across the major model families:
		//   - ChatML (`<|im_start|>`, `<|im_end|>`, `<|system|>`) — GPT, Qwen
		//   - Llama-2/3 chat (`[INST]`, `[/INST]`, `<<SYS>>`, `<</SYS>>`)
		//   - Mistral / Mixtral (same `[INST]` family, plus `<s>` / `</s>`
		//     sentence anchors that double as chat boundaries)
		// Issue #39: an injection that smuggles `[INST]` into a SKILL.md
		// becomes a hard boundary when the consuming model re-templatises
		// the skill, so flagging ChatML but not Llama-chat would leave the
		// second-largest open-weights family unguarded.
		ID: "P9", Category: CategoryPromptInjection, Severity: SeverityWarning, Confidence: 0.8,
		Pattern:     `(?i)(?:system\s+prompt\s*:|developer\s+message\s*:|hidden\s+instruction\s*:)|<\|(?:im_start|im_end|system|user|assistant)\|>|\[/?INST\]|<<SYS>>|<</SYS>>|</?s>`,
		Hint:        "forged conversation boundary or chat-template token",
		Remediation: "remove tokens that fake conversation roles or chat-template delimiters (ChatML, Llama-2/3, Mistral, Mixtral)",
	},
	{
		ID: "P10", Category: CategoryPromptInjection, Severity: SeverityWarning, Confidence: 0.7,
		Pattern:     `(?i)\b(?:DAN\s+mode|developer\s+mode\s+enabled|no\s+restrictions\s+apply|do\s+anything\s+now|jailbreak\s+mode)\b`,
		Hint:        "known jailbreak phrase",
		Remediation: "remove jailbreak boilerplate from skill content",
	},
	{
		ID: "P11", Category: CategoryPromptInjection, Severity: SeverityWarning, Confidence: 0.7,
		Pattern:     `(?i)(?:you\s+are\s+now|act\s+as|pretend\s+to\s+be|from\s+now\s+on\s+you(?:'re|\s+are))\b`,
		Hint:        "role-reassignment phrasing",
		Remediation: "remove text that asks the agent to assume a different role or persona",
	},
	{
		ID: "P12", Category: CategoryPromptInjection, Severity: SeverityWarning, Confidence: 0.7,
		Pattern:     `(?i)(?:always\s+run|execute\s+(?:this|the\s+following)\s+command|on\s+every\s+(?:request|turn|message))`,
		Hint:        "tool-invocation coercion",
		Remediation: "do not instruct the agent to invoke tools on every turn unconditionally",
	},
	{
		ID: "P13", Category: CategoryPromptInjection, Severity: SeverityWarning, Confidence: 0.7,
		Pattern:     `(?i)(?:curl\s+|wget\s+|fetch\s*\(|POST\s+to)\s*['"]?https?://[^\s'"]+\?[^\s'"]+`,
		Hint:        "outbound HTTP call with query-string payload",
		Remediation: "confirm the destination is intended; query-string payloads are a common exfiltration shape",
	},
}

// Pattern category: System Prompt Leakage (P6-P8).
var systemPromptLeakageRules = RuleSet{
	{
		ID: "P6", Category: CategorySystemPromptLeakage, Severity: SeverityError, Confidence: 0.85,
		Pattern:     `(?i)(?:print|output|show|display|reveal|expose|return|echo)\s+(?:your\s+)?(?:full\s+|complete\s+)?(?:system\s+)?(?:prompt|instructions?)`,
		Hint:        "direct system-prompt extraction request",
		Remediation: "do not instruct the agent to disclose its system prompt or hidden rules",
	},
	{
		ID: "P7", Category: CategorySystemPromptLeakage, Severity: SeverityWarning, Confidence: 0.75,
		Pattern:     `(?i)(?:summarize|paraphrase|rephrase|translate|encode|encrypt|rot13|base64|reverse)\s+(?:your\s+)?(?:system\s+)?(?:prompt|instructions?)`,
		Hint:        "indirect system-prompt extraction (summarize/translate/encode)",
		Remediation: "remove instructions designed to extract system-prompt content via transformation",
	},
	{
		ID: "P8", Category: CategorySystemPromptLeakage, Severity: SeverityError, Confidence: 0.85,
		Pattern:     `(?i)(?:write|save|store|log|dump|send|post|upload|transmit)\s+(?:your\s+)?(?:system\s+)?(?:prompt|instructions?)\s+(?:to|into|via)`,
		Hint:        "prompt exfiltration via tool (write/send/log)",
		Remediation: "never persist or transmit the agent's system prompt",
	},
}

// Pattern category: Data Exfiltration (E-series).
var dataExfiltrationRules = RuleSet{
	{
		ID: "E1", Category: CategoryDataExfiltration, Severity: SeverityWarning, Confidence: 0.65,
		Pattern:     `(?i)(?:requests|httpx|axios)\.(?:post|put|patch)\s*\(\s*['"]https?://|fetch\s*\(\s*['"]https?://[^'"]+['"]\s*,\s*\{[^}]*method\s*:\s*['"](?:POST|PUT|PATCH)`,
		Hint:        "outbound POST/PUT (data-exfiltration shape)",
		Remediation: "verify the destination is intentional and not exfiltrating user data",
		Globs:       codeFileGlobs,
	},
	{
		ID: "E1b", Category: CategoryDataExfiltration, Severity: SeverityWarning, Confidence: 0.6,
		Pattern:     `https?://(?:collect|telemetry|analytics|log|track|beacon|exfil)\.[\w.-]+/`,
		Hint:        "telemetry/analytics-style outbound URL",
		Remediation: "audit the destination; collect./telemetry./log. domains are common exfiltration patterns",
	},
	{
		ID: "E2", Category: CategoryDataExfiltration, Severity: SeverityError, Confidence: 0.8,
		Pattern:     `(?i)os\.environ(?:\.get)?\s*[\[(]\s*['"][^'"]*(?:KEY|SECRET|TOKEN|PASSWORD|PASSWD|CREDENTIAL|API_?KEY)[^'"]*['"]`,
		Hint:        "harvests secret-shaped environment variable",
		Remediation: "do not read credential-named env vars unless strictly required by the skill's documented purpose",
		Globs:       codeFileGlobs,
	},
	{
		ID: "E2b", Category: CategoryDataExfiltration, Severity: SeverityWarning, Confidence: 0.7,
		Pattern:     `(?i)\bfor\s+\w+\s*,\s*\w+\s+in\s+os\.environ\.items\(\)`,
		Hint:        "iterates the full environment",
		Remediation: "scope env-var access to the specific keys the skill needs",
		Globs:       codeFileGlobs,
	},
	{
		ID: "E2c", Category: CategoryDataExfiltration, Severity: SeverityWarning, Confidence: 0.7,
		Pattern:     `(?i)\bprintenv\b|\benv\s*\|\s*grep\s+(?:-i\s+)?(?:key|secret|token|password|api)`,
		Hint:        "shell env enumeration for credential strings",
		Remediation: "remove environment enumeration aimed at credentials",
	},
	{
		ID: "E3", Category: CategoryDataExfiltration, Severity: SeverityWarning, Confidence: 0.7,
		Pattern:     `(?i)(?:glob(?:\.glob)?\s*\([^)]*|find\s+[~$/]\S*\s+[^|]*-name\s+['"]?\*?)(?:\.env|\.ssh|\.aws|\.config|\.gnupg|credentials|secrets)`,
		Hint:        "filesystem scan for credential-shaped files",
		Remediation: "do not enumerate credential directories",
	},
	{
		ID: "E3b", Category: CategoryDataExfiltration, Severity: SeverityWarning, Confidence: 0.65,
		Pattern:     `(?i)(?:os\.walk|Path\.home\(\)\.(?:glob|rglob))\s*\([^)]*(?:home|~|/Users|/home)`,
		Hint:        "walks home directory tree (potential credential reconnaissance)",
		Remediation: "scope filesystem walks to explicit, documented paths",
		Globs:       codeFileGlobs,
	},
	{
		ID: "E4", Category: CategoryDataExfiltration, Severity: SeverityError, Confidence: 0.85,
		Pattern:     `(?i)(?:send|transmit|upload|post|forward|export|dump|log)\s+(?:the\s+)?(?:full\s+)?(?:conversation|chat|dialog|session|message)\s+(?:history|context|log|content)?`,
		Hint:        "exfiltrates conversation context",
		Remediation: "do not transmit user conversations or session content",
	},
}

// Pattern category: Privilege Escalation (PE-series).
var privilegeEscalationRules = RuleSet{
	{
		ID: "PE1", Category: CategoryPrivilegeEscalation, Severity: SeverityWarning, Confidence: 0.8,
		Pattern:     `(?i)(?:bypass|skip|disable|ignore)\s+(?:the\s+)?(?:permission|access)\s+(?:check|validation|restriction|control)`,
		Hint:        "asks the host to skip permission/access checks",
		Remediation: "remove instructions that disable host-side permission enforcement",
	},
	{
		ID: "PE2", Category: CategoryPrivilegeEscalation, Severity: SeverityWarning, Confidence: 0.7,
		// Go RE2 has no lookarounds, so we list the unsafe forms explicitly
		// rather than negate the safe ones. PE2b covers -i/-s/-E/su.
		Pattern:     `(?:^|[\s;&|` + "`" + `(])sudo\s+[A-Za-z0-9./]`,
		SkipPattern: `(?i)\b(?:example|e\.g\.|do\s+not|don't|never|avoid|warning|requires?|may\s+need)\b`,
		Hint:        "sudo invocation",
		Remediation: "avoid sudo inside a skill; document required privileges instead of escalating in-line",
		Globs:       append([]string{"**/*.md", "**/*.rst"}, codeFileGlobs...),
	},
	{
		ID: "PE2b", Category: CategoryPrivilegeEscalation, Severity: SeverityError, Confidence: 0.9,
		Pattern:     `\bsudo\s+su\b|\bsudo\s+-[isSE]\b`,
		Hint:        "interactive root shell elevation",
		Remediation: "never escalate to a root shell from a skill",
	},
	{
		ID: "PE3", Category: CategoryPrivilegeEscalation, Severity: SeverityError, Confidence: 0.9,
		Pattern:     `~?/?\.ssh/(?:id_rsa|id_ed25519|id_ecdsa|id_dsa|authorized_keys)\b`,
		SkipPattern: `(?i)(?:example|placeholder|do\s+not|never\s+(?:read|access|commit))`,
		Hint:        "references private SSH key material",
		Remediation: "remove direct references to SSH private keys; require the user to surface credentials explicitly",
	},
	{
		ID: "PE3b", Category: CategoryPrivilegeEscalation, Severity: SeverityError, Confidence: 0.9,
		Pattern:     `~?/?\.aws/(?:credentials|config)\b|application_default_credentials\.json\b|\.kube/config\b`,
		SkipPattern: `(?i)(?:example|placeholder|do\s+not|never\s+(?:read|access|commit))`,
		Hint:        "references cloud-provider credential file",
		Remediation: "do not read user cloud credentials; require explicit configuration through documented inputs",
	},
	{
		ID: "PE3c", Category: CategoryPrivilegeEscalation, Severity: SeverityCritical, Confidence: 0.95,
		Pattern:     `/etc/shadow\b`,
		Hint:        "reads /etc/shadow (system password hashes)",
		Remediation: "remove all references to /etc/shadow",
	},
}

// Pattern category: Supply Chain (SC-series, pattern subset).
var supplyChainPatternRules = RuleSet{
	{
		ID: "SC2", Category: CategorySupplyChain, Severity: SeverityError, Confidence: 0.9,
		Pattern:     `(?i)(?:curl|wget)\s+[^|]*\|\s*(?:sudo\s+)?(?:ba|z|fi)?sh\b`,
		Hint:        "fetches and executes remote script via shell pipe",
		Remediation: "download, audit, and execute scripts separately; never pipe curl/wget into a shell",
	},
	{
		ID: "SC2b", Category: CategorySupplyChain, Severity: SeverityError, Confidence: 0.8,
		Pattern:     `(?i)(?:curl|wget)\s+[^&]*-o\s+\S+\s*&&\s*(?:sudo\s+)?(?:ba|z|fi)?sh\s+\S+`,
		Hint:        "downloads then immediately executes remote script",
		Remediation: "audit fetched scripts before running them",
	},
	{
		ID: "SC3", Category: CategorySupplyChain, Severity: SeverityError, Confidence: 0.95,
		Pattern:     `(?i)\b(?:exec|eval)\s*\(\s*(?:base64\.)?b64decode\s*\(`,
		Hint:        "decodes base64 to exec/eval (obfuscated execution)",
		Remediation: "remove obfuscated execution paths; use plain, reviewable code",
		Globs:       codeFileGlobs,
	},
	{
		ID: "SC3b", Category: CategorySupplyChain, Severity: SeverityError, Confidence: 0.9,
		Pattern:     `(?i)\b(?:marshal\.loads|pickle\.loads)\s*\(`,
		Hint:        "deserializes marshal/pickle (arbitrary-code-execution sink)",
		Remediation: "use a safe data format (JSON/YAML); pickle/marshal are unsafe with untrusted input",
		Globs:       []string{"**/*.py"},
	},
	{
		ID: "SC3c", Category: CategorySupplyChain, Severity: SeverityWarning, Confidence: 0.6,
		Pattern:     `["'][A-Za-z0-9+/=]{300,}["']`,
		Hint:        "long opaque base64-looking literal",
		Remediation: "audit large encoded blobs; obfuscated payloads are a common malware vector",
		Globs:       codeFileGlobs,
	},
	{
		ID: "SC3d", Category: CategorySupplyChain, Severity: SeverityWarning, Confidence: 0.6,
		Pattern:     `["'][A-Fa-f0-9]{200,}["']`,
		Hint:        "long opaque hex literal",
		Remediation: "audit hex-encoded literals; they may hide executable bytes",
		Globs:       codeFileGlobs,
	},
}

// Pattern category: Excessive Agency (EA-series).
var excessiveAgencyRules = RuleSet{
	{
		ID: "EA1", Category: CategoryExcessiveAgency, Severity: SeverityWarning, Confidence: 0.8,
		Pattern:     `(?i)(?:unrestricted|unlimited|unconstrained)\s+(?:tool|function|api|shell)\s+(?:access|use|calls?)`,
		Hint:        "requests unrestricted tool access",
		Remediation: "constrain the skill to an explicit allowlist of tools",
	},
	{
		ID: "EA1b", Category: CategoryExcessiveAgency, Severity: SeverityWarning, Confidence: 0.8,
		Pattern:     `(?i)(?:allow|grant|enable)\s+(?:access\s+to\s+)?(?:all|any|every)\s+(?:tools?|commands?|capabilities)`,
		Hint:        "grants blanket tool/capability access",
		Remediation: "list only the tools the skill genuinely needs",
	},
	{
		ID: "EA2", Category: CategoryExcessiveAgency, Severity: SeverityError, Confidence: 0.85,
		Pattern:     `(?i)(?:skip|bypass|disable|suppress)\s+(?:user\s+)?(?:confirmation|approval|consent|verification)|auto[-_ ]?(?:approve|confirm|execute|accept)`,
		SkipPattern: `(?i)(?:do\s+not|never|must\s+not|cannot|should\s+not)\s+(?:skip|bypass|disable|auto-?)`,
		Hint:        "removes human-in-the-loop confirmation",
		Remediation: "keep user confirmation for high-impact or destructive operations",
	},
	{
		ID: "EA2b", Category: CategoryExcessiveAgency, Severity: SeverityWarning, Confidence: 0.8,
		Pattern:     `(?i)(?:do\s+not|don't|never)\s+(?:ask|prompt|confirm|verify|check)\s+(?:the\s+)?(?:user|before)`,
		Hint:        "instruction to never confirm with the user",
		Remediation: "preserve user confirmation flows for sensitive operations",
	},
	{
		ID: "EA3", Category: CategoryExcessiveAgency, Severity: SeverityInfo, Confidence: 0.6,
		Pattern:     `(?i)(?:while\s+you(?:'re|\s+are)\s+at\s+it|on\s+top\s+of\s+that)[,.]?\s*(?:also\s+)?(?:do|perform|execute|run)`,
		Hint:        "encourages out-of-scope side-actions",
		Remediation: "limit the skill to its documented purpose",
	},
	{
		ID: "EA4", Category: CategoryExcessiveAgency, Severity: SeverityWarning, Confidence: 0.75,
		Pattern:     `(?i)(?:unlimited|infinite|unbounded|no\s+limits?)\s+(?:api\s+)?(?:calls?|requests?|queries?|invocations?|retries?)`,
		Hint:        "advocates unbounded resource use",
		Remediation: "document explicit rate limits, retry caps, and timeouts",
	},
	{
		ID: "EA4b", Category: CategoryExcessiveAgency, Severity: SeverityWarning, Confidence: 0.7,
		Pattern:     `(?i)max[_-]?retries?\s*=\s*(?:None|0|-1|float\(\s*['"]inf['"]\s*\)|math\.inf)|timeout\s*=\s*(?:None|0|float\(\s*['"]inf['"]\s*\)|math\.inf)`,
		Hint:        "disables retry / timeout bounds",
		Remediation: "set finite retry counts and timeouts",
		Globs:       codeFileGlobs,
	},
}

// Pattern category: Output Handling (OH-series).
var outputHandlingRules = RuleSet{
	{
		ID: "OH1", Category: CategoryOutputHandling, Severity: SeverityError, Confidence: 0.9,
		Pattern:     `(?i)(?:exec|eval)\s*\(\s*(?:response|output|result|answer|completion|llm_output|ai_output)\b`,
		Hint:        "executes raw model output",
		Remediation: "validate / sanitize model output before passing it to exec/eval",
		Globs:       codeFileGlobs,
	},
	{
		ID: "OH1b", Category: CategoryOutputHandling, Severity: SeverityError, Confidence: 0.85,
		Pattern:     `(?i)subprocess\.\w+\s*\([^)]*(?:response|output|result|completion|llm_output|ai_output)`,
		Hint:        "passes model output to subprocess",
		Remediation: "treat model output as untrusted input; never run it as a command",
		Globs:       codeFileGlobs,
	},
	{
		ID: "OH1c", Category: CategoryOutputHandling, Severity: SeverityError, Confidence: 0.85,
		Pattern:     `(?i)\binnerHTML\s*=\s*(?:response|output|result|completion)|document\.write\s*\(\s*(?:response|output|result|completion)`,
		Hint:        "injects model output into DOM",
		Remediation: "HTML-encode model output before inserting into the DOM",
		Globs:       []string{"**/*.js", "**/*.ts", "**/*.tsx", "**/*.jsx", "**/*.html"},
	},
	{
		ID: "OH2", Category: CategoryOutputHandling, Severity: SeverityWarning, Confidence: 0.8,
		Pattern:     `(?i)(?:inject|insert|embed)\s+(?:the\s+)?(?:output|response|result)\s+(?:from\s+\w+\s+)?(?:into|as)\s+(?:the\s+)?(?:system\s+prompt|instructions?|next\s+prompt)`,
		Hint:        "feeds output across trust contexts (e.g., output → system prompt)",
		Remediation: "do not splice model output into system prompts or instruction text",
	},
	{
		ID: "OH3", Category: CategoryOutputHandling, Severity: SeverityWarning, Confidence: 0.7,
		Pattern:     `(?i)max[_-]?tokens?\s*=\s*(?:None|float\(\s*['"]inf['"]\s*\)|999999|1000000)`,
		Hint:        "disables output-length cap",
		Remediation: "set a sensible max_tokens cap",
		Globs:       codeFileGlobs,
	},
}

// Pattern category: Memory Poisoning (MP-series).
var memoryPoisoningRules = RuleSet{
	{
		ID: "MP1", Category: CategoryMemoryPoisoning, Severity: SeverityWarning, Confidence: 0.8,
		Pattern:     `(?i)(?:always\s+)?remember\s+(?:this|that|the\s+following)\s+(?:for|in)\s+(?:(?:all|every|future)\s+)+(?:interactions?|conversations?|sessions?)`,
		Hint:        "asks the agent to persist injected content across sessions",
		Remediation: "do not instruct the agent to retain skill content as permanent memory",
	},
	{
		ID: "MP1b", Category: CategoryMemoryPoisoning, Severity: SeverityWarning, Confidence: 0.75,
		Pattern:     `(?i)(?:from\s+now\s+on|henceforth|going\s+forward|permanently)[,:]?\s*(?:always|you\s+must|you\s+should|treat)`,
		Hint:        "implants persistent behavior rule",
		Remediation: "remove instructions intended to permanently change agent behavior",
	},
	{
		ID: "MP2", Category: CategoryMemoryPoisoning, Severity: SeverityError, Confidence: 0.85,
		Pattern:     `(?i)(?:displace|push\s+out|overwrite|crowd\s+out|evict)\s+(?:the\s+)?(?:(?:original|system|previous|safety)\s+)+(?:instructions?|prompts?|rules)`,
		Hint:        "attempts to evict legitimate context",
		Remediation: "remove text designed to crowd out system instructions",
	},
	{
		ID: "MP3", Category: CategoryMemoryPoisoning, Severity: SeverityError, Confidence: 0.85,
		Pattern:     `(?i)(?:clear|reset|wipe|erase|purge|forget|discard|drop|abandon)\s+(?:all\s+)?(?:your\s+)?(?:previous\s+)?(?:memory|context|state|history|instructions?)`,
		SkipPattern: `(?i)(?:do\s+not|never|must\s+not|won't|cannot)`,
		Hint:        "instructs agent to forget its prior context",
		Remediation: "do not instruct the agent to discard system instructions or session memory",
	},
	{
		ID: "MP3b", Category: CategoryMemoryPoisoning, Severity: SeverityCritical, Confidence: 0.9,
		Pattern:     `(?i)(?:poison|contaminate|corrupt|taint)\s+(?:your|the\s+agent['s]*)\s+(?:memory|context|state|knowledge)`,
		Hint:        "explicit memory-poisoning intent",
		Remediation: "remove memory-poisoning instructions",
	},
}

// Pattern category: Tool Misuse (TM-series).
var toolMisuseRules = RuleSet{
	{
		ID: "TM1a", Category: CategoryToolMisuse, Severity: SeverityError, Confidence: 0.85,
		Pattern:     `(?i)subprocess\.\w+\s*\([^)]*shell\s*=\s*True|Popen\s*\([^)]*shell\s*=\s*True`,
		Hint:        "subprocess called with shell=True (command-injection risk)",
		Remediation: "use shell=False with an explicit argv list; never interpolate user input into shell strings",
		Globs:       codeFileGlobs,
	},
	{
		ID: "TM1b", Category: CategoryToolMisuse, Severity: SeverityError, Confidence: 0.9,
		Pattern:     `\brm\s+-[a-zA-Z]*r[a-zA-Z]*f?[a-zA-Z]*\s+(?:/|~|\$HOME|\.\.\s|\*\s|\*$)`,
		Hint:        "recursive deletion of root/home/parent/wildcard",
		Remediation: "remove or constrain the rm path; never delete root/home or unqualified globs",
	},
	{
		ID: "TM1c", Category: CategoryToolMisuse, Severity: SeverityWarning, Confidence: 0.75,
		Pattern:     `(?i)--no-?verify\b|--insecure\b|--force\s+(?:delete|remove|push|reset|clean)`,
		Hint:        "force / verification-skip flag",
		Remediation: "remove --force/--insecure/--no-verify; resolve the underlying issue instead",
	},
	{
		ID: "TM1d", Category: CategoryToolMisuse, Severity: SeverityError, Confidence: 0.85,
		Pattern:     `\bchmod\s+(?:-R\s+)?(?:0?777|a\+rwx)\b`,
		Hint:        "world-writable permissions",
		Remediation: "use the minimum permission bits required",
	},
	{
		ID: "TM2", Category: CategoryToolMisuse, Severity: SeverityError, Confidence: 0.9,
		Pattern:     `(?:&&|;)\s*(?:curl|wget)\s+[^|]*\|\s*(?:ba|z|fi)?sh\b`,
		Hint:        "chains a fetch directly into a shell",
		Remediation: "separate fetch and execute; audit downloaded scripts before running",
	},
	{
		ID: "TM2b", Category: CategoryToolMisuse, Severity: SeverityWarning, Confidence: 0.75,
		Pattern:     `(?i)(?:chain|combine|sequence|pipe)\s+(?:these\s+)?(?:tools?|commands?|actions?)\s+to\s+(?:bypass|circumvent|avoid|skip)\s+(?:the\s+)?(?:safety|security|restriction|check)`,
		Hint:        "describes chaining tools to dodge safety checks",
		Remediation: "remove instructions that pipeline tool calls to defeat safety controls",
	},
	{
		ID: "TM3", Category: CategoryToolMisuse, Severity: SeverityWarning, Confidence: 0.75,
		Pattern:     `(?i)\bverify\s*=\s*False\b|VERIFY_SSL\s*=\s*False|NODE_TLS_REJECT_UNAUTHORIZED\s*=\s*['"]?0['"]?`,
		Hint:        "TLS verification disabled",
		Remediation: "keep TLS verification enabled; provide a trusted CA bundle if needed",
		Globs:       codeFileGlobs,
	},
	{
		ID: "TM3b", Category: CategoryToolMisuse, Severity: SeverityWarning, Confidence: 0.7,
		Pattern:     `(?i)\b(?:allow[_-]?anonymous|anonymous[_-]?access)\s*=\s*(?:True|true)|(?:auth|authentication)\s*=\s*(?:None|False|disabled|off)`,
		Hint:        "auth disabled or anonymous access enabled",
		Remediation: "require authentication for any externally-exposed surface",
		Globs:       codeFileGlobs,
	},
	{
		ID: "TM3c", Category: CategoryToolMisuse, Severity: SeverityInfo, Confidence: 0.6,
		Pattern:     `(?i)\bcors\b[^=]*=\s*['"]?\*['"]?`,
		Hint:        "wildcard CORS",
		Remediation: "restrict CORS to specific origins",
		Globs:       codeFileGlobs,
	},
	{
		ID: "TM_eval", Category: CategoryToolMisuse, Severity: SeverityError, Confidence: 0.8,
		Pattern:     "\\beval\\s*[`$(]",
		Hint:        "dynamic shell eval",
		Remediation: "do not evaluate dynamically-constructed shell strings",
	},
	{
		ID: "TM_forkbomb", Category: CategoryToolMisuse, Severity: SeverityCritical, Confidence: 0.95,
		Pattern:     `:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`,
		Hint:        "classic fork bomb",
		Remediation: "remove the fork bomb",
	},
}

// Pattern category: Rogue Agent (RA-series).
var rogueAgentRules = RuleSet{
	{
		ID: "RA1", Category: CategoryRogueAgent, Severity: SeverityCritical, Confidence: 0.95,
		Pattern:     `(?i)open\s*\(\s*__file__\s*,\s*['"]w|(?:Path|pathlib)\s*\(\s*__file__\s*\)\s*\.\s*write_text|fs\.writeFileSync\s*\(\s*__filename\b`,
		Hint:        "writes to its own source file",
		Remediation: "remove self-modifying code paths",
		Globs:       codeFileGlobs,
	},
	{
		ID: "RA1b", Category: CategoryRogueAgent, Severity: SeverityError, Confidence: 0.85,
		Pattern:     `(?i)(?:modify|edit|change|rewrite|update)\s+(?:your\s+own|its\s+own|this\s+skill(?:'s)?)\s+(?:code|source|file|config|skill\.md)`,
		Hint:        "instruction to self-modify",
		Remediation: "skill content must be inert at runtime",
	},
	{
		ID: "RA1c", Category: CategoryRogueAgent, Severity: SeverityError, Confidence: 0.85,
		Pattern:     `(?i)\bself[_-]?(?:modify|update|rewrite|patch|evolve)\b`,
		Hint:        "self-modifying / self-evolving language",
		Remediation: "remove self-modification semantics",
	},
	{
		ID: "RA2", Category: CategoryRogueAgent, Severity: SeverityError, Confidence: 0.85,
		Pattern:     `(?i)\bcrontab\s+-[el]\b|crontab\s+[^|]*>>?\s*/|(?:add|write|append|install)\s+(?:to|into)\s+(?:~/)?\.(?:bashrc|zshrc|profile|bash_profile|config/fish)|>>?\s*~?/?\.(?:bashrc|zshrc|profile|bash_profile|config/fish)\b`,
		Hint:        "establishes persistence via cron / shell rc",
		Remediation: "do not install cross-session persistence from a skill",
	},
	{
		ID: "RA2b", Category: CategoryRogueAgent, Severity: SeverityWarning, Confidence: 0.7,
		Pattern:     `(?i)(?:create|install|register|add)\s+(?:a\s+)?(?:systemd\s+)?(?:service|daemon|launchctl|launchd)\b`,
		Hint:        "registers a long-running background service",
		Remediation: "remove service/daemon registration from skill content",
	},
}

// Pattern category: Harmful Content (P5).
//
// We mirror SkillSpector's narrow CRITICAL list (actionable instructions
// to produce harmful outcomes). The SkipPattern guards against benign
// mentions in educational / warning contexts.
var harmfulContentRules = RuleSet{
	{
		ID: "P5a", Category: CategoryHarmfulContent, Severity: SeverityCritical, Confidence: 0.95,
		Pattern:     `(?i)\b(?:mix\s+bleach\s+(?:and|with)\s+ammonia|chlorine\s+gas\s+at\s+home|make\s+(?:a\s+)?(?:bomb|explosive|pipe\s+bomb|nail\s+bomb))\b`,
		Hint:        "actionable instruction for dangerous chemical / explosive",
		Remediation: "remove instructions that produce hazardous physical outcomes",
	},
	{
		ID: "P5b", Category: CategoryHarmfulContent, Severity: SeverityCritical, Confidence: 0.95,
		Pattern:     `(?i)(?:you\s+should|try\s+to|please)\s+(?:hurt|harm|injure|kill)\s+(?:yourself|someone|a\s+person)`,
		Hint:        "directive to inflict physical harm",
		Remediation: "remove harm-directive content",
	},
	{
		ID: "P5c", Category: CategoryHarmfulContent, Severity: SeverityWarning, Confidence: 0.5,
		Pattern:     `(?i)\b(?:cyanide|arsenic|ricin|botulinum|sarin|vx\s+gas|mustard\s+gas|anthrax)\b`,
		SkipPattern: `(?i)\b(?:history\s+of|discovered\s+by|chemical\s+compound|toxicology|antidote|symptoms|warning|do\s+not|never|avoid)\b`,
		Hint:        "mentions controlled toxin (review for context)",
		Remediation: "ensure mentions are educational/warning context, not operative",
	},
}

// Pattern category: Trigger Abuse (TR-series). These rules run against
// SKILL.md frontmatter values during the patterns check by re-injecting
// the relevant frontmatter strings into the SKILL.md content stream;
// the simpler line-by-line match catches them in the frontmatter block.
var triggerAbuseRules = RuleSet{
	{
		ID: "TR3", Category: CategoryTriggerAbuse, Severity: SeverityWarning, Confidence: 0.8,
		Pattern:     `(?i)^description\s*:\s*(?:anything|everything|whatever|always|any\s+(?:question|request|task|input))\s*$`,
		Hint:        "trigger description bait-words ('anything', 'always')",
		Remediation: "use a specific, descriptive trigger that maps to the skill's purpose",
		Globs:       []string{"**/SKILL.md"},
	},
}
