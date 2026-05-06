package ops

import "context"

// Adapter translates an agent-specific hook invocation (hook type +
// raw payload bytes from stdin) into a canonical *Event. Adapters
// live outside this package in subpackages under internal/ops/adapter/;
// see adapter/generic for the reference implementation. Per-agent
// adapters (Claude Code, Cursor, Codex, Windsurf, Copilot) plug in
// alongside the `qvr ops install-hooks` tooling.
type Adapter interface {
	// Name returns the string used to dispatch the adapter from
	// `qvr _hook <name> <hook_type>`.
	Name() string

	// ParseEvent converts a single hook-invocation payload into a
	// canonical Event. It does not touch the store, the resolver, or
	// the privacy checker — those are the funnel's responsibilities.
	//
	// rawData is the JSON stdin blob exactly as the hook emitted it.
	// hookType is the agent-specific hook identifier ("PostToolUse",
	// "PreCompact", etc.) — adapters interpret it to decide the
	// canonical ActionType.
	ParseEvent(ctx context.Context, hookType string, rawData []byte) (*Event, error)
}
