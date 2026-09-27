-- +migrate Up
-- Human review actions are append-only; audit refreshes never rewrite decisions.
CREATE TABLE calibre_audit_decisions (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    finding_id             INTEGER NOT NULL REFERENCES calibre_metadata_audit_findings(id) ON DELETE CASCADE,
    action                 TEXT NOT NULL CHECK (action IN ('ignore', 'reopen')),
    comparison_fingerprint TEXT NOT NULL,
    created_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_calibre_audit_decisions_finding ON calibre_audit_decisions(finding_id, id);

-- Preserve the known decision on pre-upgrade ignored findings. Older actions
-- cannot be reconstructed from the finding's current state.
INSERT INTO calibre_audit_decisions (finding_id, action, comparison_fingerprint, created_at)
SELECT id, 'ignore', ignored_fingerprint, updated_at
FROM calibre_metadata_audit_findings WHERE ignored_fingerprint <> '';

-- +migrate Down
DROP INDEX IF EXISTS idx_calibre_audit_decisions_finding;
DROP TABLE IF EXISTS calibre_audit_decisions;
