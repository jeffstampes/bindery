-- +migrate Up
-- Write attempts are retained in Bindery, never in Calibre's curated catalogue.
-- Pending means the process stopped before the outcome could be recorded; it
-- must not be presented as a successful Calibre write.
CREATE TABLE calibre_identifier_attempts (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    finding_id             INTEGER NOT NULL,
    actor_user_id          INTEGER NOT NULL DEFAULT 0,
    book_id                INTEGER NOT NULL,
    calibre_id             INTEGER NOT NULL,
    identifier_type        TEXT NOT NULL,
    old_value              TEXT NOT NULL DEFAULT '',
    proposed_value         TEXT NOT NULL,
    evidence_keys_json     TEXT NOT NULL DEFAULT '[]',
    action                 TEXT NOT NULL CHECK (action IN ('add', 'replace', 'remove')),
    comparison_fingerprint TEXT NOT NULL,
    outcome                TEXT NOT NULL CHECK (outcome IN ('pending', 'applied', 'failed', 'rejected')),
    error                  TEXT NOT NULL DEFAULT '',
    created_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at            DATETIME
);
CREATE INDEX idx_calibre_identifier_attempts_finding ON calibre_identifier_attempts(finding_id, id DESC);

-- +migrate Down
DROP INDEX IF EXISTS idx_calibre_identifier_attempts_finding;
DROP TABLE IF EXISTS calibre_identifier_attempts;