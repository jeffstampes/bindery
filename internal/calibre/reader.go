package calibre

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// metadataDB is the filename Calibre uses for its library database. The
// library root directory always contains one at this exact name.
const metadataDB = "metadata.db"

// ErrMissingMetadataDB is returned when the library root does not contain a
// metadata.db. Callers surface this as a user-visible error rather than a
// 500 — it usually means the user pointed library_path at the wrong folder.
var ErrMissingMetadataDB = errors.New("calibre metadata.db not found in library_path")

// ErrBookNotFound is returned when a requested book ID is not found in Calibre.
var ErrBookNotFound = errors.New("calibre book not found")

// ErrAuthoritativeDisabled is returned when authoritative library mode is not enabled
// or no library path is set.
var ErrAuthoritativeDisabled = errors.New("calibre authoritative library disabled or library path empty")

// AuthoritativeLibrary is the read-only view of a live Calibre/CWA library
// used by authoritative mode to query owned books without importing them
// into Bindery's catalogue.
type AuthoritativeLibrary interface {
	Count(ctx context.Context) (int, error)
	GetBook(ctx context.Context, id int64) (*CalibreBook, error)
	Books(ctx context.Context, fn func(CalibreBook) error) error
	AllBooks(ctx context.Context) ([]CalibreBook, error)
	FindByIdentifier(ctx context.Context, idType, idVal string) ([]CalibreBook, error)
	Close() error
}

// CalibreBook is the importer-facing view of one Calibre book row, joined
// with its authors, series, identifiers and formats. Field names match
// Bindery conventions (not Calibre's column names) so the importer can pass
// them through without a second translation.
//
// This is intentionally a flat snapshot: Calibre stores everything in one
// database so a single pass can build the full shape, and the importer
// treats each CalibreBook as an atomic unit when upserting.
type CalibreBook struct {
	CalibreID   int64
	Title       string
	SortTitle   string
	PublishDate *time.Time
	ISBN        string
	Language    string   // primary ISO 639-2 code; empty if unset
	Languages   []string // all Calibre language codes, in item order (bulk snapshots)
	Authors     []CalibreAuthor
	Series      *CalibreSeries
	Formats     []CalibreFormat
	CoverPath   string            // absolute path to cover.jpg if present; empty if not
	LibraryPath string            // absolute path to this book's folder inside the library
	Identifiers map[string]string // Calibre identifiers (e.g. isbn, asin, openlibrary, google, hardcover, etc.)
}

// CalibreAuthor captures a single authors row. Calibre books can have N
// authors; the importer picks the first as the canonical Bindery author
// and records the rest as aliases.
type CalibreAuthor struct {
	CalibreID int64
	Name      string
	Sort      string
}

// CalibreSeries mirrors Calibre's (name, series_index) tuple.
type CalibreSeries struct {
	Name     string
	Position float64
}

// CalibreFormat is one on-disk file for a Calibre book. Calibre lets a
// single book row carry multiple formats (epub + mobi + pdf); each becomes
// a separate Bindery edition.
type CalibreFormat struct {
	Format       string // uppercase file extension: EPUB, MOBI, PDF, ...
	FileName     string // Calibre's on-disk filename (no extension)
	AbsolutePath string // resolved against library root + book path
	SizeBytes    int64
}

// Reader opens a Calibre library's metadata.db read-only and returns
// populated CalibreBook records. It never mutates the Calibre database:
// the handle is opened with `mode=ro`, so a concurrent `calibredb`
// invocation from the same Bindery instance cannot deadlock us, and
// SQLite's normal WAL handling still applies so rows Calibre has committed
// but not yet checkpointed are visible (#2631).
type Reader struct {
	libraryPath string
	db          *sql.DB
}

