package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	_ "modernc.org/sqlite"
)

// ErrCalibreIdentifierConflict means the book disappeared, its identifier type
// is no longer missing, or another book already owns the proposed work ID.
// The caller must refresh its review evidence instead of replacing a value.
var ErrCalibreIdentifierConflict = errors.New("calibre identifier changed since review")

var (
	calibreWorkID     = regexp.MustCompile(`^OL[0-9]+W$`)
	calibreProviderID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	calibreDNBID      = regexp.MustCompile(`^[0-9]{1,20}X?$`)
)

// CalibreIdentifierWriter is an explicit, add-only opt-in writer for reviewed
// provider work IDs. It offers no connection, SQL, update, or delete surface.
// The service must require human approval before calling AddMissingIf.
type CalibreIdentifierWriter struct {
	libraryPath string
}

// NewCalibreIdentifierWriter binds the add-only writer to a library root.
// It opens no writable handle until AddMissingIf is called.
func NewCalibreIdentifierWriter(libraryPath string) *CalibreIdentifierWriter {
	return &CalibreIdentifierWriter{libraryPath: libraryPath}
}

// calibreIdentifierAliases mirrors the audit reader's canonical type names.
// Only fixed SQL parameters are returned, never a browser-supplied field name.
func calibreIdentifierAliases(typ string) [4]string {
	switch typ {
	case "openlibrary":
		return [4]string{"openlibrary", "ol", "openlibrary_work", "openlibrary_edition"}
	case "google":
		return [4]string{"google", "googlebooks", "google", "google"}
	default:
		return [4]string{typ, typ, typ, typ}
	}
}

func calibreIdentifierValues(typ, value string) [4]string {
	switch typ {
	case "openlibrary":
		return [4]string{value, "openlibrary:" + value, "/works/" + value, "/books/" + value}
	case "google":
		return [4]string{value, "gb:" + value, value, value}
	case "hardcover":
		return [4]string{value, "hc:" + value, value, value}
	case "dnb":
		return [4]string{value, "dnb:" + value, value, value}
	default:
		return [4]string{value, value, value, value}
	}
}

// AddMissingIf inserts exactly one identifier if the book still exists, the
// canonical type is still absent, and no other book owns that type/value.
// Existing identifiers are never overwritten, even when their value is empty.
// The caller must supply a read-only eligibility check, rerun while a SQLite
// IMMEDIATE transaction prevents any other CWA writer changing the library.
// The callback receives no writable handle. A rejected check rolls back.
func (w *CalibreIdentifierWriter) AddMissingIf(ctx context.Context, calibreID int64, identifierType, value string, validate func(context.Context) error) error {
	if validate == nil {
		return fmt.Errorf("missing identifier approval validation")
	}
	return w.addMissing(ctx, calibreID, identifierType, value, validate)
}

