package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// CalibreCrossReferenceRepo manages persistence for calibre_work_cross_references.
type CalibreCrossReferenceRepo struct {
	db   *sql.DB
	exec dbExecutor
}

func NewCalibreCrossReferenceRepo(db *sql.DB) *CalibreCrossReferenceRepo {
	return &CalibreCrossReferenceRepo{db: db, exec: db}
}

func (r *CalibreCrossReferenceRepo) WithTx(tx *sql.Tx) *CalibreCrossReferenceRepo {
	clone := *r
	clone.exec = tx
	return &clone
}

func (r *CalibreCrossReferenceRepo) UpsertCrossReference(ctx context.Context, ref *models.CalibreWorkCrossReference) error {
	if ref == nil || ref.BookID == 0 {
		return errors.New("invalid cross-reference: missing book ID")
	}
	now := time.Now().UTC()
	if ref.MatchDetailsJSON == "" {
		ref.MatchDetailsJSON = "{}"
	}
	if ref.Status == "" {
		ref.Status = models.CalibreMatchStatusMatched
	}
	if ref.Confidence == "" {
		ref.Confidence = models.CalibreMatchConfidenceExact
	}

	result, err := r.exec.ExecContext(ctx, `
		INSERT INTO calibre_work_cross_references (
			book_id, calibre_id, match_method, confidence, status,
			calibre_fingerprint, match_details_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (book_id) DO UPDATE SET
			calibre_id = EXCLUDED.calibre_id,
			match_method = EXCLUDED.match_method,
			confidence = EXCLUDED.confidence,
			status = EXCLUDED.status,
			calibre_fingerprint = EXCLUDED.calibre_fingerprint,
			match_details_json = EXCLUDED.match_details_json,
			updated_at = EXCLUDED.updated_at`,
		ref.BookID, ref.CalibreID, ref.MatchMethod, ref.Confidence, ref.Status,
		ref.CalibreFingerprint, ref.MatchDetailsJSON, now, now)
	if err != nil {
		return fmt.Errorf("upsert calibre cross reference for book %d: %w", ref.BookID, err)
	}

	if ref.ID == 0 {
		id, err := result.LastInsertId()
		if err == nil && id > 0 {
			ref.ID = id
		}
	}
	if ref.CreatedAt.IsZero() {
		ref.CreatedAt = now
	}
	ref.UpdatedAt = now
	return nil
}

func (r *CalibreCrossReferenceRepo) GetByBookID(ctx context.Context, bookID int64) (*models.CalibreWorkCrossReference, error) {
	row := r.exec.QueryRowContext(ctx, `
		SELECT id, book_id, calibre_id, match_method, confidence, status,
		       calibre_fingerprint, match_details_json, created_at, updated_at
		FROM calibre_work_cross_references
		WHERE book_id = ?`, bookID)

	var ref models.CalibreWorkCrossReference
	var createdAt, updatedAt string
	err := row.Scan(
		&ref.ID, &ref.BookID, &ref.CalibreID, &ref.MatchMethod, &ref.Confidence,
		&ref.Status, &ref.CalibreFingerprint, &ref.MatchDetailsJSON,
		&createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get calibre cross reference for book %d: %w", bookID, err)
	}
	ref.CreatedAt = parseDBTime(createdAt)
	ref.UpdatedAt = parseDBTime(updatedAt)
	return &ref, nil
}

func (r *CalibreCrossReferenceRepo) GetByCalibreID(ctx context.Context, calibreID int64) ([]models.CalibreWorkCrossReference, error) {
	rows, err := r.exec.QueryContext(ctx, `
		SELECT id, book_id, calibre_id, match_method, confidence, status,
		       calibre_fingerprint, match_details_json, created_at, updated_at
		FROM calibre_work_cross_references
		WHERE calibre_id = ? ORDER BY id`, calibreID)
	if err != nil {
		return nil, fmt.Errorf("get calibre cross references for calibre_id %d: %w", calibreID, err)
	}
	defer rows.Close()

	var out []models.CalibreWorkCrossReference
	for rows.Next() {
		var ref models.CalibreWorkCrossReference
		var createdAt, updatedAt string
		if err := rows.Scan(
			&ref.ID, &ref.BookID, &ref.CalibreID, &ref.MatchMethod, &ref.Confidence,
			&ref.Status, &ref.CalibreFingerprint, &ref.MatchDetailsJSON,
			&createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan calibre cross reference: %w", err)
		}
		ref.CreatedAt = parseDBTime(createdAt)
		ref.UpdatedAt = parseDBTime(updatedAt)
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate calibre cross references for calibre_id %d: %w", calibreID, err)
	}
	return out, nil
}

func (r *CalibreCrossReferenceRepo) ListByStatus(ctx context.Context, status string) ([]models.CalibreWorkCrossReference, error) {
	var rows *sql.Rows
	var err error
	if status == "" {
		rows, err = r.exec.QueryContext(ctx, `
			SELECT id, book_id, calibre_id, match_method, confidence, status,
			       calibre_fingerprint, match_details_json, created_at, updated_at
			FROM calibre_work_cross_references ORDER BY id`)
	} else {
		rows, err = r.exec.QueryContext(ctx, `
			SELECT id, book_id, calibre_id, match_method, confidence, status,
			       calibre_fingerprint, match_details_json, created_at, updated_at
			FROM calibre_work_cross_references WHERE status = ? ORDER BY id`, status)
	}
	if err != nil {
		return nil, fmt.Errorf("list calibre cross references (status %q): %w", status, err)
	}
	defer rows.Close()

	var out []models.CalibreWorkCrossReference
	for rows.Next() {
		var ref models.CalibreWorkCrossReference
		var createdAt, updatedAt string
		if err := rows.Scan(
			&ref.ID, &ref.BookID, &ref.CalibreID, &ref.MatchMethod, &ref.Confidence,
			&ref.Status, &ref.CalibreFingerprint, &ref.MatchDetailsJSON,
			&createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan calibre cross reference: %w", err)
		}
		ref.CreatedAt = parseDBTime(createdAt)
		ref.UpdatedAt = parseDBTime(updatedAt)
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate calibre cross references: %w", err)
	}
	return out, nil
}

func (r *CalibreCrossReferenceRepo) DeleteByBookID(ctx context.Context, bookID int64) error {
	_, err := r.exec.ExecContext(ctx, `DELETE FROM calibre_work_cross_references WHERE book_id = ?`, bookID)
	if err != nil {
		return fmt.Errorf("delete calibre cross reference for book %d: %w", bookID, err)
	}
	return nil
}

func parseDBTime(s string) time.Time {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
