package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/vavallee/bindery/internal/models"
)

// CalibreArtifactRepo stores explicit file scans separately from provider
// snapshots; no refresh of metadata.db claims can manufacture these rows.
type CalibreArtifactRepo struct{ db *sql.DB }

func NewCalibreArtifactRepo(database *sql.DB) *CalibreArtifactRepo {
	return &CalibreArtifactRepo{db: database}
}

// ReplaceForBook atomically replaces the file inventory for one owned work.
// It never touches the provider graph or the Calibre database.
func (r *CalibreArtifactRepo) ReplaceForBook(ctx context.Context, bookID, calibreID int64, scans []models.CalibreArtifactScan) error {
	if bookID <= 0 || calibreID <= 0 {
		return fmt.Errorf("invalid artifact owner %d/%d", bookID, calibreID)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin artifact replacement: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM calibre_artifact_scans WHERE book_id = ?`, bookID); err != nil {
		return fmt.Errorf("clear artifact scans: %w", err)
	}
	for _, scan := range scans {
		if scan.BookID != bookID || scan.CalibreID != calibreID || scan.Format == "" || scan.FileName == "" || scan.Method == "" {
			return fmt.Errorf("invalid artifact scan for work %d", bookID)
		}
		data, err := json.Marshal(scan.Identifiers)
		if err != nil {
			return fmt.Errorf("encode artifact identifiers: %w", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO calibre_artifact_scans
			(book_id, calibre_id, format, file_name, file_path, sha256, size_bytes, modified_at, scanned_at, method, outcome, error, correlation_group, identifiers_json)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, bookID, calibreID, scan.Format, scan.FileName, scan.FilePath,
			scan.SHA256, scan.SizeBytes, timeValueArg(scan.ModifiedAt), timeValueArg(scan.ScannedAt),
			scan.Method, scan.Outcome, scan.Error, scan.CorrelationGroup, string(data))
		if err != nil {
			return fmt.Errorf("insert artifact scan: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit artifact scans: %w", err)
	}
	return nil
}

// CountCurrent counts cached file scans attached to presently matched Calibre
// links in one indexed query. It never reads or rescans artifact contents.
func (r *CalibreArtifactRepo) CountCurrent(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM calibre_artifact_scans s
		JOIN calibre_work_cross_references r ON r.book_id = s.book_id
			AND r.calibre_id = s.calibre_id AND r.status = 'matched'`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count current calibre artifact scans: %w", err)
	}
	return count, nil
}

// ListByBookID is indexed by its leading book_id; a detail read never walks
// all files in a large library. Older Calibre links are excluded by ID.
func (r *CalibreArtifactRepo) ListByBookID(ctx context.Context, bookID, calibreID int64) ([]models.CalibreArtifactScan, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT format, file_name, file_path, sha256, size_bytes,
		modified_at, scanned_at, method, outcome, error, correlation_group, identifiers_json
		FROM calibre_artifact_scans WHERE book_id = ? AND calibre_id = ? ORDER BY format, file_name`, bookID, calibreID)
	if err != nil {
		return nil, fmt.Errorf("list artifact scans: %w", err)
	}
	defer rows.Close()
	out := []models.CalibreArtifactScan{}
	for rows.Next() {
		s := models.CalibreArtifactScan{BookID: bookID, CalibreID: calibreID}
		var modified, scanned, identifiers string
		if err := rows.Scan(&s.Format, &s.FileName, &s.FilePath, &s.SHA256, &s.SizeBytes,
			&modified, &scanned, &s.Method, &s.Outcome, &s.Error, &s.CorrelationGroup, &identifiers); err != nil {
			return nil, fmt.Errorf("scan artifact row: %w", err)
		}
		s.ModifiedAt, s.ScannedAt = parseDBTime(modified), parseDBTime(scanned)
		if err := json.Unmarshal([]byte(identifiers), &s.Identifiers); err != nil {
			return nil, fmt.Errorf("decode artifact identifiers: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifact scans: %w", err)
	}
	return out, nil
}
