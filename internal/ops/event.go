package ops

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ActionType classifies what an event represents. The enum is the
// canonical wire value — producers and consumers freeze on these strings.
// Adding a new value here is a schema change.
type ActionType string

const (
	ActionFileRead       ActionType = "file_read"
	ActionFileWrite      ActionType = "file_write"
	ActionFileDelete     ActionType = "file_delete"
	ActionCommandExec    ActionType = "command_exec"
	ActionToolUse        ActionType = "tool_use"
	ActionNetworkRequest ActionType = "network_request"
	ActionSessionStart   ActionType = "session_start"
	ActionSessionEnd     ActionType = "session_end"
	ActionNotification   ActionType = "notification"
	ActionSubagentStart  ActionType = "subagent_start"
	ActionSubagentStop   ActionType = "subagent_stop"

	// ActionSkillInvoke is Quiver-specific: records when a skill is
	// invoked by name (as opposed to a file/tool action that happens
	// to land inside a skill's directory).
	ActionSkillInvoke ActionType = "skill_invoke"

	// ActionEvalResult is reserved for skill evaluation outcomes;
	// declared here so the enum stays a closed set.
	ActionEvalResult ActionType = "eval_result"

	ActionUnknown ActionType = "unknown"
)

// ResultStatus is the outcome of an action.
type ResultStatus string

const (
	ResultSuccess ResultStatus = "success"
	ResultError   ResultStatus = "error"
	ResultBlocked ResultStatus = "blocked"
)

// EventSchemaURL is the $schema marker embedded in every MarshalJSON
// output. The canonical wire shape is the Go type `Event` below;
// the URL points at the source file so external producers can pin
// against a specific revision.
const EventSchemaURL = "https://github.com/raks097/quiver/blob/main/internal/ops/event.go"

// Event is the canonical audit record. Serialised to JSON for transport
// (adapters → hook) and as TEXT columns in audit_events for storage.
//
// Nullable policy:
//   - Plain strings use `omitempty`; "" at the Go level → absent in JSON
//     and NULL in SQLite (via scan.go).
//   - json.RawMessage nil → absent / NULL.
//   - Pointer/time types are used only where zero-vs-unset is ambiguous.
type Event struct {
	ID               uuid.UUID `json:"id"`
	SessionID        uuid.UUID `json:"session_id"`
	AgentSessionID   string    `json:"agent_session_id,omitempty"`
	Sequence         int       `json:"sequence"`
	Timestamp        time.Time `json:"timestamp"`
	DurationMs       int64     `json:"duration_ms,omitempty"`
	AgentName        string    `json:"agent_name"`
	AgentVersion     string    `json:"agent_version,omitempty"`
	WorkingDirectory string    `json:"working_directory,omitempty"`

	// Attribution — written by the Skill Resolver stage of the funnel.
	SkillName     string `json:"skill_name"`
	SkillRegistry string `json:"skill_registry,omitempty"`
	SkillCommit   string `json:"skill_commit,omitempty"`
	SkillPath     string `json:"skill_path,omitempty"`

	ActionType   ActionType   `json:"action_type"`
	ToolName     string       `json:"tool_name,omitempty"`
	ResultStatus ResultStatus `json:"result_status"`
	ErrorMessage string       `json:"error_message,omitempty"`

	Payload      json.RawMessage `json:"payload,omitempty"`
	DiffContent  string          `json:"diff_content,omitempty"`
	RawEvent     json.RawMessage `json:"raw_event,omitempty"`
	IsSensitive  bool            `json:"is_sensitive"`
	SubagentID   string          `json:"subagent_id,omitempty"`
	SubagentType string          `json:"subagent_type,omitempty"`
}

// schemaEnvelope wraps the event to inject the $schema field at the
// top level. Using a type alias (eventAlias) avoids infinite MarshalJSON
// recursion.
type schemaEnvelope struct {
	Schema string `json:"$schema"`
	eventAlias
}

type eventAlias Event

// MarshalJSON emits the event with a $schema field for downstream
// validators and observability.
func (e Event) MarshalJSON() ([]byte, error) {
	return json.Marshal(schemaEnvelope{
		Schema:     EventSchemaURL,
		eventAlias: eventAlias(e),
	})
}

// SetPayload marshals p into Payload. Returns an error only if the
// payload cannot be serialised (rare — any JSON-serialisable type works).
func (e *Event) SetPayload(p any) error {
	if p == nil {
		e.Payload = nil
		return nil
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	e.Payload = data
	return nil
}

// DecodePayload unmarshals Payload into the provided typed struct.
// Returns ErrNoPayload if Payload is nil/empty so callers can
// distinguish absence from corruption.
func (e *Event) DecodePayload(dst any) error {
	if len(e.Payload) == 0 {
		return ErrNoPayload
	}
	return json.Unmarshal(e.Payload, dst)
}
