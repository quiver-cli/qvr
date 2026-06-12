// Package discover finds agents' native session stores on disk and feeds
// new/changed files through the rawtrace ingest pipeline. Agents are the
// capture infrastructure — each one already persists its own session history;
// discovery is the zero-configuration way qvr taps it: no agent config is
// ever touched.
//
// The design mirrors the target registry (internal/model/targets_data.go): a
// data-driven table of per-agent store descriptors, walked with a stat-diff
// ledger (store: scanned_files) so an unchanged file is never re-examined.
package discover

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/astra-sh/qvr/internal/ops/derive"
)

// StoreLayout describes how an agent persists sessions on disk.
type StoreLayout int

const (
	// LayoutAppendLog is an append-only JSONL file per session (the common
	// case) — ingested by cursor-tailing.
	LayoutAppendLog StoreLayout = iota
	// LayoutDocument is a whole JSON document the agent rewrites in place —
	// re-ingested by replacing the file's stored rows on change.
	LayoutDocument
	// LayoutSQLite is a SQLite database holding many sessions — read via a
	// dedicated reader with a rowid watermark.
	LayoutSQLite
)

// SessionStore describes one agent's native session store: where its session
// files live and how they're shaped. Pure data, like the target registry —
// adding an agent is a table edit. Roots are home-relative (~/) and expanded
// at walk time; EnvRoot names an environment variable that overrides the
// store's base directory when set.
//
// Paths are sourced from each tool's official documentation; the docs: comment
// on each entry cites it.
type SessionStore struct {
	Agent           string   // canonical target name (matches the deriver registry)
	Roots           []string // home-relative store roots, first match wins per file
	EnvRoot         string   // env var overriding the base dir ("" = none)
	EnvRootSubdir   string   // subdir under the env base that holds sessions
	Recursive       bool
	NamePrefix      string   // required filename prefix ("" = any)
	NameSuffixes    []string // accepted filename suffixes
	ExcludeSuffixes []string // rejected filename suffixes (checked first)
	Layout          StoreLayout
}

