package model

// targetRegistry is the canonical, compiled-in routing table that maps every
// supported coding agent to the on-disk directories qvr symlinks skills into.
// It is the single source of truth for `qvr add --target`, `qvr target`, the
// linker (internal/skill/linker.go), `qvr sync`, and `qvr doctor`.
//
// Each entry carries BOTH a project-relative dir (LocalDir, used for project
// installs that live in qvr.lock) and a home-relative dir (GlobalDir, used for
// the `--global` ambient lane). qvr reroutes to one or the other based on the
// install scope — see ResolveTargetPath.
//
// Paths are sourced from each tool's official documentation where one exists.
// The `docs:` comment on each entry cites a path-specific skill reference when
// available. Entries marked `(convention)` follow the widely-used AGENTS.md /
// .agents skills convention but have no first-party skill-location docs yet, so
// they are best-effort and may move. Editing this table is the supported way to
// add or re-route an agent — it is plain data, no logic.
//
// Many newer CLIs read the shared AGENTS.md `.agents/skills` location rather
// than a tool-private dir; those legitimately share a project path.
var targetRegistry = []Target{
	// --- First-party / widely documented ----------------------------------
	{
		Name: "claude", Display: "Claude Code",
		LocalDir: ".claude/skills", GlobalDir: "~/.claude/skills",
		Aliases: []string{"claude-code"},
		// docs: https://docs.claude.com/en/docs/claude-code/skills
	},
	{
		Name: "codex", Display: "OpenAI Codex CLI",
		LocalDir: ".agents/skills", GlobalDir: "~/.agents/skills",
		// docs: https://developers.openai.com/codex/skills/ (also reads system /etc/codex/skills)
	},
	{
		Name: "cursor", Display: "Cursor",
		LocalDir: ".agents/skills", GlobalDir: "~/.cursor/skills",
		// docs: https://cursor.com/docs/skills
	},
	{
		Name: "copilot", Display: "GitHub Copilot CLI",
		LocalDir: ".github/skills", GlobalDir: "~/.copilot/skills",
		Aliases: []string{"github-copilot"},
		// docs: https://docs.github.com/en/copilot/how-tos/use-copilot-agents/coding-agent/create-skills
	},
	{
		Name: "gemini", Display: "Gemini CLI / Antigravity",
		LocalDir: ".agents/skills", GlobalDir: "~/.gemini/skills",
		Aliases: []string{"antigravity", "gemini-cli", "antigravity-cli"},
		// docs: https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/skills.md
	},
	{
		Name: "opencode", Display: "OpenCode",
		LocalDir: ".opencode/skills", GlobalDir: "~/.config/opencode/skills",
		// docs: https://opencode.ai/docs/skills
	},
	{
		Name: "windsurf", Display: "Windsurf",
		LocalDir: ".windsurf/skills", GlobalDir: "~/.codeium/windsurf/skills",
		// docs: https://docs.windsurf.com/windsurf/cascade/skills
	},
	{
		Name: "zed", Display: "Zed",
		LocalDir: ".agents/skills", GlobalDir: "~/.agents/skills",
		// docs: https://zed.dev/docs/ai/skills
	},
	{
		Name: "amp", Display: "Sourcegraph Amp",
		LocalDir: ".agents/skills", GlobalDir: "~/.config/agents/skills",
		// docs: https://ampcode.com/manual (convention)
	},
	{
		Name: "goose", Display: "Block Goose",
		LocalDir: ".agents/skills", GlobalDir: "~/.agents/skills",
		// docs: https://goose-docs.ai/docs/guides/context-engineering/using-skills/
	},
	{
		Name: "cline", Display: "Cline",
		LocalDir: ".cline/skills", GlobalDir: "~/.cline/skills",
		// docs: https://docs.cline.bot/customization/skills
	},
	{
		Name: "roo", Display: "Roo Code",
		LocalDir: ".agents/skills", GlobalDir: "~/.roo/skills",
		// docs: https://roocodeinc.github.io/Roo-Code/features/skills
	},
	{
		Name: "kilocode", Display: "Kilo Code",
		LocalDir: ".kilocode/skills", GlobalDir: "~/.kilocode/skills",
		Aliases: []string{"kilo"},
		// docs: https://kilo.ai/docs/features/skills
	},
	{
		Name: "qwen", Display: "Qwen Code",
		LocalDir: ".qwen/skills", GlobalDir: "~/.qwen/skills",
		Aliases: []string{"qwen-code"},
		// docs: https://qwenlm.github.io/qwen-code-docs/en/users/features/skills/
	},
	{
		Name: "continue", Display: "Continue",
		LocalDir: ".continue/skills", GlobalDir: "~/.continue/skills",
		// docs: https://docs.continue.dev (convention; no first-party agent-skills location found)
	},
	{
		Name: "augment", Display: "Augment Code",
		LocalDir: ".augment/skills", GlobalDir: "~/.augment/skills",
		// docs: https://docs.augmentcode.com/using-augment/skills
	},
	{
		Name: "crush", Display: "Charm Crush",
		LocalDir: ".crush/skills", GlobalDir: "~/.config/crush/skills",
		// docs: https://github.com/charmbracelet/crush (built-in skills exist; location is convention)
	},
	{
		Name: "aiderdesk", Display: "AiderDesk",
		LocalDir: ".aider-desk/skills", GlobalDir: "~/.aider-desk/skills",
		Aliases: []string{"aider-desk"},
		// docs: https://github.com/hotovo/aider-desk (convention)
	},
	{
		Name: "warp", Display: "Warp",
		LocalDir: ".agents/skills", GlobalDir: "~/.agents/skills",
		// docs: https://docs.warp.dev/agent-platform/capabilities/skills
	},
	{
		Name: "droid", Display: "Factory Droid",
		LocalDir: ".factory/skills", GlobalDir: "~/.factory/skills",
		// docs: https://docs.factory.ai/cli/configuration/skills
	},
	{
		Name: "devin", Display: "Devin",
		LocalDir: ".agents/skills", GlobalDir: "~/.config/devin/skills",
		Aliases: []string{"devin-terminal"},
		// docs: https://docs.devin.ai/product-guides/skills
	},
	{
		Name: "junie", Display: "JetBrains Junie",
		LocalDir: ".junie/skills", GlobalDir: "~/.junie/skills",
		// docs: https://junie.jetbrains.com/docs/agent-skills.html
	},
	{
		Name: "kiro", Display: "Kiro",
		LocalDir: ".kiro/skills", GlobalDir: "~/.kiro/skills",
		Aliases: []string{"kiro-cli"},
		// docs: https://kiro.dev/docs/skills/
	},
	{
		Name: "trae", Display: "Trae",
		LocalDir: ".trae/skills", GlobalDir: "~/.trae/skills",
		// docs: https://docs.trae.ai/ide/skills (also reads .agents/skills via opt-in setting)
	},
	{
		Name: "trae-cn", Display: "Trae CN",
		LocalDir: ".trae/skills", GlobalDir: "~/.trae-cn/skills",
		// docs: https://www.trae.com.cn (convention; no first-party skill-location docs found)
	},
	{
		Name: "vibe", Display: "Mistral Vibe",
		LocalDir: ".vibe/skills", GlobalDir: "~/.vibe/skills",
		Aliases: []string{"mistral-vibe"},
		// docs: https://docs.mistral.ai/vibe/code/cli/skills
	},
	{
		Name: "openhands", Display: "OpenHands",
		LocalDir: ".agents/skills", GlobalDir: "~/.agents/skills",
		// docs: https://docs.openhands.dev/overview/skills (.openhands/skills is a deprecated fallback)
	},
	{
		Name: "tabnine", Display: "Tabnine",
		LocalDir: ".tabnine/agent/skills", GlobalDir: "~/.tabnine/agent/skills",
		Aliases: []string{"tabnine-cli"},
		// docs: https://docs.tabnine.com/main/getting-started/tabnine-cli/features/agent-skills
	},
	{
		Name: "replit", Display: "Replit Agent",
		LocalDir: ".agents/skills", GlobalDir: "~/.config/agents/skills",
		// docs: https://docs.replit.com (convention)
	},
	{
		Name: "kimi", Display: "Kimi CLI",
		LocalDir: ".agents/skills", GlobalDir: "~/.config/agents/skills",
		Aliases: []string{"kimi-cli"},
		// docs: https://github.com/MoonshotAI (convention; no first-party skill-location docs found)
	},
	{
		Name: "iflow", Display: "iFlow CLI",
		LocalDir: ".iflow/skills", GlobalDir: "~/.iflow/skills",
		Aliases: []string{"iflow-cli"},
		// docs: https://docs.iflow.cn (convention; no first-party skill-location docs found)
	},

	// --- Generic / AGENTS.md standard -------------------------------------
	{
		Name: "project", Display: "Generic AGENTS.md agent",
		LocalDir: ".agents/skills", GlobalDir: "~/.agents/skills",
		Aliases: []string{"agents", "universal", "dexto", "witsy"},
		// docs: https://agents.md (the shared cross-agent convention)
	},

	// --- Additional tools ---------------------------------------------------
	{
		Name: "adal", Display: "Adal",
		LocalDir: ".adal/skills", GlobalDir: "~/.adal/skills",
		// docs: https://docs.sylph.ai/features/plugins-and-skills
	},
	{
		Name: "astrbot", Display: "AstrBot",
		LocalDir: "data/skills", GlobalDir: "~/.astrbot/data/skills",
		// docs: https://docs.astrbot.app/use/computer.html
	},
	{
		Name: "autohand", Display: "Autohand Code CLI",
		LocalDir: ".autohand/skills", GlobalDir: "~/.autohand/skills",
		Aliases: []string{"autohand-code"},
		// docs: https://github.com/autohandai/code-cli/blob/main/docs/agent-skills.md
	},
	{
		Name: "bob", Display: "Bob",
		LocalDir: ".bob/skills", GlobalDir: "~/.bob/skills",
		// docs: (convention; no first-party skill-location docs found)
	},
	{
		Name: "bub", Display: "Bub",
		LocalDir: ".agents/skills", GlobalDir: "~/.agents/skills",
		// docs: https://bub.build/docs/build/skills/ (implements the agentskills.io standard locations)
	},
	{
		Name: "codearts", Display: "CodeArts Doer",
		LocalDir: ".codeartsdoer/skills", GlobalDir: "~/.codeartsdoer/skills",
		Aliases: []string{"codearts-agent"},
		// docs: (convention; no first-party skill-location docs found)
	},
	{
		Name: "codebuddy", Display: "CodeBuddy",
		LocalDir: ".codebuddy/skills", GlobalDir: "~/.codebuddy/skills",
		// docs: https://www.codebuddy.ai/docs/cli/skills
	},
	{
		Name: "codestudio", Display: "Code Studio",
		LocalDir: ".codestudio/skills", GlobalDir: "~/.codestudio/skills",
		Aliases: []string{"code-studio"},
		// docs: (convention; no first-party skill-location docs found)
	},
	{
		Name: "comate", Display: "Baidu Comate",
		LocalDir: ".comate/skills", GlobalDir: "~/.comate/skills",
		// docs: https://cloud.baidu.com/doc/COMATE/s/Nmma28iqe
	},
	{
		Name: "commandcode", Display: "Command Code",
		LocalDir: ".commandcode/skills", GlobalDir: "~/.commandcode/skills",
		Aliases: []string{"command-code"},
		// docs: https://commandcode.ai/docs/skills/commands
	},
	{
		Name: "cortex", Display: "Snowflake Cortex",
		LocalDir: ".cortex/skills", GlobalDir: "~/.snowflake/cortex/skills",
		// docs: https://docs.snowflake.com/en/user-guide/cortex-code/extensibility
	},
	{
		Name: "deepagents", Display: "DeepAgents",
		LocalDir: ".agents/skills", GlobalDir: "~/.deepagents/agent/skills",
		Aliases: []string{"deep-agents"},
		// docs: https://docs.langchain.com/oss/python/deepagents/skills
	},
	{
		Name: "fastagent", Display: "fast-agent",
		LocalDir: ".fast-agent/skills", GlobalDir: "~/.fast-agent/skills",
		Aliases: []string{"fast-agent"},
		// docs: https://fast-agent.ai/agents/skills/ (project dirs only — .fast-agent/skills,
		// .agents/skills, .claude/skills; global dir is convention, no documented global location)
	},
	{
		Name: "firebender", Display: "Firebender",
		LocalDir: ".firebender/skills", GlobalDir: "~/.firebender/skills",
		// docs: https://docs.firebender.com/multi-agent/skills
	},
	{
		Name: "forgecode", Display: "Forge Code",
		LocalDir: ".forge/skills", GlobalDir: "~/forge/skills",
		Aliases: []string{"forge-code"},
		// docs: https://forgecode.dev/docs/skills/
	},
	{
		Name: "hermes", Display: "Hermes",
		LocalDir: ".hermes/skills", GlobalDir: "~/.hermes/skills",
		Aliases: []string{"hermes-agent"},
		// docs: https://github.com/NousResearch/hermes-agent/blob/main/website/docs/reference/skills-catalog.md
	},
	{
		Name: "kode", Display: "Kode",
		LocalDir: ".kode/skills", GlobalDir: "~/.kode/skills",
		// docs: (convention; no first-party skill-location docs found)
	},
	{
		Name: "letta", Display: "Letta",
		LocalDir: ".agents/skills", GlobalDir: "~/.letta/skills",
		// docs: https://docs.letta.com/letta-code/skills/
	},
	{
		Name: "lingma", Display: "Alibaba Lingma",
		LocalDir: ".lingma/skills", GlobalDir: "~/.lingma/skills",
		// docs: https://www.alibabacloud.com/help/en/lingma/qoderwork-cn/user-guide/skills
	},
	{
		Name: "mcpjam", Display: "MCPJam",
		LocalDir: ".mcpjam/skills", GlobalDir: "~/.mcpjam/skills",
		// docs: https://www.mcpjam.com/blog/skills
	},
	{
		Name: "mux", Display: "Mux",
		LocalDir: ".mux/skills", GlobalDir: "~/.mux/skills",
		// docs: https://mux.coder.com/agents/agent-skills
	},
	{
		Name: "nanobot", Display: "nanobot",
		LocalDir: ".nanobot/skills", GlobalDir: "~/.nanobot/workspace/skills",
		// docs: https://nanobot.wiki/docs/0.1.5/use-nanobot/skills (global workspace dir only,
		// verified in nanobot/agent/skills.py; local dir is convention — no project-level loading)
	},
	{
		Name: "neovate", Display: "Neovate",
		LocalDir: ".neovate/skills", GlobalDir: "~/.neovate/skills",
		// docs: (convention; no first-party skill-location docs found)
	},
	{
		Name: "omp", Display: "Oh My Pi",
		LocalDir: ".omp/skills", GlobalDir: "~/.omp/agent/skills",
		Aliases: []string{"oh-my-pi"},
		// docs: https://github.com/can1357/oh-my-pi/blob/main/docs/skills.md
	},
	{
		Name: "ona", Display: "Ona",
		LocalDir: ".ona/skills", GlobalDir: "~/.ona/skills",
		// docs: https://ona.com/docs/ona/agents/skills (repo dir only — also reads .claude/skills
		// and .agents/skills; org skills are platform-configured, global dir is convention)
	},
	{
		Name: "openclaw", Display: "OpenClaw",
		LocalDir: "skills", GlobalDir: "~/.openclaw/skills",
		// docs: https://docs.openclaw.ai/cli/skills
	},
	{
		Name: "pi", Display: "Pi",
		LocalDir: ".pi/skills", GlobalDir: "~/.pi/agent/skills",
		// docs: https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/docs/skills.md (also reads .agents/skills)
	},
	{
		Name: "pochi", Display: "Pochi",
		LocalDir: ".pochi/skills", GlobalDir: "~/.pochi/skills",
		// docs: https://docs.getpochi.com/skills/
	},
	{
		Name: "purecode", Display: "PureCode",
		LocalDir: ".agents/skills", GlobalDir: "~/.purecode/skills",
		Aliases: []string{"purecode-ai"},
		// docs: (convention; no first-party skill-location docs found)
	},
	{
		Name: "qoder", Display: "Qoder",
		LocalDir: ".qoder/skills", GlobalDir: "~/.qoder/skills",
		// docs: https://docs.qoder.com/extensions/skills
	},
	{
		Name: "rovodev", Display: "Atlassian Rovo Dev",
		LocalDir: ".rovodev/skills", GlobalDir: "~/.rovodev/skills",
		Aliases: []string{"rovo-dev"},
		// docs: https://support.atlassian.com/rovo/docs/extend-rovo-dev-cli-with-agent-skills/
	},
	{
		Name: "verdent", Display: "Verdent",
		LocalDir: ".verdent/skills", GlobalDir: "~/.verdent/skills",
		// docs: https://www.verdent.ai/docs/verdent-manager/core-features/skills
	},
	{
		Name: "vtcode", Display: "VT Code",
		LocalDir: ".agents/skills", GlobalDir: "~/.agents/skills",
		Aliases: []string{"vt-code"},
		// docs: https://github.com/vinhnx/vtcode/blob/main/docs/skills/SKILLS_GUIDE.md
	},
	{
		Name: "workshop", Display: "Workshop",
		LocalDir: ".workshop/skills", GlobalDir: "~/.workshop/skills",
		// docs: https://docs.workshop.ai/core-concepts/skills.md (project dir documented; installs
		// are UI-managed — manual placement unverified; global dir is convention)
	},
	{
		Name: "zencoder", Display: "Zencoder",
		LocalDir: ".agents/skills", GlobalDir: "~/.agents/skills",
		// docs: https://docs.zencoder.ai/features/skills
	},
	{
		Name: "xcode-claude", Display: "Xcode Coding Assistant (Claude)",
		LocalDir: ".claude/skills", GlobalDir: "~/Library/Developer/Xcode/CodingAssistant/ClaudeAgentConfig/skills",
		// docs: https://developer.apple.com/documentation/xcode/setting-up-coding-intelligence (convention; config dir only)
	},
	{
		Name: "xcode-codex", Display: "Xcode Coding Assistant (Codex)",
		LocalDir: ".codex/skills", GlobalDir: "~/Library/Developer/Xcode/CodingAssistant/codex/skills",
		// docs: https://developer.apple.com/documentation/xcode/setting-up-coding-intelligence (convention; config dir only)
	},
}
