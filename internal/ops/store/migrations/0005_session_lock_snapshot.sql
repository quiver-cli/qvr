-- Session lock snapshot: the skill identity that was PROVEN at first ingest,
-- frozen per (session, skill). Agents that record symlink paths (claude's
-- "Base directory for this skill: <agent-dir>") can only be resolved against
-- the filesystem as it is at derive time — so a later rederive (back-fill,
-- deriver upgrade) after a `qvr switch` would re-resolve the symlink to the
-- NEW target and rewrite history. Ingest runs near-live, when the resolution
-- is correct; this table freezes that result so rederive replays the truth
-- instead of re-deriving it from a moved symlink. Write-once by design
-- (INSERT OR IGNORE): the first ingest's proof wins.
CREATE TABLE IF NOT EXISTS session_lock_snapshot (
    session_id   TEXT    NOT NULL,
    skill_name   TEXT    NOT NULL,
    registry     TEXT    NOT NULL DEFAULT '',
    ref          TEXT    NOT NULL DEFAULT '',
    commit_sha   TEXT    NOT NULL DEFAULT '',
    subtree_hash TEXT    NOT NULL DEFAULT '',
    source       TEXT    NOT NULL DEFAULT '',
    canonical    TEXT    NOT NULL DEFAULT '',
    snapped_at   INTEGER NOT NULL,
    PRIMARY KEY (session_id, skill_name)
);
