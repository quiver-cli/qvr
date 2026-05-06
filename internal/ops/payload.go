package ops

// Typed payloads attached to events. The Payload field on Event is a
// json.RawMessage; concrete payload types round-trip through SetPayload
// and DecodePayload. Keeping the typed shapes here gives adapters a
// reference for the canonical on-wire format.
//
// Adding a field is backward-compatible (old consumers ignore it).
// Changing a field name is NOT — the wire format is frozen by the JSON
// tags.

// FileReadPayload — ActionFileRead. Path is the file the agent read.
// Pattern is set when the read happened via a glob (e.g. Grep), in
// which case Path may be empty.
type FileReadPayload struct {
	Path           string `json:"path,omitempty"`
	Pattern        string `json:"pattern,omitempty"`
	Lines          int    `json:"lines,omitempty"`
	Bytes          int64  `json:"bytes,omitempty"`
	ContentPreview string `json:"content_preview,omitempty"`
}

// FileWritePayload — ActionFileWrite. Covers both create and update.
// NewString/OldString are populated on edit; ContentPreview on create.
// All three are content-bearing and are zeroed by StripContent.
type FileWritePayload struct {
	Path           string `json:"path,omitempty"`
	OldString      string `json:"old_string,omitempty"`
	NewString      string `json:"new_string,omitempty"`
	ContentPreview string `json:"content_preview,omitempty"`
	Bytes          int64  `json:"bytes,omitempty"`
	Created        bool   `json:"created,omitempty"`
}

// FileDeletePayload — ActionFileDelete. No content bearing fields.
type FileDeletePayload struct {
	Path string `json:"path,omitempty"`
}

// CommandExecPayload — ActionCommandExec. Stdout/Stderr are
// content-bearing; Command and Cwd are metadata and survive stripping.
//
// ExitCode is emitted unconditionally (no omitempty) so a successful
// zero exit is distinguishable from an absent/unknown value.
type CommandExecPayload struct {
	Command  string `json:"command,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Duration int64  `json:"duration_ms,omitempty"`
}

// ToolUsePayload — ActionToolUse. Input is a decoded JSON object
// (string keys → any value); Output is the tool's string return. If
// you need byte-identical round-tripping use the envelope Event.Payload
// directly — this struct loses JSON formatting by design.
type ToolUsePayload struct {
	Input  map[string]any `json:"input,omitempty"`
	Output string         `json:"output,omitempty"`
}

// NetworkRequestPayload — ActionNetworkRequest. Body is content-bearing.
type NetworkRequestPayload struct {
	URL        string `json:"url,omitempty"`
	Method     string `json:"method,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	Body       string `json:"body,omitempty"`
}

// SessionPayload — ActionSessionStart and ActionSessionEnd. Reason is
// set on end to capture why the session terminated.
type SessionPayload struct {
	ProjectName string `json:"project_name,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// NotificationPayload — ActionNotification. Free-form text surface for
// agent-emitted signals (e.g. "user confirmed destructive action").
type NotificationPayload struct {
	Title   string `json:"title,omitempty"`
	Message string `json:"message,omitempty"`
}

// SubagentPayload — ActionSubagentStart/Stop. Captures spawn/exit of a
// sub-agent inside a parent session.
type SubagentPayload struct {
	Type   string `json:"type,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

// SkillInvokePayload — ActionSkillInvoke. Quiver-only. Origin is the
// caller that recorded the invocation (e.g. "qvr-install", "claude").
type SkillInvokePayload struct {
	Origin string `json:"origin,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Reason string `json:"reason,omitempty"`
}
