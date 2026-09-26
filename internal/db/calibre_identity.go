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

// CalibreIdentityRepo stores discovery results in Bindery only. A batch
// replaces exactly the supplied works; absent works retain their snapshots.
type CalibreIdentityRepo struct{ db *sql.DB }

func NewCalibreIdentityRepo(database *sql.DB) *CalibreIdentityRepo {
	return &CalibreIdentityRepo{db: database}
}

// ReplaceBatch replaces all evidence, lookup outcomes and CWA identifier
// claims for each supplied work in one transaction. Empty slices clear stale
// child rows; an empty batch is a no-op. Writes use bounded multi-row SQL, not
// a read or prepared statement per book. A missing Bindery book rolls back the
// entire batch rather than leaving an incomplete discovery snapshot.
func (r *CalibreIdentityRepo) ReplaceBatch(ctx context.Context, snapshots []models.CalibreIdentitySnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin calibre identity replacement: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	seen := make(map[int64]bool, len(snapshots))
	const workBatch = 100
	for start := 0; start < len(snapshots); start += workBatch {
		end := min(start+workBatch, len(snapshots))
		batch := snapshots[start:end]
		idArgs := make([]int64, 0, len(batch))
		roots := make([][]any, 0, len(batch))
		var evidence, lookups, claims [][]any
		for _, s := range batch {
			if s.BookID <= 0 || s.CalibreID <= 0 || seen[s.BookID] {
				return fmt.Errorf("invalid or duplicate calibre identity work %d / calibre %d", s.BookID, s.CalibreID)
			}
			seen[s.BookID] = true
			checked := s.CheckedAt
			if checked.IsZero() {
				checked = time.Now().UTC()
			}
			idArgs = append(idArgs, s.BookID)
			roots = append(roots, []any{s.BookID, s.CalibreID, s.RootKey, timeValueArg(checked)})
			for _, e := range s.Evidence {
				if e.Key == "" || e.Provider == "" || (e.BookID != 0 && e.BookID != s.BookID) ||
					(e.CalibreID != 0 && e.CalibreID != s.CalibreID) || !identityEvidenceStatus(e.Status) {
					return fmt.Errorf("invalid calibre identity evidence %q for book %d", e.Key, s.BookID)
				}
				identifiers, err := identityJSON(e.NormalizedIdentifiers)
				if err != nil {
					return fmt.Errorf("encode identity identifiers for book %d: %w", s.BookID, err)
				}
				metadata, err := identityJSON(e.ProviderMetadata)
				if err != nil {
					return fmt.Errorf("encode provider metadata for book %d: %w", s.BookID, err)
				}
				evidence = append(evidence, []any{s.BookID, e.Key, e.CanonicalIdentity, e.Provider,
					e.ForeignID, e.EditionID, e.Method, e.Seed, e.ProvenanceGroup, e.Status,
					e.WorkConfidence, e.EditionConfidence, identifiers, metadata, identityCheckedAt(e.CheckedAt, checked)})
			}
			for _, l := range s.Lookups {
				if l.Provider == "" || (l.BookID != 0 && l.BookID != s.BookID) ||
					(l.CalibreID != 0 && l.CalibreID != s.CalibreID) || !identityLookupOutcome(l.Outcome) {
					return fmt.Errorf("invalid calibre identity lookup for book %d provider %q", s.BookID, l.Provider)
				}
				lookups = append(lookups, []any{s.BookID, l.Provider, l.Method, l.Seed, l.Outcome,
					l.Error, identityCheckedAt(l.CheckedAt, checked)})
			}
			for _, c := range s.Claims {
				if c.IdentifierType == "" || c.IdentifierValue == "" ||
					(c.BookID != 0 && c.BookID != s.BookID) || (c.CalibreID != 0 && c.CalibreID != s.CalibreID) ||
					!identityClaimStatus(c.Status) {
					return fmt.Errorf("invalid calibre identity claim for book %d type %q", s.BookID, c.IdentifierType)
				}
				var evidenceKey any
				if c.EvidenceKey != "" {
					evidenceKey = c.EvidenceKey
				}
				claims = append(claims, []any{s.BookID, c.IdentifierType, c.IdentifierValue,
					c.Status, evidenceKey, identityCheckedAt(c.CheckedAt, checked)})
			}
		}
		// Deleting a root cascades its children. Reinsert evidence before claims
		// so the latter's composite FK rejects a cross-work or stale reference.
		idsJSON, err := json.Marshal(idArgs)
		if err != nil {
			return fmt.Errorf("encode calibre identity batch ids: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM calibre_identity_snapshots WHERE book_id IN (SELECT value FROM json_each(?))`, string(idsJSON)); err != nil {
			return fmt.Errorf("delete calibre identity snapshots: %w", err)
		}
		for _, part := range []struct {
			query string
			rows  [][]any
		}{
			{`INSERT INTO calibre_identity_snapshots (book_id, calibre_id, root_key, checked_at) VALUES `, roots},
			{`INSERT INTO calibre_identity_evidence (book_id, evidence_key, canonical_identity, provider, foreign_id, edition_id, method, seed, provenance_group, status, work_confidence, edition_confidence, normalized_identifiers_json, provider_metadata_json, checked_at) VALUES `, evidence},
			{`INSERT INTO calibre_identity_lookups (book_id, provider, method, seed, outcome, error, checked_at) VALUES `, lookups},
			{`INSERT INTO calibre_identity_claims (book_id, identifier_type, identifier_value, status, evidence_key, checked_at) VALUES `, claims},
		} {
			if err := insertIdentityRows(ctx, tx, part.query, part.rows); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit calibre identity replacement: %w", err)
	}
	return nil
}

func identityEvidenceStatus(status string) bool {
	switch status {
	case models.CalibreIdentityRoot, models.CalibreIdentityCorroborated, models.CalibreIdentityCandidate, models.CalibreIdentityConflict:
		return true
	}
	return false
}

func identityLookupOutcome(outcome string) bool {
	switch outcome {
	case models.CalibreIdentityLookupAnswered, models.CalibreIdentityLookupEmpty, models.CalibreIdentityLookupFailed,
		models.CalibreIdentityLookupUnconfigured, models.CalibreIdentityLookupTruncated, models.CalibreIdentityLookupNotAttempted:
		return true
	}
	return false
}

func identityClaimStatus(status string) bool {
	switch status {
	case models.CalibreIdentityClaimAgrees, models.CalibreIdentityClaimConflicts, models.CalibreIdentityClaimUnverified:
		return true
	}
	return false
}

func identityCheckedAt(t, fallback time.Time) any {
	if t.IsZero() {
		return timeValueArg(fallback)
	}
	return timeValueArg(t)
}

func identityJSON(v any) (string, error) {
	if v == nil {
		return "{}", nil
	}
	data, err := json.Marshal(v)
	return string(data), err
}

// insertIdentityRows uses at most 900 bound values per statement, under even
// SQLite's historical 999-variable ceiling. query comes only from constants
// above; row values are always bound parameters.
func insertIdentityRows(ctx context.Context, tx *sql.Tx, query string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	width := len(rows[0])
	maxRows := 900 / width
	placeholders := "(" + strings.TrimSuffix(strings.Repeat("?,", width), ",") + ")"
	for start := 0; start < len(rows); start += maxRows {
		end := min(start+maxRows, len(rows))
		values := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*width)
		for _, row := range rows[start:end] {
			values = append(values, placeholders)
			args = append(args, row...)
		}
		if _, err := tx.ExecContext(ctx, query+strings.Join(values, ","), args...); err != nil {
			return fmt.Errorf("insert calibre identity rows: %w", err)
		}
	}
	return nil
}

// ListByBookID returns nil for a work with no identity snapshot.
func (r *CalibreIdentityRepo) ListByBookID(ctx context.Context, bookID int64) (*models.CalibreIdentitySnapshot, error) {
	rows, err := r.list(ctx, " WHERE book_id = ?", bookID)
	if err != nil {
		return nil, err
	}
	if s, ok := rows[bookID]; ok {
		return &s, nil
	}
	return nil, nil
}

// ListAll bulk-loads all snapshots and child rows in four queries. It does
// not issue reads per work, even for a large owned library.
func (r *CalibreIdentityRepo) ListAll(ctx context.Context) (map[int64]models.CalibreIdentitySnapshot, error) {
	return r.list(ctx, "")
}

func (r *CalibreIdentityRepo) list(ctx context.Context, filter string, args ...any) (map[int64]models.CalibreIdentitySnapshot, error) {
	out := make(map[int64]models.CalibreIdentitySnapshot)
	var rows *sql.Rows
	var err error
	if filter == "" {
		rows, err = r.db.QueryContext(ctx, `SELECT book_id, calibre_id, root_key, checked_at FROM calibre_identity_snapshots ORDER BY book_id`)
	} else {
		rows, err = r.db.QueryContext(ctx, `SELECT book_id, calibre_id, root_key, checked_at FROM calibre_identity_snapshots WHERE book_id = ? ORDER BY book_id`, args...)
	}
	if err != nil {
		return nil, fmt.Errorf("list calibre identity snapshots: %w", err)
	}
	for rows.Next() {
		var s models.CalibreIdentitySnapshot
		var checked string
		if err := rows.Scan(&s.BookID, &s.CalibreID, &s.RootKey, &checked); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan calibre identity snapshot: %w", err)
		}
		s.CheckedAt = parseDBTime(checked)
		s.Evidence = []models.CalibreIdentityEvidence{}
		s.Lookups = []models.CalibreIdentityLookup{}
		s.Claims = []models.CalibreIdentityClaim{}
		out[s.BookID] = s
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate calibre identity snapshots: %w", err)
	}
	_ = rows.Close()
	if len(out) == 0 {
		return out, nil
	}
	// With a single-work filter each child query is indexed on its PK's
	// leading book_id. Without it the queries scan the child tables only once.
	if err := r.listEvidence(ctx, out, filter, args...); err != nil {
		return nil, err
	}
	if err := r.listLookups(ctx, out, filter, args...); err != nil {
		return nil, err
	}
	if err := r.listClaims(ctx, out, filter, args...); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *CalibreIdentityRepo) listEvidence(ctx context.Context, out map[int64]models.CalibreIdentitySnapshot, filter string, args ...any) error {
	var rows *sql.Rows
	var err error
	if filter == "" {
		rows, err = r.db.QueryContext(ctx, `SELECT book_id, evidence_key, canonical_identity, provider, foreign_id, edition_id, method, seed, provenance_group, status, work_confidence, edition_confidence, normalized_identifiers_json, provider_metadata_json, checked_at FROM calibre_identity_evidence ORDER BY book_id, evidence_key`)
	} else {
		rows, err = r.db.QueryContext(ctx, `SELECT book_id, evidence_key, canonical_identity, provider, foreign_id, edition_id, method, seed, provenance_group, status, work_confidence, edition_confidence, normalized_identifiers_json, provider_metadata_json, checked_at FROM calibre_identity_evidence WHERE book_id = ? ORDER BY book_id, evidence_key`, args...)
	}
	if err != nil {
		return fmt.Errorf("list calibre identity evidence: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e models.CalibreIdentityEvidence
		var identifiers, metadata, checked string
		if err := rows.Scan(&e.BookID, &e.Key, &e.CanonicalIdentity, &e.Provider, &e.ForeignID,
			&e.EditionID, &e.Method, &e.Seed, &e.ProvenanceGroup, &e.Status, &e.WorkConfidence,
			&e.EditionConfidence, &identifiers, &metadata, &checked); err != nil {
			return fmt.Errorf("scan calibre identity evidence: %w", err)
		}
		if err := json.Unmarshal([]byte(identifiers), &e.NormalizedIdentifiers); err != nil {
			return fmt.Errorf("decode identity identifiers for book %d: %w", e.BookID, err)
		}
		if err := json.Unmarshal([]byte(metadata), &e.ProviderMetadata); err != nil {
			return fmt.Errorf("decode provider metadata for book %d: %w", e.BookID, err)
		}
		e.CheckedAt = parseDBTime(checked)
		s := out[e.BookID]
		e.CalibreID = s.CalibreID
		s.Evidence = append(s.Evidence, e)
		out[e.BookID] = s
	}
	return rows.Err()
}

func (r *CalibreIdentityRepo) listLookups(ctx context.Context, out map[int64]models.CalibreIdentitySnapshot, filter string, args ...any) error {
	var rows *sql.Rows
	var err error
	if filter == "" {
		rows, err = r.db.QueryContext(ctx, `SELECT book_id, provider, method, seed, outcome, error, checked_at FROM calibre_identity_lookups ORDER BY book_id, provider, method, seed`)
	} else {
		rows, err = r.db.QueryContext(ctx, `SELECT book_id, provider, method, seed, outcome, error, checked_at FROM calibre_identity_lookups WHERE book_id = ? ORDER BY book_id, provider, method, seed`, args...)
	}
	if err != nil {
		return fmt.Errorf("list calibre identity lookups: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var l models.CalibreIdentityLookup
		var checked string
		if err := rows.Scan(&l.BookID, &l.Provider, &l.Method, &l.Seed, &l.Outcome, &l.Error, &checked); err != nil {
			return fmt.Errorf("scan calibre identity lookup: %w", err)
		}
		l.CheckedAt = parseDBTime(checked)
		s := out[l.BookID]
		l.CalibreID = s.CalibreID
		s.Lookups = append(s.Lookups, l)
		out[l.BookID] = s
	}
	return rows.Err()
}

func (r *CalibreIdentityRepo) listClaims(ctx context.Context, out map[int64]models.CalibreIdentitySnapshot, filter string, args ...any) error {
	var rows *sql.Rows
	var err error
	if filter == "" {
		rows, err = r.db.QueryContext(ctx, `SELECT book_id, identifier_type, identifier_value, status, evidence_key, checked_at FROM calibre_identity_claims ORDER BY book_id, identifier_type, identifier_value`)
	} else {
		rows, err = r.db.QueryContext(ctx, `SELECT book_id, identifier_type, identifier_value, status, evidence_key, checked_at FROM calibre_identity_claims WHERE book_id = ? ORDER BY book_id, identifier_type, identifier_value`, args...)
	}
	if err != nil {
		return fmt.Errorf("list calibre identity claims: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c models.CalibreIdentityClaim
		var key sql.NullString
		var checked string
		if err := rows.Scan(&c.BookID, &c.IdentifierType, &c.IdentifierValue, &c.Status, &key, &checked); err != nil {
			return fmt.Errorf("scan calibre identity claim: %w", err)
		}
		c.EvidenceKey = key.String
		c.CheckedAt = parseDBTime(checked)
		s := out[c.BookID]
		c.CalibreID = s.CalibreID
		s.Claims = append(s.Claims, c)
		out[c.BookID] = s
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate calibre identity claims: %w", err)
	}
	return nil
}