func (w *CalibreIdentifierWriter) addMissing(ctx context.Context, calibreID int64, identifierType, value string, validate func(context.Context) error) error {
	if w == nil || strings.TrimSpace(w.libraryPath) == "" {
		return fmt.Errorf("calibre identifier writer needs a library path")
	}
	if calibreID <= 0 {
		return fmt.Errorf("invalid Calibre book id %d", calibreID)
	}
	valid := false
	switch identifierType {
	case "openlibrary":
		valid = calibreWorkID.MatchString(value)
	case "google", "hardcover":
		valid = calibreProviderID.MatchString(value)
	case "dnb":
		valid = calibreDNBID.MatchString(value)
	}
	if !valid {
		return fmt.Errorf("invalid Calibre work identifier %q=%q", identifierType, value)
	}

	dbPath, err := filepath.Abs(filepath.Join(w.libraryPath, "metadata.db"))
	if err != nil {
		return fmt.Errorf("resolve Calibre identifier library path: %w", err)
	}
	// mode=rw refuses to create a missing metadata.db. All caller-controlled
	// identifiers are bound values, never SQL fragments or URI parameters.
	dbURL := url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=rw&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", dbURL.String())
	if err != nil {
		return fmt.Errorf("open Calibre identifier writer: %w", err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire Calibre identifier connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// database/sql BeginTx defaults to DEFERRED. Reserve SQLite's single
	// writer slot before reading any CAS predicates so they cannot change
	// between the checks and the insert, including across processes.
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin Calibre identifier transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// A canceled request still must release the SQLite write lock.
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	var applicationID int64
	if err := conn.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&applicationID); err != nil {
		return fmt.Errorf("verify Calibre application id: %w", err)
	}
	if applicationID != 0x63616c69 { // "cali"
		return fmt.Errorf("refusing to edit a non-Calibre metadata.db (application_id %#x)", applicationID)
	}
	if err := validate(ctx); err != nil {
		return fmt.Errorf("revalidate approved identifier under Calibre write lock: %w", err)
	}

	var exists int
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM books WHERE id = ?)`, calibreID).Scan(&exists); err != nil {
		return fmt.Errorf("check Calibre book %d: %w", calibreID, err)
	}
	if exists == 0 {
		return fmt.Errorf("%w: Calibre book %d no longer exists", ErrCalibreIdentifierConflict, calibreID)
	}
	aliases := calibreIdentifierAliases(identifierType)
	values := calibreIdentifierValues(identifierType, value)
	// Compare against even malformed/empty existing values; never silently
	// replace a type that a reader might filter out. The fixed alias set
	// matches the audit reader's canonical work/provider type normalization.
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM identifiers WHERE book = ? AND LOWER(TRIM(type)) IN (?, ?, ?, ?))`,
		calibreID, aliases[0], aliases[1], aliases[2], aliases[3]).Scan(&exists); err != nil {
		return fmt.Errorf("check Calibre identifier type for book %d: %w", calibreID, err)
	}
	if exists != 0 {
		return fmt.Errorf("%w: book %d already has %s", ErrCalibreIdentifierConflict, calibreID, identifierType)
	}
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM identifiers WHERE LOWER(TRIM(type)) IN (?, ?, ?, ?)
		AND LOWER(TRIM(val)) IN (LOWER(?), LOWER(?), LOWER(?), LOWER(?)))`,
		aliases[0], aliases[1], aliases[2], aliases[3],
		values[0], values[1], values[2], values[3]).Scan(&exists); err != nil {
		return fmt.Errorf("check Calibre identifier value for book %d: %w", calibreID, err)
	}
	if exists != 0 {
		return fmt.Errorf("%w: %s=%q already belongs to a book", ErrCalibreIdentifierConflict, identifierType, value)
	}

	// The fixed INSERT repeats the book/type/value CAS predicates; no UPDATE,
	// DELETE, generic SQL, or whole-map replacement is exposed by this writer.
	res, err := conn.ExecContext(ctx, `INSERT INTO identifiers (book, type, val)
		SELECT ?, ?, ? WHERE EXISTS (SELECT 1 FROM books WHERE id = ?)
		AND NOT EXISTS (SELECT 1 FROM identifiers WHERE book = ? AND LOWER(TRIM(type)) IN (?, ?, ?, ?))
		AND NOT EXISTS (SELECT 1 FROM identifiers WHERE LOWER(TRIM(type)) IN (?, ?, ?, ?)
		AND LOWER(TRIM(val)) IN (LOWER(?), LOWER(?), LOWER(?), LOWER(?)))`,
		calibreID, identifierType, value, calibreID, calibreID,
		aliases[0], aliases[1], aliases[2], aliases[3],
		aliases[0], aliases[1], aliases[2], aliases[3],
		values[0], values[1], values[2], values[3])
	if err != nil {
		return fmt.Errorf("insert Calibre identifier for book %d: %w", calibreID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("count Calibre identifier insert for book %d: %w", calibreID, err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: book %d or %s changed before insertion", ErrCalibreIdentifierConflict, calibreID, identifierType)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit Calibre identifier for book %d: %w", calibreID, err)
	}
	committed = true
	return nil
}