// OpenReader locates metadata.db inside libraryPath and opens it read-only.
// If metadata.db is absent, returns ErrMissingMetadataDB so the API layer
// can map it to a 400. Callers MUST Close the returned Reader.
func OpenReader(libraryPath string) (*Reader, error) {
	if libraryPath == "" {
		return nil, errors.New("calibre library_path is empty")
	}
	abs, err := filepath.Abs(libraryPath)
	if err != nil {
		return nil, fmt.Errorf("resolve library_path: %w", err)
	}
	dbPath := filepath.Join(abs, metadataDB)
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrMissingMetadataDB, dbPath)
		}
		return nil, fmt.Errorf("stat %s: %w", dbPath, err)
	}
	conn, err := openReadOnly(dbPath)
	if err != nil {
		return nil, err
	}
	return &Reader{libraryPath: abs, db: conn}, nil
}

// OpenAuthoritativeReader opens a Calibre library's metadata.db read-only and
// returns an AuthoritativeLibrary interface.
func OpenAuthoritativeReader(libraryPath string) (AuthoritativeLibrary, error) {
	return OpenReader(libraryPath)
}

// OpenAuthoritativeLibraryFromConfig opens the Calibre library for authoritative reading
// if cfg.AuthoritativeLibraryEnabled is true and cfg.LibraryPath is non-empty.
func OpenAuthoritativeLibraryFromConfig(cfg Config) (AuthoritativeLibrary, error) {
	if !cfg.AuthoritativeLibraryEnabled || strings.TrimSpace(cfg.LibraryPath) == "" {
		return nil, ErrAuthoritativeDisabled
	}
	return OpenReader(cfg.LibraryPath)
}

// openReadOnly opens dbPath with `mode=ro` and verifies a read actually
// works. Plain read-only is what we want: SQLite honours the -wal file, so
// edits made by a long running Calibre, Calibre-Web-Automated or a
// `calibredb` call that have not been checkpointed into metadata.db yet
// are still visible (#2631). The reader used to add `immutable=1`, which
// tells SQLite the file cannot change and makes it skip the WAL entirely;
// that silently served a snapshot as of the last checkpoint, and any
// author or book the user had just fixed in Calibre came back on the next
// import.
//
// A WAL database opened without `immutable=1` needs working shared memory
// for its metadata.db-shm index. Two common layouts cannot provide it: a
// library directory mounted read-only with no -shm present (SQLite cannot
// create one), and a library on NFS or SMB, where SQLite's WAL shared
// memory does not work at all. Those fail with a spread of result codes
// (READONLY, CANTOPEN, the IOERR_SHM family, BUSY), so rather than try to
// classify them, any probe failure on the plain open retries with
// `immutable=1` and warns, naming the consequence. That keeps such
// libraries importable, as they were before, without the stale read being
// silent. Only when the immutable open cannot read either (not a SQLite
// file, unreadable, corrupt) does the error reach the caller.
func openReadOnly(dbPath string) (*sql.DB, error) {
	return openReadOnlyWith(dbPath, probeRead)
}

// probeFunc checks that conn can serve a read. immutable says which of the
// two opens is being probed; the production probe ignores it, and tests use
// it to fail only the plain open.
type probeFunc func(ctx context.Context, conn *sql.DB, immutable bool) error

func openReadOnlyWith(dbPath string, probe probeFunc) (*sql.DB, error) {
	// OpenReader has no context parameter and its one caller runs the
	// import in the background, so bound the probe locally: a metadata.db
	// that cannot answer a trivial query in this long is broken, not busy.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	// sql.Open is lazy; the -shm requirement only surfaces on the first
	// read transaction, so probe with a real query rather than Ping.
	probeErr := probe(ctx, conn, false)
	if probeErr == nil {
		return conn, nil
	}
	_ = conn.Close()

	conn, err = sql.Open("sqlite", "file:"+dbPath+"?mode=ro&immutable=1")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	if err := probe(ctx, conn, true); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open %s: %w (read-only open failed first with: %w)", dbPath, err, probeErr)
	}
	slog.Warn("calibre: cannot read metadata.db with WAL support, so it was opened immutable instead. "+
		"Edits made in Calibre will not be visible to Bindery until Calibre checkpoints its WAL. "+
		"Usual causes: the library directory is not writable to Bindery and metadata.db-shm does not exist, "+
		"or the library is on a network filesystem (NFS, SMB) that cannot share the WAL index.",
		"path", dbPath, "error", probeErr)
	return conn, nil
}

