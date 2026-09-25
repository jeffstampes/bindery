-- +migrate Up

-- Calibre work cross-references: stores links between Bindery works and Calibre book IDs
-- for authoritative library mode (#4).

CREATE TABLE calibre_work_cross_references (
    id                  INTEGER  PRIMARY KEY AUTOINCREMENT,
    book_id             INTEGER  NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    calibre_id          INTEGER  NOT NULL,
    match_method        TEXT     NOT NULL,
    confidence          TEXT     NOT NULL CHECK(confidence IN ('exact', 'high', 'medium', 'low', 'ambiguous')),
    status              TEXT     NOT NULL DEFAULT 'matched' CHECK(status IN ('matched', 'ambiguous', 'stale', 'ignored')),
    calibre_fingerprint TEXT     NOT NULL DEFAULT '',
    match_details_json  TEXT     NOT NULL DEFAULT '{}',
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (book_id)
);

CREATE INDEX idx_calibre_cross_ref_calibre_id ON calibre_work_cross_references(calibre_id);
CREATE INDEX idx_calibre_cross_ref_status ON calibre_work_cross_references(status);

-- +migrate Down

DROP INDEX IF EXISTS idx_calibre_cross_ref_status;
DROP INDEX IF EXISTS idx_calibre_cross_ref_calibre_id;
DROP TABLE IF EXISTS calibre_work_cross_references;
