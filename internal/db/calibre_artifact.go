package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

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
	// Archive superseded observations before replacing the active file inventory.
	// The historical rows keep their scan time and original file digest; later
	// write-back reports classify them without rewriting their observations.
	if _, err := tx.ExecContext(ctx, `INSERT INTO calibre_artifact_scan_history
		(book_id, calibre_id, format, file_name, file_path, sha256, size_bytes, modified_at, scanned_at, method, outcome, error, correlation_group, identifiers_json, attested_original)
		SELECT book_id, calibre_id, format, file_name, file_path, sha256, size_bytes, modified_at, scanned_at, method, outcome, error, correlation_group, identifiers_json, attested_original
		FROM calibre_artifact_scans WHERE book_id = ?`, bookID); err != nil {
		return fmt.Errorf("archive artifact scans: %w", err)
	}
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
			(book_id, calibre_id, format, file_name, file_path, sha256, size_bytes, modified_at, scanned_at, method, outcome, error, correlation_group, identifiers_json, attested_original)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, bookID, calibreID, scan.Format, scan.FileName, scan.FilePath,
			scan.SHA256, scan.SizeBytes, timeValueArg(scan.ModifiedAt), timeValueArg(scan.ScannedAt),
			scan.Method, scan.Outcome, scan.Error, scan.CorrelationGroup, string(data), scan.AttestedOriginal)
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
	rows, err := r.db.QueryContext(ctx, `SELECT book_id, calibre_id, format, file_name, file_path, sha256, size_bytes,
		modified_at, scanned_at, method, outcome, error, correlation_group, identifiers_json, attested_original
		FROM calibre_artifact_scans WHERE book_id = ? AND calibre_id = ? ORDER BY format, file_name`, bookID, calibreID)
	if err != nil {
		return nil, fmt.Errorf("list artifact scans: %w", err)
	}
	defer rows.Close()
	return readArtifactScans(rows)
}

// ListCurrent loads the scan inventory for an audit in one bulk query.
func (r *CalibreArtifactRepo) ListCurrent(ctx context.Context) (map[int64][]models.CalibreArtifactScan, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT book_id, calibre_id, format, file_name, file_path, sha256, size_bytes,
		modified_at, scanned_at, method, outcome, error, correlation_group, identifiers_json, attested_original
		FROM calibre_artifact_scans ORDER BY book_id, format, file_name`)
	if err != nil {
		return nil, fmt.Errorf("list current artifact scans: %w", err)
	}
	defer rows.Close()
	scans, err := readArtifactScans(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]models.CalibreArtifactScan)
	for _, scan := range scans {
		out[scan.BookID] = append(out[scan.BookID], scan)
	}
	return out, nil
}

// ListHistoryByBookID retains superseded observations for the same ownership.
func (r *CalibreArtifactRepo) ListHistoryByBookID(ctx context.Context, bookID, calibreID int64) ([]models.CalibreArtifactScan, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT book_id, calibre_id, format, file_name, file_path, sha256, size_bytes,
		modified_at, scanned_at, method, outcome, error, correlation_group, identifiers_json, attested_original
		FROM calibre_artifact_scan_history WHERE book_id = ? AND calibre_id = ? ORDER BY scanned_at, id`, bookID, calibreID)
	if err != nil {
		return nil, fmt.Errorf("list artifact history: %w", err)
	}
	defer rows.Close()
	scans, err := readArtifactScans(rows)
	for i := range scans {
		scans[i].Historical = true
	}
	return scans, err
}

