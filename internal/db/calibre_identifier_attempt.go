package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// GetByID reads one persisted comparison rather than trusting a browser copy.
func (r *CalibreAuditRepo) GetByID(ctx context.Context, id int64) (*models.CalibreAuditFinding, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, book_id, calibre_id, field, evidence_key, finding_type, assessment,
		calibre_evidence_json, bindery_evidence_json, match_method, match_confidence, reason,
		comparison_fingerprint, ignored_fingerprint, state, created_at, updated_at
		FROM calibre_metadata_audit_findings WHERE id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("read identifier finding: %w", err)
	}
	defer rows.Close()
	findings, err := scanCalibreAuditFindings(rows, false)
	if err != nil {
		return nil, err
	}
	if len(findings) == 0 {
		return nil, nil
	}
	return &findings[0], nil
}

// CalibreIdentifierAttempt describes an explicit human write attempt. Pending
// is not success; a crash between the Calibre commit and Bindery outcome update
// leaves an inspectable pending row for manual verification.
type CalibreIdentifierAttempt struct {
	ID                    int64      `json:"id"`
	FindingID             int64      `json:"findingId"`
	ActorUserID           int64      `json:"actorUserId"`
	BookID                int64      `json:"bookId"`
	CalibreID             int64      `json:"calibreId"`
	IdentifierType        string     `json:"identifierType"`
	OldValue              string     `json:"oldValue"`
	ProposedValue         string     `json:"proposedValue"`
	EvidenceKeys          []string   `json:"evidenceKeys"`
	Action                string     `json:"action"`
	ComparisonFingerprint string     `json:"comparisonFingerprint"`
	Outcome               string     `json:"outcome"`
	Error                 string     `json:"error,omitempty"`
	CreatedAt             time.Time  `json:"createdAt"`
	FinishedAt            *time.Time `json:"finishedAt,omitempty"`
}

type CalibreIdentifierAttemptRepo struct{ db *sql.DB }

func NewCalibreIdentifierAttemptRepo(database *sql.DB) *CalibreIdentifierAttemptRepo {
	return &CalibreIdentifierAttemptRepo{db: database}
}

// Begin durably records intent before opening any writable Calibre handle.
func (r *CalibreIdentifierAttemptRepo) Begin(ctx context.Context, a *CalibreIdentifierAttempt) error {
	if a == nil || a.FindingID <= 0 || a.BookID <= 0 || a.CalibreID <= 0 || a.ProposedValue == "" || a.Action != "add" {
		return fmt.Errorf("invalid Calibre identifier attempt")
	}
	keys, err := json.Marshal(a.EvidenceKeys)
	if err != nil {
		return fmt.Errorf("encode identifier evidence keys: %w", err)
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO calibre_identifier_attempts
		(finding_id, actor_user_id, book_id, calibre_id, identifier_type, old_value,
		 proposed_value, evidence_keys_json, action, comparison_fingerprint, outcome)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'add', ?, 'pending')`,
		a.FindingID, a.ActorUserID, a.BookID, a.CalibreID, a.IdentifierType, a.OldValue,
		a.ProposedValue, string(keys), a.ComparisonFingerprint)
	if err != nil {
		return fmt.Errorf("record Calibre identifier attempt: %w", err)
	}
	a.ID, err = res.LastInsertId()
	if err != nil {
		return fmt.Errorf("read Calibre identifier attempt id: %w", err)
	}
	a.Outcome = "pending"
	return nil
}

// SetEvidenceKeys pins the exact persisted provider observations used at
// approval time before the Calibre transaction begins.
func (r *CalibreIdentifierAttemptRepo) SetEvidenceKeys(ctx context.Context, id int64, keys []string) error {
	if id <= 0 || len(keys) == 0 {
		return fmt.Errorf("missing identifier attempt evidence")
	}
	data, err := json.Marshal(keys)
	if err != nil {
		return fmt.Errorf("encode identifier attempt evidence: %w", err)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE calibre_identifier_attempts SET evidence_keys_json = ?
		WHERE id = ? AND outcome = 'pending'`, string(data), id)
	if err != nil {
		return fmt.Errorf("save identifier attempt evidence: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("count identifier attempt %d updates: %w", id, err)
	}
	if rows != 1 {
		return fmt.Errorf("identifier attempt %d was not pending", id)
	}
	return nil
}

// Finish records only a real observed outcome after the external operation.
func (r *CalibreIdentifierAttemptRepo) Finish(ctx context.Context, id int64, outcome, detail string) error {
	if id <= 0 || (outcome != "applied" && outcome != "failed" && outcome != "rejected") {
		return fmt.Errorf("invalid identifier attempt outcome")
	}
	res, err := r.db.ExecContext(ctx, `UPDATE calibre_identifier_attempts
		SET outcome = ?, error = ?, finished_at = ? WHERE id = ? AND outcome = 'pending'`,
		outcome, detail, timeValueArg(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("finish Calibre identifier attempt: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("count identifier attempt %d updates: %w", id, err)
	}
	if rows != 1 {
		return fmt.Errorf("identifier attempt %d was not pending", id)
	}
	return nil
}

// ListByFinding returns recent attempts, including failed and interrupted ones.
func (r *CalibreIdentifierAttemptRepo) ListByFinding(ctx context.Context, findingID int64) ([]CalibreIdentifierAttempt, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, finding_id, actor_user_id, book_id, calibre_id, identifier_type, old_value,
		proposed_value, evidence_keys_json, action, comparison_fingerprint, outcome, error, created_at, finished_at
		FROM calibre_identifier_attempts WHERE finding_id = ? ORDER BY id DESC LIMIT 50`, findingID)
	if err != nil {
		return nil, fmt.Errorf("list Calibre identifier attempts: %w", err)
	}
	defer rows.Close()
	out := make([]CalibreIdentifierAttempt, 0)
	for rows.Next() {
		var a CalibreIdentifierAttempt
		var keys, created string
		var finished sql.NullString
		if err := rows.Scan(&a.ID, &a.FindingID, &a.ActorUserID, &a.BookID, &a.CalibreID,
			&a.IdentifierType, &a.OldValue, &a.ProposedValue, &keys, &a.Action,
			&a.ComparisonFingerprint, &a.Outcome, &a.Error, &created, &finished); err != nil {
			return nil, fmt.Errorf("scan Calibre identifier attempt: %w", err)
		}
		if err := json.Unmarshal([]byte(keys), &a.EvidenceKeys); err != nil {
			return nil, fmt.Errorf("decode Calibre identifier evidence keys: %w", err)
		}
		a.CreatedAt = parseDBTime(created)
		if finished.Valid {
			at := parseDBTime(finished.String)
			a.FinishedAt = &at
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Calibre identifier attempts: %w", err)
	}
	return out, nil
}