// probeRead runs the cheapest query that forces SQLite to actually open
// the database file and, in WAL mode, its -shm.
func probeRead(ctx context.Context, conn *sql.DB, _ bool) error {
	var n int
	return conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master`).Scan(&n)
}

// Close releases the SQLite handle. Safe to call on a nil receiver so the
// defer-close pattern works even when OpenReader failed.
func (r *Reader) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

// LibraryPath returns the absolute path the reader was opened against.
// Used by the importer to resolve relative format paths.
func (r *Reader) LibraryPath() string { return r.libraryPath }

// Count returns the total number of books the library contains. Used by
// the importer to seed its progress total before the streaming read.
func (r *Reader) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM books`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count books: %w", err)
	}
	return n, nil
}

// Books streams every book row from the library, one at a time, into fn.
// Returning an error from fn aborts the walk and surfaces that error to
// the caller.
//
// Implementation note: we materialise the headline books list first (all
// columns from the books + series join) and close the rows cursor before
// issuing per-book follow-up queries for authors, formats, and ISBNs.
// Calibre ships one SQLite file shared across the app, so nested queries
// on a single connection deadlock — buffering the book list keeps the
// reader compatible with constrained connection pools.
func (r *Reader) Books(ctx context.Context, fn func(CalibreBook) error) error {
	headers, err := r.listBookHeaders(ctx)
	if err != nil {
		return err
	}

	for _, cb := range headers {
		authors, err := r.loadAuthors(ctx, cb.CalibreID)
		if err != nil {
			return err
		}
		cb.Authors = authors

		cb.Formats, err = r.loadFormats(ctx, cb.CalibreID, cb.LibraryPath)
		if err != nil {
			return err
		}

		cb.ISBN, err = r.loadISBN(ctx, cb.CalibreID)
		if err != nil {
			return err
		}

		cb.Language, err = r.loadLanguage(ctx, cb.CalibreID)
		if err != nil {
			return err
		}

		if cover := filepath.Join(cb.LibraryPath, "cover.jpg"); fileExists(cover) {
			cb.CoverPath = cover
		}

		if err := fn(cb); err != nil {
			return err
		}
	}
	return nil
}

