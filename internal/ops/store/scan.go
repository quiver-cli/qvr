package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/ops"
)

// eventColumns is the single source of truth for audit_events row
// layout. Every SELECT uses this string; every scan matches the order.
const eventColumns = `
  id, session_id, agent_session_id, sequence, timestamp, duration_ms,
  agent_name, agent_version, working_directory,
  skill_name, skill_registry, skill_commit, skill_path,
  action_type, tool_name, result_status, error_message,
  payload, diff_content, raw_event, is_sensitive,
  subagent_id, subagent_type
`

// sessionColumns mirrors the sessions row layout.
const sessionColumns = `
  id, agent_session_id, agent_name, started_at, ended_at,
  working_directory, project_name,
  total_actions, files_read, files_written, commands_executed,
  errors, sensitive_actions, blocked_actions, skills_touched
`

// scannable is satisfied by both *sql.Row and *sql.Rows.
type scannable interface {
	Scan(dest ...any) error
}

// scanEvent reads one audit_events row. The column order MUST match
// eventColumns exactly.
func scanEvent(row scannable) (*ops.Event, error) {
	var (
		e                 ops.Event
		idStr, sessionStr string
		agentSessionID    sql.NullString
		agentVersion      sql.NullString
		workingDir        sql.NullString
		skillRegistry     sql.NullString
		skillCommit       sql.NullString
		skillPath         sql.NullString
		toolName          sql.NullString
		errorMessage      sql.NullString
		payload           sql.NullString
		diffContent       sql.NullString
		rawEvent          sql.NullString
		isSensitiveInt    int
		subagentID        sql.NullString
		subagentType      sql.NullString
	)
	if err := row.Scan(
		&idStr, &sessionStr, &agentSessionID, &e.Sequence, &e.Timestamp, &e.DurationMs,
		&e.AgentName, &agentVersion, &workingDir,
		&e.SkillName, &skillRegistry, &skillCommit, &skillPath,
		&e.ActionType, &toolName, &e.ResultStatus, &errorMessage,
		&payload, &diffContent, &rawEvent, &isSensitiveInt,
		&subagentID, &subagentType,
	); err != nil {
		return nil, err
	}

	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil, err
	}
	sid, err := uuid.Parse(sessionStr)
	if err != nil {
		return nil, err
	}
	e.ID = id
	e.SessionID = sid
	e.AgentSessionID = agentSessionID.String
	e.AgentVersion = agentVersion.String
	e.WorkingDirectory = workingDir.String
	e.SkillRegistry = skillRegistry.String
	e.SkillCommit = skillCommit.String
	e.SkillPath = skillPath.String
	e.ToolName = toolName.String
	e.ErrorMessage = errorMessage.String
	if payload.Valid && payload.String != "" {
		e.Payload = json.RawMessage(payload.String)
	}
	e.DiffContent = diffContent.String
	if rawEvent.Valid && rawEvent.String != "" {
		e.RawEvent = json.RawMessage(rawEvent.String)
	}
	e.IsSensitive = isSensitiveInt != 0
	e.SubagentID = subagentID.String
	e.SubagentType = subagentType.String
	// Normalise time zone to UTC. SQLite stores DATETIME without TZ
	// info; inputs went in as UTC so outputs read back as UTC.
	e.Timestamp = e.Timestamp.UTC()
	return &e, nil
}

// scanSession reads one sessions row. Column order must match sessionColumns.
func scanSession(row scannable) (*ops.Session, error) {
	var (
		s              ops.Session
		idStr          string
		agentSessionID sql.NullString
		endedAt        sql.NullTime
		workingDir     sql.NullString
		projectName    sql.NullString
		skillsTouched  sql.NullString
	)
	if err := row.Scan(
		&idStr, &agentSessionID, &s.AgentName, &s.StartedAt, &endedAt,
		&workingDir, &projectName,
		&s.TotalActions, &s.FilesRead, &s.FilesWritten, &s.CommandsExecuted,
		&s.Errors, &s.SensitiveActions, &s.BlockedActions, &skillsTouched,
	); err != nil {
		return nil, err
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil, err
	}
	s.ID = id
	s.AgentSessionID = agentSessionID.String
	if endedAt.Valid {
		end := endedAt.Time.UTC()
		s.EndedAt = &end
	}
	s.WorkingDirectory = workingDir.String
	s.ProjectName = projectName.String
	s.StartedAt = s.StartedAt.UTC()
	if skillsTouched.Valid && skillsTouched.String != "" {
		if err := json.Unmarshal([]byte(skillsTouched.String), &s.SkillsTouched); err != nil {
			return nil, fmt.Errorf("decode skills_touched: %w", err)
		}
	}
	return &s, nil
}

// nullableString returns a sql.NullString that is .Valid when s != "".
func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// nullableJSON returns a sql.NullString holding the provided raw JSON;
// nil or empty → NULL.
func nullableJSON(raw json.RawMessage) sql.NullString {
	if len(raw) == 0 {
		return sql.NullString{}
	}
	return sql.NullString{String: string(raw), Valid: true}
}

// nullableTime returns a sql.NullTime.
func nullableTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t.UTC(), Valid: true}
}

// boolToInt returns 1 for true, 0 for false — SQLite has no real bool.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// encodeSkillsTouched returns a JSON array string for the sessions row,
// or NULL when the slice is empty.
func encodeSkillsTouched(skills []string) sql.NullString {
	if len(skills) == 0 {
		return sql.NullString{}
	}
	b, err := json.Marshal(skills)
	if err != nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(b), Valid: true}
}
