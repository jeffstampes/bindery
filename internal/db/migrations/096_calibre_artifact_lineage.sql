-- +migrate Up
-- An operator may explicitly attest that an observation came from an original
-- file before any known metadata write-back; absent attestation stays unknown.
ALTER TABLE calibre_artifact_scans ADD COLUMN attested_original INTEGER NOT NULL DEFAULT 0;
-- Operator-reported Calibre-to-file metadata write-backs are lineage facts,
-- not proof of what Calibre actually wrote. A scan without such a fact is unknown.
CREATE TABLE calibre_artifact_writebacks (
    id INTEGER PRIMARY KEY,
    book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    calibre_id INTEGER NOT NULL CHECK (calibre_id > 0),
    written_at DATETIME NOT NULL,
    recorded_at DATETIME NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('polish_books', 'calibre_to_file'))
);
CREATE INDEX idx_calibre_artifact_writebacks_owner ON calibre_artifact_writebacks(book_id, calibre_id, written_at);

-- Superseded scans are not discarded when a file is rescanned. A historical
-- observation can be assessed against the write-back timeline independently.
CREATE TABLE calibre_artifact_scan_history (
    id INTEGER PRIMARY KEY,
    book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    calibre_id INTEGER NOT NULL,
    format TEXT NOT NULL,
    file_name TEXT NOT NULL,
    file_path TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    modified_at DATETIME NOT NULL,
    scanned_at DATETIME NOT NULL,
    method TEXT NOT NULL,
    outcome TEXT NOT NULL,
    error TEXT NOT NULL,
    correlation_group TEXT NOT NULL,
    identifiers_json TEXT NOT NULL,
    attested_original INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_calibre_artifact_history_owner ON calibre_artifact_scan_history(book_id, calibre_id, scanned_at);

-- +migrate Down
DROP INDEX IF EXISTS idx_calibre_artifact_history_owner;
DROP TABLE IF EXISTS calibre_artifact_scan_history;
DROP INDEX IF EXISTS idx_calibre_artifact_writebacks_owner;
DROP TABLE IF EXISTS calibre_artifact_writebacks;
ALTER TABLE calibre_artifact_scans DROP COLUMN attested_original;
