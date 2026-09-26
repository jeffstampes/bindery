-- +migrate Up
-- Advisory comparison results only: Calibre/CWA remains the authority for
-- owned metadata. No Calibre catalogue or authoritative metadata is mirrored.
CREATE TABLE calibre_metadata_audit_findings (
    id                     INTEGER  PRIMARY KEY AUTOINCREMENT,
    book_id                INTEGER  NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    calibre_id             INTEGER  NOT NULL CHECK (calibre_id > 0),
    field                  TEXT     NOT NULL CHECK (field IN ('identifiers', 'title', 'authors', 'series_membership', 'series_position', 'language', 'publication_date')),
    evidence_key           TEXT     NOT NULL DEFAULT '',
    finding_type           TEXT     NOT NULL CHECK (finding_type IN ('identifier_missing', 'identifier_conflict', 'title_difference', 'author_difference', 'series_membership_difference', 'series_position_difference', 'language_difference', 'publication_date_difference')),
    assessment             TEXT     NOT NULL CHECK (assessment IN ('needs_review', 'ambiguous')),
    calibre_evidence_json  TEXT     NOT NULL DEFAULT '[]',
    bindery_evidence_json  TEXT     NOT NULL DEFAULT '[]',
    match_method           TEXT     NOT NULL DEFAULT '',
    match_confidence       TEXT     NOT NULL DEFAULT '',
    reason                 TEXT     NOT NULL DEFAULT '',
    comparison_fingerprint TEXT     NOT NULL DEFAULT '',
    ignored_fingerprint    TEXT     NOT NULL DEFAULT '',
    state                  TEXT     NOT NULL DEFAULT 'unresolved' CHECK (state IN ('unresolved', 'ignored', 'resolved', 'unmatched')),
    created_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (book_id, field, evidence_key)
);

CREATE INDEX idx_calibre_audit_state ON calibre_metadata_audit_findings(state, updated_at DESC);

-- +migrate Down
DROP INDEX IF EXISTS idx_calibre_audit_state;
DROP TABLE IF EXISTS calibre_metadata_audit_findings;
