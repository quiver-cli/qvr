-- 0003: optional indexes for the skill-metrics aggregations (metrics.go).
--
-- Only write-mode opens apply migrations (capture / rederive / ingest); the
-- read-only UI open skips them entirely. The metrics queries therefore NEVER
-- depend on these indexes existing — they only cheapen the GROUP BYs on
-- machines where a write-mode consumer has run since this migration landed.

-- SKILL spans grouped/filtered by skill.name (every metrics aggregation).
CREATE INDEX IF NOT EXISTS idx_spans_skill_name
  ON spans(json_extract(attributes, '$."skill.name"'))
  WHERE kind = 'SKILL';

-- Working-directory scoping resolves sessions through raw_traces.
CREATE INDEX IF NOT EXISTS idx_raw_wd_session
  ON raw_traces(working_directory, session_id);
