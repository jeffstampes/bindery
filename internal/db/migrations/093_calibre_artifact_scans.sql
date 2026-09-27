-- +migrate Up
-- Independent of provider snapshots: refreshing discovery never erases a scan.
-- These rows contain observations from files, not metadata.db identifiers.
CREATE TABLE calibre_artifact_scans (
    book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    calibre_id INTEGER NOT NULL CHECK (calibre_id > 0),
    format TEXT NOT NULL,
    file_name TEXT NOT NULL,
    file_path TEXT NOT NULL,
    sha256 TEXT NOT NULL DEFAULT '',
    size_bytes INTEGER NOT NULL DEFAULT 0,
    modified_at DATETIME NOT NULL,
    scanned_at DATETIME NOT NULL,
    method TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('scanned', 'partial', 'unsupported', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    correlation_group TEXT NOT NULL DEFAULT '',
    identifiers_json TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (book_id, calibre_id, format, file_name)
);

-- +migrate Down
DROP TABLE IF EXISTS calibre_artifact_scans;
