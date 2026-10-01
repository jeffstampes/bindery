-- +migrate Up
-- Proposals are frozen, one-book review records in Bindery, not catalogue copies.
-- OPF input is retained here for recovery; neither table changes Calibre itself.
CREATE TABLE calibre_metadata_refresh_proposals (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    book_id INTEGER NOT NULL,
    calibre_id INTEGER NOT NULL,
    fingerprint TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('ready', 'no_result', 'lookup_failed', 'ineligible')),
    proposal_json TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_calibre_metadata_refresh_book ON calibre_metadata_refresh_proposals(book_id, id DESC);
CREATE TABLE calibre_metadata_refresh_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    proposal_id INTEGER NOT NULL,
    actor_user_id INTEGER NOT NULL DEFAULT 0,
    book_id INTEGER NOT NULL,
    calibre_id INTEGER NOT NULL,
    action TEXT NOT NULL DEFAULT 'apply_fields',
    pre_write_opf TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL CHECK (outcome IN ('pending', 'applied', 'rejected', 'partial', 'failed', 'verification_failed')),
    error TEXT NOT NULL DEFAULT '',
    verification TEXT NOT NULL DEFAULT '',
    started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at DATETIME
);
CREATE INDEX idx_calibre_metadata_refresh_attempt ON calibre_metadata_refresh_attempts(book_id, id DESC);
CREATE UNIQUE INDEX idx_calibre_metadata_refresh_one_attempt ON calibre_metadata_refresh_attempts(proposal_id);
-- +migrate Down
DROP INDEX IF EXISTS idx_calibre_metadata_refresh_one_attempt;
DROP INDEX IF EXISTS idx_calibre_metadata_refresh_attempt;
DROP TABLE IF EXISTS calibre_metadata_refresh_attempts;
DROP INDEX IF EXISTS idx_calibre_metadata_refresh_book;
DROP TABLE IF EXISTS calibre_metadata_refresh_proposals;
