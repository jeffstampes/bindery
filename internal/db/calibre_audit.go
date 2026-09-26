package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// CalibreAuditRepo persists advisory comparisons in Bindery's database only.
type CalibreAuditRepo struct {
	db *sql.DB
}

func NewCalibreAuditRepo(database *sql.DB) *CalibreAuditRepo {
	return &CalibreAuditRepo{db: database}
}

// List returns the durable finding history in one read, including resolved and
// unmatched rows. The audit uses it to determine which old comparisons cleared.
func (r *CalibreAuditRepo) List(ctx context.Context) ([]models.CalibreAuditFinding, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, book_id, calibre_id, field, evidence_key, finding_type, assessment,
		       calibre_evidence_json, bindery_evidence_json, match_method,
		       match_confidence, reason, comparison_fingerprint, ignored_fingerprint, state,
		       created_at, updated_at
		FROM calibre_metadata_audit_findings ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list calibre audit findings: %w", err)
	}
	defer rows.Close()

	var findings []models.CalibreAuditFinding
	for rows.Next() {
		var f models.CalibreAuditFinding
		var calibreJSON, binderyJSON, created, updated string
		if err := rows.Scan(&f.ID, &f.BookID, &f.CalibreID, &f.Field, &f.EvidenceKey,
			&f.FindingType, &f.Assessment, &calibreJSON, &binderyJSON, &f.MatchMethod,
			&f.MatchConfidence, &f.Reason, &f.ComparisonFingerprint, &f.IgnoredFingerprint, &f.State,
			&created, &updated); err != nil {
			return nil, fmt.Errorf("scan calibre audit finding: %w", err)
		}
		if err := json.Unmarshal([]byte(calibreJSON), &f.CalibreEvidence); err != nil {
			return nil, fmt.Errorf("decode calibre audit finding %d calibre evidence: %w", f.ID, err)
		}
		if err := json.Unmarshal([]byte(binderyJSON), &f.BinderyEvidence); err != nil {
			return nil, fmt.Errorf("decode calibre audit finding %d bindery evidence: %w", f.ID, err)
		}
		f.CreatedAt = parseDBTime(created)
		f.UpdatedAt = parseDBTime(updated)
		findings = append(findings, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate calibre audit findings: %w", err)
	}
	return findings, nil
}

