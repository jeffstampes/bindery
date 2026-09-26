package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	_ "modernc.org/sqlite"
)

// CalibreMismatchTag is the only Calibre metadata value Bindery manages for
// existing owned books. This writer deliberately has no field or tag argument.
const CalibreMismatchTag = "BinderyMismatch"

// calibreTagBatchSize leaves room for the fixed tag name within SQLite's
// historical 999-variable limit and bounds each write transaction.
const calibreTagBatchSize = 256

// CalibreTagWriter is separate from the authoritative, read-only Calibre reader.
// Its sole mutation is inserting/deleting links to CalibreMismatchTag. In
// particular it never replaces a book's tag set or updates Calibre's books row.
type CalibreTagWriter struct {
	libraryPath string
}

// NewCalibreTagWriter binds the fixed-tag writer to one configured library.
// It does not open a writable handle until ReconcileMismatch is called.
func NewCalibreTagWriter(libraryPath string) *CalibreTagWriter {
	return &CalibreTagWriter{libraryPath: libraryPath}
}

// ReconcileMismatch compares the desired set with live tag links in one bulk
// read. A missing or inaccessible metadata.db fails closed (mode=rw never
// creates a new database). Changes are committed in bounded batches; a partial
// failure is safe to retry on the next audit or review action.
func (w *CalibreTagWriter) ReconcileMismatch(ctx context.Context, desired map[int64]bool) (int, error) {
	if w == nil || w.libraryPath == "" {
		return 0, fmt.Errorf("calibre tag writer needs a library path")
	}
	dbPath, err := filepath.Abs(filepath.Join(w.libraryPath, "metadata.db"))
	if err != nil {
		return 0, fmt.Errorf("resolve Calibre tag library path: %w", err)
	}
	dbURL := url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=rw&_pragma=busy_timeout(5000)"}
	conn, err := sql.Open("sqlite", dbURL.String())
	if err != nil {
		return 0, fmt.Errorf("open Calibre tag writer: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Calibre marks metadata.db with "cali". Never mutate a different
	// SQLite database just because it has similarly named tables.
	var applicationID int64
	if err := conn.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&applicationID); err != nil {
		return 0, fmt.Errorf("verify Calibre application id: %w", err)
	}
	if applicationID != 0x63616c69 {
		return 0, fmt.Errorf("refusing to tag a non-Calibre metadata.db (application_id %#x)", applicationID)
	}

	// Validate the required schema even when desired is empty: an opt-in must
	// not silently report success against the wrong database or an absent file.
	rows, err := conn.QueryContext(ctx, `
		SELECT l.book FROM books_tags_link l JOIN tags t ON t.id = l.tag
		WHERE t.name = ?`, CalibreMismatchTag)
	if err != nil {
		return 0, fmt.Errorf("read Calibre mismatch links: %w", err)
	}
	defer rows.Close()
	present := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan Calibre mismatch link: %w", err)
		}
		present[id] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("iterate Calibre mismatch links: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close Calibre mismatch links: %w", err)
	}

	// Add and remove only differences. Neither statement touches links for
	// other tags; no whole-collection replacement can discard unrelated
	// links. A separate Calibre process may cache a stale book, however.
	changed := 0
	toAdd := make([]int64, 0, len(desired))
	for id, needed := range desired {
		if needed && id > 0 && !present[id] {
			toAdd = append(toAdd, id)
		}
	}
	slices.Sort(toAdd)
	for start := 0; start < len(toAdd); start += calibreTagBatchSize {
		if err := ctx.Err(); err != nil {
			return changed, err
		}
		end := min(start+calibreTagBatchSize, len(toAdd))
		batch := toAdd[start:end]
		placeholders, args := calibreTagBatchArgs(batch)
		// Tag creation and link insertion share one transaction. The EXISTS
		// guard excludes deleted books; if none was linked, rolling back also
		// prevents an unused tag after a concurrent deletion.
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return changed, fmt.Errorf("begin Calibre mismatch add batch at %d: %w", batch[0], err)
		}
		//nolint:gosec // Only the number of bound placeholders is concatenated; values are parameters.
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO tags (name)
			SELECT ? WHERE EXISTS (SELECT 1 FROM books WHERE id IN (`+placeholders+`))`, args...)
		var added int64
		if err == nil {
			var res sql.Result
			//nolint:gosec // Only the number of bound placeholders is concatenated; values are parameters.
			res, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO books_tags_link (book, tag)
				SELECT b.id, t.id FROM books b JOIN tags t ON t.name = ?
				WHERE b.id IN (`+placeholders+`)`, args...)
			if err == nil {
				added, err = res.RowsAffected()
			}
		}
		if err != nil {
			_ = tx.Rollback()
			return changed, fmt.Errorf("add Calibre mismatch batch at %d: %w", batch[0], err)
		}
		if added == 0 {
			_ = tx.Rollback()
			continue
		}
		if err := tx.Commit(); err != nil {
			return changed, fmt.Errorf("commit Calibre mismatch add batch at %d: %w", batch[0], err)
		}
		changed += int(added)
	}

	toRemove := make([]int64, 0, len(present))
	for id := range present {
		if !desired[id] {
			toRemove = append(toRemove, id)
		}
	}
	slices.Sort(toRemove)
	for start := 0; start < len(toRemove); start += calibreTagBatchSize {
		if err := ctx.Err(); err != nil {
			return changed, err
		}
		end := min(start+calibreTagBatchSize, len(toRemove))
		batch := toRemove[start:end]
		placeholders, args := calibreTagBatchArgs(batch)
		// One atomic DELETE per batch; the tag subquery fixes the write
		// boundary even when unrelated tags share the same books.
		//nolint:gosec // Only the number of bound placeholders is concatenated; values are parameters.
		res, err := conn.ExecContext(ctx, `DELETE FROM books_tags_link
			WHERE tag IN (SELECT id FROM tags WHERE name = ?)
			AND book IN (`+placeholders+`)`, args...)
		if err != nil {
			return changed, fmt.Errorf("remove Calibre mismatch batch at %d: %w", batch[0], err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return changed, fmt.Errorf("count Calibre mismatch removal batch at %d: %w", batch[0], err)
		}
		changed += int(n)
	}
	return changed, nil
}

// calibreTagBatchArgs builds only placeholders from the bounded ID count;
// every ID and the fixed tag name are bound values, never SQL fragments.
func calibreTagBatchArgs(ids []int64) (string, []any) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, CalibreMismatchTag)
	for _, id := range ids {
		args = append(args, id)
	}
	return placeholders, args
}