// sessionStores is the compiled-in registry of known agent session stores.
var sessionStores = []SessionStore{
	{
		Agent: "claude",
		Roots: []string{"~/.claude/projects"},
		// docs: https://docs.claude.com/en/docs/claude-code/data-usage (local
		// transcripts under ~/.claude/projects/<cwd-slug>/<session>.jsonl)
		Recursive:    true,
		NameSuffixes: []string{".jsonl"},
		Layout:       LayoutAppendLog,
	},
	{
		Agent:         "codex",
		Roots:         []string{"~/.codex/sessions", "~/.codex/archived_sessions"},
		EnvRoot:       "CODEX_HOME",
		EnvRootSubdir: "sessions",
		// docs: https://developers.openai.com/codex/cli/ ($CODEX_HOME/sessions
		// rollout-<ts>-<uuid>.jsonl under YYYY/MM/DD subdirs)
		Recursive:    true,
		NamePrefix:   "rollout-",
		NameSuffixes: []string{".jsonl"},
		Layout:       LayoutAppendLog,
	},
	{
		Agent: "copilot",
		Roots: []string{"~/.copilot/session-state"},
		// docs: https://docs.github.com/en/copilot/concepts/agents/about-copilot-cli
		// (session state under ~/.copilot/session-state/<sessionId>.jsonl)
		Recursive:    true,
		NameSuffixes: []string{".jsonl"},
		Layout:       LayoutAppendLog,
	},
	{
		Agent: "cursor",
		Roots: []string{"~/.cursor/projects"},
		// docs: https://cursor.com/docs/cli/overview (agent transcripts under
		// ~/.cursor/projects/<project>/agent-transcripts/<uuid>.jsonl)
		Recursive:    true,
		NameSuffixes: []string{".jsonl"},
		Layout:       LayoutAppendLog,
	},
	{
		Agent: "gemini",
		Roots: []string{"~/.gemini/tmp"},
		// docs: https://github.com/google-gemini/gemini-cli (session checkpoints
		// under ~/.gemini/tmp/<project-hash>/chats/session-*.json)
		Recursive:    true,
		NamePrefix:   "session-",
		NameSuffixes: []string{".json", ".jsonl"},
		Layout:       LayoutDocument,
	},
	{
		Agent: "opencode",
		Roots: []string{"~/.local/share/opencode"},
		// OpenCode v1.17+ persists sessions in opencode.db (SQLite, WAL —
		// session/message/part tables; observed live 2026-06-11), replacing
		// the storage/session file trees its earlier docs describe. Read by
		// rawtrace.IngestOpencodeDB with per-session replace semantics
		// (parts mutate in place).
		Recursive:    false,
		NameSuffixes: []string{"opencode.db"},
		Layout:       LayoutSQLite,
	},
	{
		Agent: "droid",
		Roots: []string{"~/.factory/sessions"},
		// docs: https://docs.factory.ai/cli/getting-started (session JSONL under
		// ~/.factory/sessions/<sessionId>.jsonl)
		Recursive:    true,
		NameSuffixes: []string{".jsonl"},
		Layout:       LayoutAppendLog,
	},
	{
		Agent: "pi",
		Roots: []string{"~/.pi/agent/sessions"},
		// docs: https://github.com/badlogic/pi-mono (session JSONL under
		// ~/.pi/agent/sessions/*.jsonl)
		Recursive:    true,
		NameSuffixes: []string{".jsonl"},
		Layout:       LayoutAppendLog,
	},
	{
		Agent: "hermes",
		Roots: []string{"~/.hermes/sessions"},
		// docs: https://hermes-agent.nousresearch.com/docs/developer-guide/session-storage
		// This entry covers the LEGACY per-session JSON documents; the entry
		// below covers the current SQLite store.
		EnvRoot:       "HERMES_HOME",
		EnvRootSubdir: "sessions",
		Recursive:     false,
		NamePrefix:    "session_",
		NameSuffixes:  []string{".json"},
		Layout:        LayoutDocument,
	},
	{
		Agent: "hermes",
		Roots: []string{"~/.hermes"},
		// Current Hermes versions persist sessions in ~/.hermes/state.db
		// (SQLite, WAL; sessions + messages tables — schema observed live,
		// 2026-06-11). Read by rawtrace.IngestHermesStateDB with per-session
		// message-id watermarks.
		EnvRoot:      "HERMES_HOME",
		Recursive:    false,
		NameSuffixes: []string{"state.db"},
		Layout:       LayoutSQLite,
	},
	{
		Agent: "openclaw",
		Roots: []string{"~/.openclaw/agents", "~/.clawdbot/agents"},
		// docs: https://docs.openclaw.ai/concepts/session (append-only JSONL at
		// ~/.openclaw/agents/<agentId>/sessions/<sessionId>.jsonl; trajectory and
		// lock files are not sessions)
		EnvRoot:         "OPENCLAW_STATE_DIR",
		EnvRootSubdir:   "agents",
		Recursive:       true,
		NameSuffixes:    []string{".jsonl"},
		ExcludeSuffixes: []string{".trajectory.jsonl", ".jsonl.lock"},
		Layout:          LayoutAppendLog,
	},
}

// Stores returns every known session store. Scan only processes the ones
// whose agent has a registered deriver — the rest are inert table rows that
// activate the moment their deriver ships.
func Stores() []SessionStore {
	out := make([]SessionStore, len(sessionStores))
	copy(out, sessionStores)
	return out
}

// Scannable returns the stores whose agent has a deriver registered (and so
// can pass the skill gate), optionally restricted to the given agent names.
func Scannable(agents []string) []SessionStore {
	want := map[string]bool{}
	for _, a := range agents {
		want[a] = true
	}
	var out []SessionStore
	for _, st := range sessionStores {
		if len(want) > 0 && !want[st.Agent] {
			continue
		}
		if _, ok := derive.Get(st.Agent); !ok {
			continue
		}
		out = append(out, st)
	}
	return out
}

// roots resolves the store's base directories. A set env override replaces
// the default roots entirely (a relocated store should not also scan the
// default location — and the default-value case would scan it twice).
func (st SessionStore) roots() []string {
	if st.EnvRoot != "" {
		if base := os.Getenv(st.EnvRoot); base != "" {
			return []string{filepath.Join(base, st.EnvRootSubdir)}
		}
	}
	out := make([]string, 0, len(st.Roots))
	for _, r := range st.Roots {
		out = append(out, expandHome(r))
	}
	return out
}

// matches reports whether a filename belongs to this store.
func (st SessionStore) matches(name string) bool {
	for _, suf := range st.ExcludeSuffixes {
		if strings.HasSuffix(name, suf) {
			return false
		}
	}
	if st.NamePrefix != "" && !strings.HasPrefix(name, st.NamePrefix) {
		return false
	}
	for _, suf := range st.NameSuffixes {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