// Ignore records a human decision for exactly the comparison the reviewer saw.
// A stale fingerprint or non-unresolved finding is not ignored. No metadata is
// changed, and Apply will reopen it when materially compared values change.
func (r *CalibreAuditRepo) Ignore(ctx context.Context, id int64, fingerprint string) (bool, error) {
	if id <= 0 || fingerprint == "" {
		return false, nil
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE calibre_metadata_audit_findings
		SET state = ?, ignored_fingerprint = comparison_fingerprint, updated_at = ?
		WHERE id = ? AND comparison_fingerprint = ? AND state = ?`,
		models.CalibreAuditIgnored, timeValueArg(time.Now().UTC()), id, fingerprint,
		models.CalibreAuditUnresolved)
	if err != nil {
		return false, fmt.Errorf("ignore calibre audit finding %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("ignore calibre audit finding %d rows affected: %w", id, err)
	}
	return n == 1, nil
}

// Apply batches a complete pass's changed findings into one transaction and
// returns the number actually written. The conflict rule preserves an ignore
// only for the same comparison fingerprint, including after a temporary
// unmatch; it never suppresses changed evidence or a new owned match.
func (r *CalibreAuditRepo) Apply(ctx context.Context, findings []models.CalibreAuditFinding) (int, error) {
	if len(findings) == 0 {
		return 0, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin calibre audit findings update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const batchSize = 50 // 750 bind values, below even SQLite's historical 999-variable limit.
	now := timeValueArg(time.Now().UTC())
	written := 0
	for start := 0; start < len(findings); start += batchSize {
		end := min(start+batchSize, len(findings))
		values := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*15)
		for _, f := range findings[start:end] {
			if f.BookID <= 0 || f.CalibreID <= 0 || f.State == models.CalibreAuditIgnored {
				return 0, fmt.Errorf("invalid calibre audit update for book %d", f.BookID)
			}
			calibreJSON, err := encodeCalibreAuditEvidence(f.CalibreEvidence)
			if err != nil {
				return 0, fmt.Errorf("encode calibre evidence for book %d: %w", f.BookID, err)
			}
			binderyJSON, err := encodeCalibreAuditEvidence(f.BinderyEvidence)
			if err != nil {
				return 0, fmt.Errorf("encode bindery evidence for book %d: %w", f.BookID, err)
			}
			values = append(values, "(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
			args = append(args, f.BookID, f.CalibreID, f.Field, f.EvidenceKey,
				f.FindingType, f.Assessment, calibreJSON, binderyJSON,
				f.MatchMethod, f.MatchConfidence, f.Reason,
				f.ComparisonFingerprint, f.State, now, now)
		}
		// Only generated ? tuples are concatenated; every value is bound below.
		// Filtering against books in this write statement avoids a stale FK if a
		// Bindery work was deleted after the audit took its bulk snapshot.
		//nolint:gosec // G202: the SQL fragments are constants or generated placeholders, never user input.
		query := `WITH audit_input (
			book_id, calibre_id, field, evidence_key, finding_type, assessment,
			calibre_evidence_json, bindery_evidence_json, match_method,
			match_confidence, reason, comparison_fingerprint, state,
			created_at, updated_at
		) AS (VALUES ` + strings.Join(values, ",") + `)
		INSERT INTO calibre_metadata_audit_findings (
			book_id, calibre_id, field, evidence_key, finding_type, assessment,
			calibre_evidence_json, bindery_evidence_json, match_method,
			match_confidence, reason, comparison_fingerprint, state,
			created_at, updated_at
		) SELECT * FROM audit_input
		WHERE EXISTS (SELECT 1 FROM books WHERE books.id = audit_input.book_id)
		ON CONFLICT(book_id, field, evidence_key) DO UPDATE SET
			calibre_id = excluded.calibre_id,
			finding_type = excluded.finding_type,
			assessment = excluded.assessment,
			calibre_evidence_json = excluded.calibre_evidence_json,
			bindery_evidence_json = excluded.bindery_evidence_json,
			match_method = excluded.match_method,
			match_confidence = excluded.match_confidence,
			reason = excluded.reason,
			comparison_fingerprint = excluded.comparison_fingerprint,
			state = CASE
				WHEN excluded.state = 'unresolved'
				 AND calibre_metadata_audit_findings.ignored_fingerprint = excluded.comparison_fingerprint
				 AND calibre_metadata_audit_findings.calibre_id = excluded.calibre_id
				 THEN 'ignored'
				ELSE excluded.state END,
			ignored_fingerprint = CASE
				WHEN excluded.state = 'unmatched' THEN calibre_metadata_audit_findings.ignored_fingerprint
				WHEN excluded.state = 'unresolved'
				 AND calibre_metadata_audit_findings.ignored_fingerprint = excluded.comparison_fingerprint
				 AND calibre_metadata_audit_findings.calibre_id = excluded.calibre_id
				 THEN calibre_metadata_audit_findings.ignored_fingerprint
				ELSE '' END,
			updated_at = excluded.updated_at`
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return 0, fmt.Errorf("apply calibre audit findings batch: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("count applied calibre audit findings: %w", err)
		}
		written += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit calibre audit findings: %w", err)
	}
	return written, nil
}

// ListMatchedSeriesEvidence loads every stored series association for currently
// matched works in a single join. It is the Bindery-side series evidence; the
// transient provider SeriesRefs on Book cannot be read back from the database.
func (r *CalibreAuditRepo) ListMatchedSeriesEvidence(ctx context.Context) (map[int64][]BookSeriesMembership, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT sb.book_id, sb.series_id, COALESCE(s.foreign_id, ''), COALESCE(s.title, ''),
		       COALESCE(sb.position_in_series, ''), COALESCE(sb.primary_series, 0)
		FROM calibre_work_cross_references cr
		JOIN series_books sb ON sb.book_id = cr.book_id
		JOIN series s ON s.id = sb.series_id
		WHERE cr.status = 'matched'
		ORDER BY sb.book_id, sb.primary_series DESC, sb.series_id`)
	if err != nil {
		return nil, fmt.Errorf("list matched calibre audit series evidence: %w", err)
	}
	defer rows.Close()

	out := make(map[int64][]BookSeriesMembership)
	for rows.Next() {
		var m BookSeriesMembership
		var primary int
		if err := rows.Scan(&m.BookID, &m.SeriesID, &m.SeriesForeignID,
			&m.SeriesTitle, &m.Position, &primary); err != nil {
			return nil, fmt.Errorf("scan matched calibre audit series evidence: %w", err)
		}
		m.Primary = primary != 0
		out[m.BookID] = append(out[m.BookID], m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate matched calibre audit series evidence: %w", err)
	}
	return out, nil
}

func encodeCalibreAuditEvidence(values []models.CalibreAuditEvidence) (string, error) {
	if len(values) == 0 {
		return "[]", nil
	}
	encoded, err := json.Marshal(values)
	return string(encoded), err
}
