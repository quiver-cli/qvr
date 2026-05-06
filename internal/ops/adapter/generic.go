// Package generic is the reference SkillOps adapter. It accepts the
// canonical event JSON format verbatim from stdin and fills in any
// missing required fields with sensible defaults.
//
// External producers (CI scripts, custom wrappers, tools that already
// speak canonical JSON) use this adapter to feed `qvr _hook generic
// <hook_type>`. First-party agent-specific adapters live alongside this
// package and follow the same contract.
package generic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/ops"
)

// Adapter implements ops.Adapter as a pass-through over canonical event
// JSON. It applies these fill-in rules:
//
//   - If ID is nil → new v4 UUID.
//   - If Timestamp is zero → time.Now().UTC().
//   - If SessionID is nil but AgentSessionID is set → deterministic v5
//     UUID from agent_session_id (matches ops.NewSession).
//   - If SessionID is nil AND AgentSessionID is empty → error (we
//     cannot correlate the event).
//   - If ActionType is empty → mapHookTypeToAction(hookType) or Unknown.
//   - If ResultStatus is empty → ResultSuccess.
//   - RawEvent is set to rawData (the stdin bytes) regardless of what
//     the event body contained, so funnel consumers always have the
//     original wire payload if they need to replay or re-redact.
type Adapter struct{}

// Name satisfies ops.Adapter.
func (a *Adapter) Name() string { return "generic" }

// ParseEvent unmarshals rawData into an ops.Event, applies fill-ins,
// and returns the completed event.
func (a *Adapter) ParseEvent(_ context.Context, hookType string, rawData []byte) (*ops.Event, error) {
	if len(rawData) == 0 {
		return nil, errors.New("generic: empty stdin")
	}

	var e ops.Event
	if err := json.Unmarshal(rawData, &e); err != nil {
		return nil, fmt.Errorf("generic: unmarshal canonical event: %w", err)
	}

	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	if e.SessionID == uuid.Nil {
		if e.AgentSessionID == "" {
			return nil, errors.New("generic: event missing both session_id and agent_session_id")
		}
		e.SessionID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(e.AgentSessionID))
	}
	if e.ActionType == "" {
		e.ActionType = mapHookTypeToAction(hookType)
	}
	if e.ResultStatus == "" {
		e.ResultStatus = ops.ResultSuccess
	}

	// Preserve the original stdin bytes for audit / debugging. Privacy
	// will strip this later if the event is flagged sensitive.
	e.RawEvent = json.RawMessage(append([]byte(nil), rawData...))

	return &e, nil
}

// mapHookTypeToAction provides a best-effort translation from common
// hook-type names to canonical ActionTypes. Unknown values fall through
// to ActionUnknown — callers should set ActionType explicitly in
// canonical payloads to avoid this.
//
// Matching is case-sensitive and keyed on the PascalCase hook names that
// Claude / Cursor / Codex actually send (e.g. "PreToolUse",
// "SessionStart"). ParseEvent passes the raw hookType argument through
// unchanged, so callers must feed the same casing their agent emits or
// implement their own adapter rather than piggy-backing here.
func mapHookTypeToAction(hookType string) ops.ActionType {
	switch hookType {
	case "PreToolUse", "PostToolUse":
		return ops.ActionToolUse
	case "SessionStart":
		return ops.ActionSessionStart
	case "SessionEnd", "Stop":
		return ops.ActionSessionEnd
	case "SubagentStart":
		return ops.ActionSubagentStart
	case "SubagentStop":
		return ops.ActionSubagentStop
	case "Notification":
		return ops.ActionNotification
	}
	return ops.ActionUnknown
}

// init registers the adapter globally. Importing the package for its
// side effects (blank import in cmd/hook.go) is enough to make
// `_hook generic <type>` work.
func init() {
	ops.Register(&Adapter{})
}