// ListAllHistory loads historical scans in one query for active audit links.
func (r *CalibreArtifactRepo) ListAllHistory(ctx context.Context) (map[int64][]models.CalibreArtifactScan, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT h.book_id, h.calibre_id, h.format, h.file_name, h.file_path,
		h.sha256, h.size_bytes, h.modified_at, h.scanned_at, h.method, h.outcome, h.error, h.correlation_group, h.identifiers_json, h.attested_original
		FROM calibre_artifact_scan_history h JOIN calibre_work_cross_references r
		ON r.book_id = h.book_id AND r.calibre_id = h.calibre_id AND r.status = 'matched'
		ORDER BY h.book_id, h.scanned_at, h.id`)
	if err != nil {
		return nil, fmt.Errorf("list artifact history for audit: %w", err)
	}
	defer rows.Close()
	scans, err := readArtifactScans(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]models.CalibreArtifactScan)
	for _, scan := range scans {
		scan.Historical = true
		out[scan.BookID] = append(out[scan.BookID], scan)
	}
	return out, nil
}

func readArtifactScans(rows *sql.Rows) ([]models.CalibreArtifactScan, error) {
	out := []models.CalibreArtifactScan{}
	for rows.Next() {
		var s models.CalibreArtifactScan
		var modified, scanned, identifiers string
		if err := rows.Scan(&s.BookID, &s.CalibreID, &s.Format, &s.FileName, &s.FilePath, &s.SHA256, &s.SizeBytes,
			&modified, &scanned, &s.Method, &s.Outcome, &s.Error, &s.CorrelationGroup, &identifiers, &s.AttestedOriginal); err != nil {
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

// RecordWriteback stores an operator-reported timestamp, never modifying an ebook
// or Calibre. Missing write-backs are not inferred from matching identifier values.
func (r *CalibreArtifactRepo) RecordWriteback(ctx context.Context, event models.CalibreArtifactWriteback) error {
	if event.BookID <= 0 || event.CalibreID <= 0 || event.WrittenAt.IsZero() ||
		(event.Source != "polish_books" && event.Source != "calibre_to_file") ||
		event.WrittenAt.After(time.Now().UTC()) {
		return fmt.Errorf("invalid artifact write-back report")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO calibre_artifact_writebacks
		(book_id, calibre_id, written_at, recorded_at, source) VALUES (?,?,?,?,?)`,
		event.BookID, event.CalibreID, timeValueArg(event.WrittenAt), timeValueArg(time.Now().UTC()), event.Source)
	if err != nil {
		return fmt.Errorf("record artifact write-back: %w", err)
	}
	return nil
}

// ListWritebacks returns reported events for the active ownership link only.
func (r *CalibreArtifactRepo) ListWritebacks(ctx context.Context, bookID, calibreID int64) ([]models.CalibreArtifactWriteback, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT written_at, recorded_at, source FROM calibre_artifact_writebacks
		WHERE book_id = ? AND calibre_id = ? ORDER BY written_at`, bookID, calibreID)
	if err != nil {
		return nil, fmt.Errorf("list artifact write-backs: %w", err)
	}
	defer rows.Close()
	out := []models.CalibreArtifactWriteback{}
	for rows.Next() {
		var written, recorded string
		e := models.CalibreArtifactWriteback{BookID: bookID, CalibreID: calibreID}
		if err := rows.Scan(&written, &recorded, &e.Source); err != nil {
			return nil, fmt.Errorf("scan artifact write-back: %w", err)
		}
		e.WrittenAt, e.RecordedAt = parseDBTime(written), parseDBTime(recorded)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListAllWritebacks loads reported lineage events in one audit query.
func (r *CalibreArtifactRepo) ListAllWritebacks(ctx context.Context) (map[int64][]models.CalibreArtifactWriteback, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT book_id, calibre_id, written_at, recorded_at, source
		FROM calibre_artifact_writebacks ORDER BY book_id, written_at`)
	if err != nil {
		return nil, fmt.Errorf("list artifact write-backs: %w", err)
	}
	defer rows.Close()
	out := make(map[int64][]models.CalibreArtifactWriteback)
	for rows.Next() {
		var written, recorded string
		var e models.CalibreArtifactWriteback
		if err := rows.Scan(&e.BookID, &e.CalibreID, &written, &recorded, &e.Source); err != nil {
			return nil, fmt.Errorf("scan artifact write-back: %w", err)
		}
		e.WrittenAt, e.RecordedAt = parseDBTime(written), parseDBTime(recorded)
		out[e.BookID] = append(out[e.BookID], e)
	}
	return out, rows.Err()
}
