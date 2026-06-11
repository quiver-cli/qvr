-- 0004: the unified session read model + the discovery scan ledger.
--
-- session_meta is the one read model every consumer (UI, CLI, metrics) queries
-- for session listings. Like spans it is a PROJECTION: each agent's deriver
-- constructs it from the verbatim raw_traces rows, so it is regenerable at
-- will (`qvr audit rederive`) and carries deriver_version for parity across
-- deriver revisions. Raw stays the canonical source of truth.
--
-- Idempotent via IF NOT EXISTS, matching prior migrations.

CREATE TABLE IF NOT EXISTS session_meta (
  session_id        TEXT PRIMARY KEY,            -- canonical uuid (same key as raw_traces)
  agent_name        TEXT NOT NULL,               -- canonical target name
  source_session_id TEXT,                        -- the agent's own session id, verbatim
  source_path       TEXT,                        -- native store file the session came from
  working_directory TEXT,                        -- cwd (project scoping)
  git_branch        TEXT,
  model             TEXT,                        -- LLM model
  title             TEXT,                        -- first real user prompt
  started_ms        INTEGER NOT NULL DEFAULT 0,  -- epoch milliseconds
  ended_ms          INTEGER NOT NULL DEFAULT 0,
  turns             INTEGER NOT NULL DEFAULT 0,  -- LLM span count
  tools             INTEGER NOT NULL DEFAULT 0,  -- TOOL span count
  skills            TEXT,                        -- JSON array of distinct skill names, first-use order
  deriver_version   INTEGER NOT NULL DEFAULT 1,
  derived_at        DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_session_meta_agent   ON session_meta(agent_name);
CREATE INDEX IF NOT EXISTS idx_session_meta_wd      ON session_meta(working_directory);
CREATE INDEX IF NOT EXISTS idx_session_meta_started ON session_meta(started_ms DESC);

-- scanned_files is the discovery scan ledger: one row per native session-store
-- file qvr has looked at, keyed by file (NOT by session) so it survives
-- DeleteSession — a skill-less file that was skipped must not be re-derived on
-- every scan. (size, mtime_ms) is the stat-diff gate; a file that grows or
-- changes fails the gate and is re-examined from its cursor.
CREATE TABLE IF NOT EXISTS scanned_files (
  agent_name  TEXT NOT NULL,
  source_path TEXT NOT NULL,
  size        INTEGER NOT NULL,
  mtime_ms    INTEGER NOT NULL,
  status      TEXT NOT NULL,        -- 'ingested' | 'skipped_no_skill' | 'error'
  session_id  TEXT,                 -- correlated session (NULL when skipped pre-persist)
  scanned_at  DATETIME NOT NULL,
  PRIMARY KEY (agent_name, source_path)
);

-- Agents are now keyed by canonical target name everywhere. Rename rows
-- written by earlier versions under the "claude-code" alias, so the first
-- discover scan after upgrading finds the existing cursors (and does not
-- re-tail every transcript from offset 0 into duplicate rows).
UPDATE raw_traces   SET agent_name = 'claude' WHERE agent_name = 'claude-code';
UPDATE spans        SET agent_name = 'claude' WHERE agent_name = 'claude-code';
UPDATE trace_cursors SET agent_name = 'claude'
  WHERE agent_name = 'claude-code'
    AND NOT EXISTS (SELECT 1 FROM trace_cursors t2
                    WHERE t2.agent_name = 'claude'
                      AND t2.source_path = trace_cursors.source_path);