// listBookHeaders reads the books table + series join into a slice so the
// outer iterator never holds rows open while child queries fire.
func (r *Reader) listBookHeaders(ctx context.Context) ([]CalibreBook, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT b.id, COALESCE(b.title, ''), COALESCE(b.sort, ''), b.pubdate,
		       COALESCE(b.path, ''), b.series_index, COALESCE(s.name, '')
		FROM books b
		LEFT JOIN books_series_link bsl ON bsl.book = b.id
		LEFT JOIN series s               ON s.id   = bsl.series
		ORDER BY b.id`)
	if err != nil {
		return nil, fmt.Errorf("query books: %w", err)
	}
	defer rows.Close()

	var out []CalibreBook
	for rows.Next() {
		var (
			cb          CalibreBook
			pubdate     sql.NullString
			relPath     string
			seriesIndex sql.NullString
			seriesName  string
		)
		if err := rows.Scan(&cb.CalibreID, &cb.Title, &cb.SortTitle, &pubdate,
			&relPath, &seriesIndex, &seriesName); err != nil {
			return nil, fmt.Errorf("scan book: %w", err)
		}
		cb.PublishDate = parseCalibreDate(pubdate.String)
		cb.LibraryPath = filepath.Join(r.libraryPath, relPath)
		if seriesName != "" {
			cb.Series = &CalibreSeries{
				Name:     seriesName,
				Position: parseSeriesIndex(seriesIndex.String),
			}
		}
		out = append(out, cb)
	}
	return out, rows.Err()
}

func (r *Reader) loadAuthors(ctx context.Context, bookID int64) ([]CalibreAuthor, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.id, a.name, COALESCE(a.sort, '')
		FROM authors a
		JOIN books_authors_link bal ON bal.author = a.id
		WHERE bal.book = ?
		ORDER BY bal.id`, bookID)
	if err != nil {
		return nil, fmt.Errorf("load authors for book %d: %w", bookID, err)
	}
	defer rows.Close()

	var out []CalibreAuthor
	for rows.Next() {
		var a CalibreAuthor
		if err := rows.Scan(&a.CalibreID, &a.Name, &a.Sort); err != nil {
			return nil, fmt.Errorf("scan author: %w", err)
		}
		// Calibre stores a literal comma in an author name as "|": a comma
		// separates authors in its comma-joined author columns, and the
		// authors table keeps that escaping so the joined form stays
		// unambiguous. Calibre's own read path turns the pipe back into a
		// comma per author (calibre/db/write.py get_adapter), and both the
		// name and sort columns carry the escaped form. Doing it here, row by
		// row, keeps the books_authors_link rows as separate authors (#2666).
		a.Name = strings.ReplaceAll(a.Name, "|", ",")
		a.Sort = strings.ReplaceAll(a.Sort, "|", ",")
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Reader) loadFormats(ctx context.Context, bookID int64, bookPath string) ([]CalibreFormat, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT format, name, uncompressed_size
		FROM data WHERE book = ?
		ORDER BY id`, bookID)
	if err != nil {
		return nil, fmt.Errorf("load formats for book %d: %w", bookID, err)
	}
	defer rows.Close()

	var out []CalibreFormat
	for rows.Next() {
		var f CalibreFormat
		if err := rows.Scan(&f.Format, &f.FileName, &f.SizeBytes); err != nil {
			return nil, fmt.Errorf("scan format: %w", err)
		}
		f.Format = strings.ToUpper(strings.TrimSpace(f.Format))
		// Calibre stores the format file as `<name>.<format>` inside the
		// per-book directory. Build the absolute path so callers don't have
		// to replicate the convention.
		if f.Format != "" && f.FileName != "" && bookPath != "" {
			f.AbsolutePath = filepath.Join(bookPath, f.FileName+"."+strings.ToLower(f.Format))
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// loadISBN returns the first ISBN identifier (type='isbn') for the book, or
// empty string if none exists. Calibre occasionally stores two ISBNs on one
// book (one per format); we keep the first since Bindery's editions model
// already captures the variance.
func (r *Reader) loadISBN(ctx context.Context, bookID int64) (string, error) {
	var isbn string
	row := r.db.QueryRowContext(ctx,
		`SELECT val FROM identifiers WHERE book = ? AND type = 'isbn' ORDER BY id LIMIT 1`, bookID)
	err := row.Scan(&isbn)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load isbn for book %d: %w", bookID, err)
	}
	return isbn, nil
}

// loadLanguage returns the primary ISO 639-2 language code for the book from
// Calibre's languages table, or empty string if none is set. Calibre stores
// codes as three-letter ISO 639-2 strings (e.g. "eng", "deu", "fra") which
// are the same format Bindery uses, so no translation is needed.
func (r *Reader) loadLanguage(ctx context.Context, bookID int64) (string, error) {
	var lang string
	row := r.db.QueryRowContext(ctx, `
		SELECT l.lang_code
		FROM books_languages_link bll
		JOIN languages l ON l.id = bll.lang_code
		WHERE bll.book = ?
		ORDER BY bll.item_order
		LIMIT 1`, bookID)
	err := row.Scan(&lang)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		// Older Calibre libraries (pre-0.7.x) don't have the languages table.
		if strings.Contains(err.Error(), "no such table") {
			return "", nil
		}
		return "", fmt.Errorf("load language for book %d: %w", bookID, err)
	}
	return lang, nil
}

// parseSeriesIndex reads Calibre's series_index column. The column is declared
// REAL NOT NULL DEFAULT 1.0, but SQLite assigns a storage class per value, not
// per column, so a library edited by an older Calibre or by a third-party tool
// can hold the text "" (or any other non-numeric string) there. Scanning that
// straight into a float aborted the whole import on the first such row (#2720),
// which is a worse outcome than losing one position, so an unreadable value
// reads as 0 — the same "no position" the importer already uses for a book with
// no series_index at all (attachBookToSeries only writes a position when it is
// greater than zero).
func parseSeriesIndex(raw string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0
	}
	return f
}

// parseCalibreDate tolerates the three pubdate formats Calibre has shipped:
// RFC3339, RFC3339 with a +00:00 offset (pre-2020), and an all-day date
// without a time component. Returns nil for the special sentinel 0101-01-01
// Calibre uses to mean "no pubdate".
func parseCalibreDate(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "0101-01-01") {
		return nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.000000-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// GetBook returns the single CalibreBook record for the given Calibre ID,
// including its authors, series, formats, identifiers, language, and cover path.
// Returns ErrBookNotFound if no book with that ID exists.
func (r *Reader) GetBook(ctx context.Context, id int64) (*CalibreBook, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("reader is nil or closed")
	}

	row := r.db.QueryRowContext(ctx, `
		SELECT b.id, COALESCE(b.title, ''), COALESCE(b.sort, ''), b.pubdate,
		       COALESCE(b.path, ''), b.series_index, COALESCE(s.name, '')
		FROM books b
		LEFT JOIN books_series_link bsl ON bsl.book = b.id
		LEFT JOIN series s               ON s.id   = bsl.series
		WHERE b.id = ?`, id)

	var (
		cb          CalibreBook
		pubdate     sql.NullString
		relPath     string
		seriesIndex sql.NullString
		seriesName  string
	)
	if err := row.Scan(&cb.CalibreID, &cb.Title, &cb.SortTitle, &pubdate,
		&relPath, &seriesIndex, &seriesName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrBookNotFound
		}
		return nil, fmt.Errorf("get book %d: %w", id, err)
	}

	cb.PublishDate = parseCalibreDate(pubdate.String)
	cb.LibraryPath = filepath.Join(r.libraryPath, relPath)
	if seriesName != "" {
		cb.Series = &CalibreSeries{
			Name:     seriesName,
			Position: parseSeriesIndex(seriesIndex.String),
		}
	}

	authors, err := r.loadAuthors(ctx, id)
	if err != nil {
		return nil, err
	}
	cb.Authors = authors

	formats, err := r.loadFormats(ctx, id, cb.LibraryPath)
	if err != nil {
		return nil, err
	}
	cb.Formats = formats

	identifiers, err := r.loadIdentifiers(ctx, id)
	if err != nil {
		return nil, err
	}
	cb.Identifiers = identifiers
	if isbn, ok := identifiers["isbn"]; ok && isbn != "" {
		cb.ISBN = isbn
	} else {
		isbn, err := r.loadISBN(ctx, id)
		if err != nil {
			return nil, err
		}
		cb.ISBN = isbn
	}

	lang, err := r.loadLanguage(ctx, id)
	if err != nil {
		return nil, err
	}
	cb.Language = lang

	if cover := filepath.Join(cb.LibraryPath, "cover.jpg"); fileExists(cover) {
		cb.CoverPath = cover
	}

	return &cb, nil
}

// FindByIdentifier searches the live Calibre library for books matching an
// identifier (e.g. idType "isbn", "asin", "goodreads", "openlibrary", "hardcover").
// Returns a slice of matching CalibreBook records (empty slice if none matched).
func (r *Reader) FindByIdentifier(ctx context.Context, idType, idVal string) ([]CalibreBook, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("reader is nil or closed")
	}

	idTypeClean := cleanIdentifierType(idType)
	idValClean := cleanIdentifierValue(idVal)
	if idTypeClean == "" || idValClean == "" {
		return nil, nil
	}

	idValLower := strings.ToLower(idValClean)
	idValStripped := strings.ReplaceAll(idValLower, "-", "")

	var rows *sql.Rows
	var err error

	if idTypeClean == "isbn" && idValStripped != idValLower {
		rows, err = r.db.QueryContext(ctx, `
			SELECT DISTINCT book
			FROM identifiers
			WHERE LOWER(type) = ? AND (LOWER(val) = ? OR LOWER(val) = ? OR REPLACE(LOWER(val), '-', '') = ?)
			ORDER BY book`, idTypeClean, idValLower, idValStripped, idValStripped)
	} else if idTypeClean == "isbn" {
		rows, err = r.db.QueryContext(ctx, `
			SELECT DISTINCT book
			FROM identifiers
			WHERE LOWER(type) = ? AND (LOWER(val) = ? OR REPLACE(LOWER(val), '-', '') = ?)
			ORDER BY book`, idTypeClean, idValLower, idValStripped)
	} else {
		rows, err = r.db.QueryContext(ctx, `
			SELECT DISTINCT book
			FROM identifiers
			WHERE LOWER(type) = ? AND LOWER(val) = ?
			ORDER BY book`, idTypeClean, idValLower)
	}
	if err != nil {
		return nil, fmt.Errorf("find books by identifier (%s=%s): %w", idType, idVal, err)
	}
	defer rows.Close()

	var bookIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan identifier book id: %w", err)
		}
		bookIDs = append(bookIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []CalibreBook
	for _, id := range bookIDs {
		b, err := r.GetBook(ctx, id)
		if err != nil {
			if errors.Is(err, ErrBookNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, *b)
	}
	return out, nil
}

// AllBooks returns all books in the Calibre library using bulk SQL queries,
// avoiding per-book N+1 roundtrips. This is designed for high performance
// on large (~80,000 book) libraries.
func (r *Reader) AllBooks(ctx context.Context) ([]CalibreBook, error) {
	return r.allBooks(ctx, true)
}

// AllBooksMetadata reads the same bulk SQL snapshot without probing every
// book's cover file. Advisory audits do not use cover paths, and a remote
// library's per-book filesystem stats would dominate the database read.
func (r *Reader) AllBooksMetadata(ctx context.Context) ([]CalibreBook, error) {
	return r.allBooks(ctx, false)
}

func (r *Reader) allBooks(ctx context.Context, includeCovers bool) ([]CalibreBook, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("reader is nil or closed")
	}

	headers, err := r.listBookHeaders(ctx)
	if err != nil {
		return nil, err
	}
	if len(headers) == 0 {
		return nil, nil
	}

	authorsMap, err := r.loadAllAuthors(ctx)
	if err != nil {
		return nil, err
	}

	formatsMap, err := r.loadAllFormats(ctx)
	if err != nil {
		return nil, err
	}

	identifiersMap, err := r.loadAllIdentifiers(ctx)
	if err != nil {
		return nil, err
	}

	languagesMap, err := r.loadAllLanguages(ctx)
	if err != nil {
		return nil, err
	}

	for i := range headers {
		id := headers[i].CalibreID
		headers[i].Authors = authorsMap[id]
		headers[i].Formats = formatsMap[id]
		if ids, ok := identifiersMap[id]; ok {
			headers[i].Identifiers = ids
			headers[i].ISBN = ids["isbn"]
		} else {
			headers[i].Identifiers = make(map[string]string)
		}
		if languages := languagesMap[id]; len(languages) > 0 {
			headers[i].Language = languages[0]
			headers[i].Languages = languages
		}

		if includeCovers {
			if cover := filepath.Join(headers[i].LibraryPath, "cover.jpg"); fileExists(cover) {
				headers[i].CoverPath = cover
			}
		}
	}

	return headers, nil
}

func (r *Reader) loadIdentifiers(ctx context.Context, bookID int64) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT type, val
		FROM identifiers
		WHERE book = ?
		ORDER BY id`, bookID)
	if err != nil {
		return nil, fmt.Errorf("load identifiers for book %d: %w", bookID, err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var typ, val string
		if err := rows.Scan(&typ, &val); err != nil {
			return nil, fmt.Errorf("scan identifier: %w", err)
		}
		typ = cleanIdentifierType(typ)
		val = cleanIdentifierValue(val)
		if typ != "" && val != "" {
			if _, exists := out[typ]; !exists {
				out[typ] = val
			}
		}
	}
	return out, rows.Err()
}

