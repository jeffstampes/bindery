package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CalibreMetadataRefreshRepo retains immutable lookup proposals and every write
// attempt independently of the cross-reference's deliberately minimal shape.
type CalibreMetadataRefreshRepo struct{ db *sql.DB }

func NewCalibreMetadataRefreshRepo(database *sql.DB) *CalibreMetadataRefreshRepo {
	return &CalibreMetadataRefreshRepo{db: database}
}

type CalibreMetadataRefreshRecord struct {
	ID          int64
	BookID      int64
	CalibreID   int64
	Fingerprint string
	Status      string
	JSON        string
}

func (r *CalibreMetadataRefreshRepo) Create(ctx context.Context, record *CalibreMetadataRefreshRecord) error {
	if record == nil || record.BookID <= 0 || record.CalibreID <= 0 || record.Fingerprint == "" || record.JSON == "" {
		return fmt.Errorf("invalid metadata refresh proposal")
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO calibre_metadata_refresh_proposals
		(book_id, calibre_id, fingerprint, status, proposal_json) VALUES (?, ?, ?, ?, ?)`,
		record.BookID, record.CalibreID, record.Fingerprint, record.Status, record.JSON)
	if err != nil {
		return fmt.Errorf("save metadata refresh proposal: %w", err)
	}
	record.ID, err = res.LastInsertId()
	return err
}

func (r *CalibreMetadataRefreshRepo) Get(ctx context.Context, id int64) (*CalibreMetadataRefreshRecord, error) {
	var record CalibreMetadataRefreshRecord
	err := r.db.QueryRowContext(ctx, `SELECT id, book_id, calibre_id, fingerprint, status, proposal_json
		FROM calibre_metadata_refresh_proposals WHERE id = ?`, id).Scan(
		&record.ID, &record.BookID, &record.CalibreID, &record.Fingerprint, &record.Status, &record.JSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read metadata refresh proposal: %w", err)
	}
	return &record, nil
}

type CalibreMetadataRefreshAttempt struct {
	ID           int64      `json:"id"`
	ProposalID   int64      `json:"proposalId"`
	ActorUserID  int64      `json:"actorUserId"`
	BookID       int64      `json:"bookId"`
	CalibreID    int64      `json:"calibreId"`
	Action       string     `json:"action"`
	PreWriteOPF  string     `json:"-"`
	Outcome      string     `json:"outcome"`
	Error        string     `json:"error,omitempty"`
	Verification string     `json:"verification,omitempty"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
}

func (r *CalibreMetadataRefreshRepo) Begin(ctx context.Context, a *CalibreMetadataRefreshAttempt) error {
	if a == nil || a.ProposalID <= 0 || a.BookID <= 0 || a.CalibreID <= 0 {
		return fmt.Errorf("invalid metadata refresh attempt")
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO calibre_metadata_refresh_attempts
		(proposal_id, actor_user_id, book_id, calibre_id, outcome) VALUES (?, ?, ?, ?, 'pending')`,
		a.ProposalID, a.ActorUserID, a.BookID, a.CalibreID)
	if err != nil {
		return fmt.Errorf("record metadata refresh intent: %w", err)
	}
	a.ID, err = res.LastInsertId()
	a.Action, a.Outcome = "apply_fields", "pending"
	return err
}

func (r *CalibreMetadataRefreshRepo) Capture(ctx context.Context, id int64, opf string) error {
	if opf == "" {
		return fmt.Errorf("empty pre-write OPF")
	}
	res, err := r.db.ExecContext(ctx, `UPDATE calibre_metadata_refresh_attempts SET pre_write_opf = ?
		WHERE id = ? AND outcome = 'pending' AND pre_write_opf = ''`, opf, id)
	if err != nil {
		return fmt.Errorf("capture pre-write OPF: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return fmt.Errorf("pre-write OPF capture not pending: %w", err)
	}
	return nil
}

func (r *CalibreMetadataRefreshRepo) Finish(ctx context.Context, id int64, outcome, detail, verification string) error {
	switch outcome {
	case "applied", "rejected", "partial", "failed", "verification_failed":
	default:
		return fmt.Errorf("invalid metadata refresh outcome")
	}
	res, err := r.db.ExecContext(ctx, `UPDATE calibre_metadata_refresh_attempts
		SET outcome = ?, error = ?, verification = ?, finished_at = ? WHERE id = ? AND outcome = 'pending'`,
		outcome, detail, verification, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("finish metadata refresh attempt: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return fmt.Errorf("metadata refresh attempt not pending: %w", err)
	}
	return nil
}

func (r *CalibreMetadataRefreshRepo) ListAttempts(ctx context.Context, bookID int64) ([]CalibreMetadataRefreshAttempt, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, proposal_id, actor_user_id, book_id, calibre_id,
		action, pre_write_opf, outcome, error, verification, started_at, finished_at
		FROM calibre_metadata_refresh_attempts WHERE book_id = ? ORDER BY id DESC LIMIT 50`, bookID)
	if err != nil {
		return nil, fmt.Errorf("list metadata refresh attempts: %w", err)
	}
	defer rows.Close()
	items := make([]CalibreMetadataRefreshAttempt, 0)
	for rows.Next() {
		var a CalibreMetadataRefreshAttempt
		var started string
		var finished sql.NullString
		if err := rows.Scan(&a.ID, &a.ProposalID, &a.ActorUserID, &a.BookID, &a.CalibreID,
			&a.Action, &a.PreWriteOPF, &a.Outcome, &a.Error, &a.Verification, &started, &finished); err != nil {
			return nil, fmt.Errorf("scan metadata refresh attempt: %w", err)
		}
		a.StartedAt = parseDBTime(started)
		if finished.Valid {
			at := parseDBTime(finished.String)
			a.FinishedAt = &at
		}
		items = append(items, a)
	}
	return items, rows.Err()
}
