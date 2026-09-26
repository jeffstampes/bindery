-- +migrate Up
-- Discovery/evidence snapshots in Bindery only. No curated Calibre metadata
-- is mirrored into Bindery books or written back to metadata.db.
CREATE TABLE calibre_identity_snapshots (
    book_id    INTEGER PRIMARY KEY REFERENCES books(id) ON DELETE CASCADE,
    calibre_id INTEGER NOT NULL CHECK (calibre_id > 0),
    root_key   TEXT NOT NULL DEFAULT '',
    checked_at DATETIME NOT NULL
);

CREATE TABLE calibre_identity_evidence (
    book_id                     INTEGER NOT NULL REFERENCES calibre_identity_snapshots(book_id) ON DELETE CASCADE,
    evidence_key                TEXT NOT NULL CHECK (evidence_key <> ''),
    canonical_identity          TEXT NOT NULL DEFAULT '',
    provider                    TEXT NOT NULL,
    foreign_id                  TEXT NOT NULL DEFAULT '',
    edition_id                  TEXT NOT NULL DEFAULT '',
    method                      TEXT NOT NULL DEFAULT '',
    seed                        TEXT NOT NULL DEFAULT '',
    provenance_group            TEXT NOT NULL DEFAULT '',
    status                      TEXT NOT NULL CHECK (status IN ('root', 'corroborated', 'candidate', 'conflict')),
    work_confidence             TEXT NOT NULL DEFAULT '',
    edition_confidence          TEXT NOT NULL DEFAULT '',
    normalized_identifiers_json TEXT NOT NULL DEFAULT '{}',
    provider_metadata_json      TEXT NOT NULL DEFAULT '{}',
    checked_at                  DATETIME NOT NULL,
    PRIMARY KEY (book_id, evidence_key)
);
CREATE INDEX idx_calibre_identity_evidence_canonical ON calibre_identity_evidence(canonical_identity);

CREATE TABLE calibre_identity_lookups (
    book_id    INTEGER NOT NULL REFERENCES calibre_identity_snapshots(book_id) ON DELETE CASCADE,
    provider   TEXT NOT NULL,
    method     TEXT NOT NULL,
    seed       TEXT NOT NULL,
    outcome    TEXT NOT NULL CHECK (outcome IN ('answered', 'empty', 'failed', 'unconfigured', 'truncated', 'not_attempted')),
    error      TEXT NOT NULL DEFAULT '',
    checked_at DATETIME NOT NULL,
    PRIMARY KEY (book_id, provider, method, seed)
);

CREATE TABLE calibre_identity_claims (
    book_id          INTEGER NOT NULL REFERENCES calibre_identity_snapshots(book_id) ON DELETE CASCADE,
    identifier_type  TEXT NOT NULL,
    identifier_value TEXT NOT NULL,
    status           TEXT NOT NULL CHECK (status IN ('agrees', 'conflicts', 'unverified')),
    evidence_key     TEXT,
    checked_at       DATETIME NOT NULL,
    PRIMARY KEY (book_id, identifier_type, identifier_value),
    FOREIGN KEY (book_id, evidence_key) REFERENCES calibre_identity_evidence(book_id, evidence_key)
);

-- +migrate Down
DROP TABLE IF EXISTS calibre_identity_claims;
DROP TABLE IF EXISTS calibre_identity_lookups;
DROP INDEX IF EXISTS idx_calibre_identity_evidence_canonical;
DROP TABLE IF EXISTS calibre_identity_evidence;
DROP TABLE IF EXISTS calibre_identity_snapshots;