func (r *Reader) loadAllAuthors(ctx context.Context) (map[int64][]CalibreAuthor, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT bal.book, a.id, a.name, COALESCE(a.sort, '')
		FROM authors a
		JOIN books_authors_link bal ON bal.author = a.id
		ORDER BY bal.book, bal.id`)
	if err != nil {
		return nil, fmt.Errorf("load all authors: %w", err)
	}
	defer rows.Close()

	out := make(map[int64][]CalibreAuthor)
	for rows.Next() {
		var (
			bookID int64
			a      CalibreAuthor
		)
		if err := rows.Scan(&bookID, &a.CalibreID, &a.Name, &a.Sort); err != nil {
			return nil, fmt.Errorf("scan author: %w", err)
		}
		a.Name = strings.ReplaceAll(a.Name, "|", ",")
		a.Sort = strings.ReplaceAll(a.Sort, "|", ",")
		out[bookID] = append(out[bookID], a)
	}
	return out, rows.Err()
}

func (r *Reader) loadAllFormats(ctx context.Context) (map[int64][]CalibreFormat, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT d.book, d.format, d.name, d.uncompressed_size, COALESCE(b.path, '')
		FROM data d
		JOIN books b ON b.id = d.book
		ORDER BY d.book, d.id`)
	if err != nil {
		return nil, fmt.Errorf("load all formats: %w", err)
	}
	defer rows.Close()

	out := make(map[int64][]CalibreFormat)
	for rows.Next() {
		var (
			bookID  int64
			f       CalibreFormat
			relPath string
		)
		if err := rows.Scan(&bookID, &f.Format, &f.FileName, &f.SizeBytes, &relPath); err != nil {
			return nil, fmt.Errorf("scan format: %w", err)
		}
		f.Format = strings.ToUpper(strings.TrimSpace(f.Format))
		if f.Format != "" && f.FileName != "" && relPath != "" {
			bookPath := filepath.Join(r.libraryPath, relPath)
			f.AbsolutePath = filepath.Join(bookPath, f.FileName+"."+strings.ToLower(f.Format))
		}
		out[bookID] = append(out[bookID], f)
	}
	return out, rows.Err()
}

