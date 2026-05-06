package ops

import (
	"time"

	"github.com/google/uuid"
)

// Session is one agent session — a contiguous period of agent activity.
// Events belong to exactly one session (via SessionID on Event). The
// counters here are maintained by the funnel as events flow through,
// so `qvr ops sessions list` can render per-session summaries without
// scanning the full events table.
//
// SkillsTouched is stored as a JSON array in SQLite (see the migrations
// under internal/ops/store). The slice is de-duplicated via
// AddSkillTouched.
type Session struct {
	ID               uuid.UUID  `json:"id"`
	AgentSessionID   string     `json:"agent_session_id,omitempty"`
	AgentName        string     `json:"agent_name"`
	StartedAt        time.Time  `json:"started_at"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
	WorkingDirectory string     `json:"working_directory,omitempty"`
	ProjectName      string     `json:"project_name,omitempty"`

	TotalActions     int `json:"total_actions"`
	FilesRead        int `json:"files_read"`
	FilesWritten     int `json:"files_written"`
	CommandsExecuted int `json:"commands_executed"`
	Errors           int `json:"errors"`
	SensitiveActions int `json:"sensitive_actions"`
	BlockedActions   int `json:"blocked_actions"`

	SkillsTouched []string `json:"skills_touched,omitempty"`

	// EndReason is the free-form tag End() was called with (e.g.
	// "user-exit", "session_end-event"). Empty for sessions still open
	// or ended before the field was introduced.
	EndReason string `json:"end_reason,omitempty"`
}

// NewSession constructs a Session with deterministic ID derived from
// the agent-session correlation key. Using NameSpaceOID means repeated
// calls with the same AgentSessionID produce the same UUID — events
// from a multi-hook stream correlate without an out-of-band handshake.
func NewSession(agentName, agentSessionID string, startedAt time.Time) *Session {
	var id uuid.UUID
	if agentSessionID != "" {
		id = uuid.NewSHA1(uuid.NameSpaceOID, []byte(agentSessionID))
	} else {
		id = uuid.New()
	}
	return &Session{
		ID:             id,
		AgentSessionID: agentSessionID,
		AgentName:      agentName,
		StartedAt:      startedAt,
	}
}

// RecordEvent folds an event into the session's running counters.
// Called by the funnel after privacy (so IsSensitive reflects the
// post-redaction state).
func (s *Session) RecordEvent(e *Event) {
	if s == nil || e == nil {
		return
	}
	s.TotalActions++
	if e.IsSensitive {
		s.SensitiveActions++
	}
	if e.ResultStatus == ResultError {
		s.Errors++
	}
	if e.ResultStatus == ResultBlocked {
		s.BlockedActions++
	}
	switch e.ActionType {
	case ActionFileRead:
		s.FilesRead++
	case ActionFileWrite:
		s.FilesWritten++
	case ActionCommandExec:
		s.CommandsExecuted++
	}
	if e.SkillName != "" {
		s.AddSkillTouched(e.SkillName)
	}
}

// AddSkillTouched appends name if not already present. O(N) scan is
// fine — sessions touch a handful of skills, not thousands.
func (s *Session) AddSkillTouched(name string) {
	if s == nil || name == "" {
		return
	}
	for _, existing := range s.SkillsTouched {
		if existing == name {
			return
		}
	}
	s.SkillsTouched = append(s.SkillsTouched, name)
}

// End marks the session as finished and records why.
func (s *Session) End(at time.Time, reason string) {
	if s == nil {
		return
	}
	end := at
	s.EndedAt = &end
	s.EndReason = reason
}

// Duration reports the session's active span. Returns zero for an
// open session (EndedAt nil).
func (s *Session) Duration() time.Duration {
	if s == nil || s.EndedAt == nil {
		return 0
	}
	return s.EndedAt.Sub(s.StartedAt)
}

// SkillVersion is written to skill_versions the first time a (registry,
// name, commit) triple is seen. Purely informational — lets 3b's
// lineage view attach extra metadata (branch, content hash) to the
// attribution triple without widening the Event row.
type SkillVersion struct {
	Registry    string    `json:"registry"`
	Name        string    `json:"name"`
	Commit      string    `json:"commit"`
	Branch      string    `json:"branch,omitempty"`
	ContentHash string    `json:"content_hash,omitempty"`
	FirstSeenAt time.Time `json:"first_seen_at"`
}
