-- 0001_init.sql
-- Initial SkillOps schema. Creates audit_events, sessions, skill_versions,
-- self_audits, and schema_migrations. Idempotent via IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS sessions (
  id                   TEXT PRIMARY KEY,
  agent_session_id     TEXT,
  agent_name           TEXT NOT NULL,
  started_at           DATETIME NOT NULL,
  ended_at             DATETIME,
  working_directory    TEXT,
  project_name         TEXT,
  total_actions        INTEGER NOT NULL DEFAULT 0,
  files_read           INTEGER NOT NULL DEFAULT 0,
  files_written        INTEGER NOT NULL DEFAULT 0,
  commands_executed    INTEGER NOT NULL DEFAULT 0,
  errors               INTEGER NOT NULL DEFAULT 0,
  sensitive_actions    INTEGER NOT NULL DEFAULT 0,
  blocked_actions      INTEGER NOT NULL DEFAULT 0,
  skills_touched       TEXT                          -- JSON array
);

CREATE INDEX IF NOT EXISTS idx_sessions_started ON sessions(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_agent   ON sessions(agent_name);

CREATE TABLE IF NOT EXISTS audit_events (
  id                 TEXT PRIMARY KEY,
  session_id         TEXT NOT NULL REFERENCES sessions(id),
  agent_session_id   TEXT,
  sequence           INTEGER NOT NULL,
  timestamp          DATETIME NOT NULL,
  duration_ms        INTEGER NOT NULL DEFAULT 0,
  agent_name         TEXT NOT NULL,
  agent_version      TEXT,
  working_directory  TEXT,
  skill_name         TEXT NOT NULL,
  skill_registry     TEXT,
  skill_commit       TEXT,
  skill_path         TEXT,
  action_type        TEXT NOT NULL,
  tool_name          TEXT,
  result_status      TEXT NOT NULL,
  error_message      TEXT,
  payload            TEXT,                           -- JSON
  diff_content       TEXT,
  raw_event          TEXT,                           -- JSON
  is_sensitive       INTEGER NOT NULL DEFAULT 0,
  subagent_id        TEXT,
  subagent_type      TEXT
);

CREATE INDEX IF NOT EXISTS idx_events_skill_ts    ON audit_events(skill_name, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_events_action      ON audit_events(action_type);
CREATE INDEX IF NOT EXISTS idx_events_sensitive   ON audit_events(is_sensitive) WHERE is_sensitive = 1;
CREATE INDEX IF NOT EXISTS idx_events_session_seq ON audit_events(session_id, sequence);
CREATE INDEX IF NOT EXISTS idx_events_ts          ON audit_events(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_events_agent_ts    ON audit_events(agent_name, timestamp DESC);

CREATE TABLE IF NOT EXISTS skill_versions (
  registry       TEXT NOT NULL,
  name           TEXT NOT NULL,
  commit_sha     TEXT NOT NULL,
  branch         TEXT,
  content_hash   TEXT,
  first_seen_at  DATETIME NOT NULL,
  PRIMARY KEY (registry, name, commit_sha)
);

CREATE TABLE IF NOT EXISTS self_audits (
  id          TEXT PRIMARY KEY,
  timestamp   DATETIME NOT NULL,
  action      TEXT NOT NULL,
  actor       TEXT,
  result      TEXT NOT NULL,
  error_msg   TEXT,
  details     TEXT                                    -- JSON
);

CREATE INDEX IF NOT EXISTS idx_self_audits_ts     ON self_audits(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_self_audits_action ON self_audits(action);