func (r *Reader) loadAllIdentifiers(ctx context.Context) (map[int64]map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT book, type, val
		FROM identifiers
		ORDER BY book, id`)
	if err != nil {
		return nil, fmt.Errorf("load all identifiers: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]map[string]string)
	for rows.Next() {
		var (
			bookID   int64
			typ, val string
		)
		if err := rows.Scan(&bookID, &typ, &val); err != nil {
			return nil, fmt.Errorf("scan identifier: %w", err)
		}
		typ = cleanIdentifierType(typ)
		val = cleanIdentifierValue(val)
		if typ != "" && val != "" {
			if _, ok := out[bookID]; !ok {
				out[bookID] = make(map[string]string)
			}
			if _, exists := out[bookID][typ]; !exists {
				out[bookID][typ] = val
			}
		}
	}
	return out, rows.Err()
}

func (r *Reader) loadAllLanguages(ctx context.Context) (map[int64][]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT bll.book, l.lang_code
		FROM books_languages_link bll
		JOIN languages l ON l.id = bll.lang_code
		ORDER BY bll.book, bll.item_order`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return make(map[int64][]string), nil
		}
		return nil, fmt.Errorf("load all languages: %w", err)
	}
	defer rows.Close()

	out := make(map[int64][]string)
	for rows.Next() {
		var (
			bookID int64
			lang   string
		)
		if err := rows.Scan(&bookID, &lang); err != nil {
			return nil, fmt.Errorf("scan language: %w", err)
		}
		out[bookID] = append(out[bookID], lang)
	}
	return out, rows.Err()
}
