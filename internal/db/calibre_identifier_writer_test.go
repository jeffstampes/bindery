package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// Tests supply an explicit validator; production supplies a live eligibility recheck.
func allowIdentifierTestWrite(context.Context) error { return nil }

// Every test library is a disposable metadata.db, never a configured user library.
func identifierLibrary(t *testing.T) (string, *sql.DB) {
	t.Helper()
	root := t.TempDir()
	conn, err := sql.Open("sqlite", filepath.Join(root, "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, stmt := range []string{
		`PRAGMA application_id = 0x63616c69`,
		`CREATE TABLE books (id INTEGER PRIMARY KEY, title TEXT NOT NULL, last_modified TEXT NOT NULL)`,
		`CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE books_tags_link (book INTEGER NOT NULL, tag INTEGER NOT NULL)`,
		`CREATE TABLE identifiers (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, type TEXT NOT NULL DEFAULT 'isbn', val TEXT NOT NULL, UNIQUE(book, type))`,
		`INSERT INTO books (id, title, last_modified) VALUES (1, 'Curated', 'yesterday'), (2, 'Other', 'today')`,
		`INSERT INTO tags (id, name) VALUES (1, 'Favorite')`,
		`INSERT INTO books_tags_link (book, tag) VALUES (1, 1), (2, 1)`,
		`INSERT INTO identifiers (book, type, val) VALUES (1, 'isbn', '9780306406157'), (2, 'google', 'otherVolume')`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("fixture statement %q: %v", stmt, err)
		}
	}
	return root, conn
}

func identifierRows(t *testing.T, conn *sql.DB) []string {
	t.Helper()
	rows, err := conn.Query(`SELECT CAST(book AS TEXT) || ':' || type || ':' || val FROM identifiers ORDER BY book, type`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCalibreIdentifierWriter_AddMissingOnly(t *testing.T) {
	root, conn := identifierLibrary(t)
	writer := NewCalibreIdentifierWriter(root)
	ctx := context.Background()
	for _, item := range []struct{ typ, val string }{
		{"openlibrary", "OL123W"}, {"google", "zyTCAlFPjgYC"},
		{"hardcover", "dune"}, {"dnb", "123456789"},
	} {
		if err := writer.AddMissingIf(ctx, 1, item.typ, item.val, allowIdentifierTestWrite); err != nil {
			t.Fatalf("add %s: %v", item.typ, err)
		}
	}
	want := []string{
		"1:dnb:123456789", "1:google:zyTCAlFPjgYC", "1:hardcover:dune",
		"1:isbn:9780306406157", "1:openlibrary:OL123W", "2:google:otherVolume",
	}
	if got := identifierRows(t, conn); !reflect.DeepEqual(got, want) {
		t.Fatalf("identifiers = %v, want %v", got, want)
	}
	var books, tags, links string
	if err := conn.QueryRow(`SELECT group_concat(title || ':' || last_modified, '|') FROM books ORDER BY id`).Scan(&books); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT group_concat(name) FROM tags`).Scan(&tags); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT group_concat(book || ':' || tag, '|') FROM books_tags_link`).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if books != "Curated:yesterday|Other:today" || tags != "Favorite" || links != "1:1|2:1" {
		t.Fatalf("unrelated metadata changed: books=%q tags=%q links=%q", books, tags, links)
	}
}

func TestCalibreIdentifierWriter_RejectsStaleAndDuplicate(t *testing.T) {
	root, conn := identifierLibrary(t)
	writer := NewCalibreIdentifierWriter(root)
	ctx := context.Background()
	before := identifierRows(t, conn)
	for _, tc := range []struct {
		name string
		id   int64
		typ  string
		val  string
	}{
		{"existing type, even with matching value", 2, "google", "otherVolume"},
		{"existing type, different value", 2, "google", "newVolume"},
		{"duplicate work on another book", 1, "google", "otherVolume"},
		{"deleted book", 99, "openlibrary", "OL123W"},
		{"invalid book ID", 0, "openlibrary", "OL123W"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := writer.AddMissingIf(ctx, tc.id, tc.typ, tc.val, allowIdentifierTestWrite); !errors.Is(err, ErrCalibreIdentifierConflict) && tc.id > 0 {
				t.Fatalf("stale or duplicate addition: %v, want ErrCalibreIdentifierConflict", err)
			} else if err == nil {
				t.Fatal("invalid or conflicting addition accepted")
			}
			if got := identifierRows(t, conn); !reflect.DeepEqual(got, before) {
				t.Fatalf("failure changed identifiers: %v", got)
			}
		})
	}
	// A candidate approved when the type was absent is stale by application time.
	if _, err := conn.Exec(`INSERT INTO identifiers (book, type, val) VALUES (1, 'hardcover', 'curator-value')`); err != nil {
		t.Fatal(err)
	}
	if err := writer.AddMissingIf(ctx, 1, "hardcover", "1001", allowIdentifierTestWrite); !errors.Is(err, ErrCalibreIdentifierConflict) {
		t.Fatalf("curator's identifier conflict: %v", err)
	}
	want := []string{"1:hardcover:curator-value", "1:isbn:9780306406157", "2:google:otherVolume"}
	if got := identifierRows(t, conn); !reflect.DeepEqual(got, want) {
		t.Fatalf("stale write changed identifiers: %v, want %v", got, want)
	}
}

func TestCalibreIdentifierWriter_CaseFoldedCASAndEmptyExistingValue(t *testing.T) {
	root, conn := identifierLibrary(t)
	if _, err := conn.Exec(`INSERT INTO identifiers (book, type, val) VALUES (1, 'GOOGLE', '')`); err != nil {
		t.Fatal(err)
	}
	writer := NewCalibreIdentifierWriter(root)
	if err := writer.AddMissingIf(context.Background(), 1, "google", "freshVolume", allowIdentifierTestWrite); !errors.Is(err, ErrCalibreIdentifierConflict) {
		t.Fatalf("empty but already occupied type: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO identifiers (book, type, val) VALUES (2, 'OPENLIBRARY', 'OL321W')`); err != nil {
		t.Fatal(err)
	}
	if err := writer.AddMissingIf(context.Background(), 1, "openlibrary", "OL321W", allowIdentifierTestWrite); !errors.Is(err, ErrCalibreIdentifierConflict) {
		t.Fatalf("existing work identity with noncanonical type: %v", err)
	}
	var count int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM identifiers`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("conflicts changed identifier rows: %d, %v", count, err)
	}
}

func TestCalibreIdentifierWriter_RejectsCanonicalTypeAliases(t *testing.T) {
	root, conn := identifierLibrary(t)
	for _, stmt := range []string{
		`INSERT INTO identifiers (book, type, val) VALUES (1, 'googlebooks', 'existing')`,
		`INSERT INTO identifiers (book, type, val) VALUES (2, 'ol', 'OL707W')`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	writer := NewCalibreIdentifierWriter(root)
	for _, tc := range []struct {
		book     int64
		typ, val string
	}{
		{1, "google", "newVolume"},   // Same logical field is occupied.
		{1, "openlibrary", "OL707W"}, // Same work ID belongs to another book.
		{2, "openlibrary", "OL900W"}, // Existing alias occupies the work field.
	} {
		if err := writer.AddMissingIf(context.Background(), tc.book, tc.typ, tc.val, allowIdentifierTestWrite); !errors.Is(err, ErrCalibreIdentifierConflict) {
			t.Fatalf("alias %d %s=%s: %v", tc.book, tc.typ, tc.val, err)
		}
	}
}

func TestCalibreIdentifierWriter_RejectsNormalizedValuesAcrossBooks(t *testing.T) {
	root, conn := identifierLibrary(t)
	for _, stmt := range []string{
		`INSERT INTO identifiers (book, type, val) VALUES (2, 'hardcover', 'hc:known-slug')`,
		`INSERT INTO identifiers (book, type, val) VALUES (2, 'dnb', 'dnb:123456789')`,
		`INSERT INTO identifiers (book, type, val) VALUES (2, 'ol', '/works/OL404W')`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ typ, val string }{
		{"hardcover", "known-slug"}, {"dnb", "123456789"},
		{"google", "otherVolume"}, {"openlibrary", "OL404W"},
	} {
		if err := NewCalibreIdentifierWriter(root).AddMissingIf(context.Background(), 1, tc.typ, tc.val, allowIdentifierTestWrite); !errors.Is(err, ErrCalibreIdentifierConflict) {
			t.Errorf("duplicate normalized %s=%s was not rejected: %v", tc.typ, tc.val, err)
		}
	}
}

func TestCalibreIdentifierWriter_ValidationRunsUnderReservedWriteLock(t *testing.T) {
	root, conn := identifierLibrary(t)
	writer := NewCalibreIdentifierWriter(root)
	ctx := context.Background()
	if err := writer.AddMissingIf(ctx, 1, "openlibrary", "OL123W", nil); err == nil {
		t.Fatal("missing approval validation accepted")
	}
	stale := errors.New("reviewed book changed")
	called := false
	err := writer.AddMissingIf(ctx, 1, "openlibrary", "OL123W", func(ctx context.Context) error {
		called = true
		var title string
		if err := conn.QueryRowContext(ctx, `SELECT title FROM books WHERE id = 1`).Scan(&title); err != nil {
			return err
		}
		if title != "Curated" {
			return stale
		}
		otherCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		if _, err := conn.ExecContext(otherCtx, `UPDATE books SET title = 'Racing writer' WHERE id = 1`); err == nil {
			return errors.New("another writer changed the approved book inside the write reservation")
		}
		return stale // Simulate invalid ownership or fingerprint after review.
	})
	if !called || !errors.Is(err, stale) {
		t.Fatalf("locked revalidation was not honored: %v", err)
	}
	if got := identifierRows(t, conn); len(got) != 2 {
		t.Fatalf("rejected validation committed an identifier: %v", got)
	}
	if err := writer.AddMissingIf(ctx, 1, "openlibrary", "OL123W", allowIdentifierTestWrite); err != nil {
		t.Fatalf("rejected validation left a writer lock: %v", err)
	}
}

func TestCalibreIdentifierWriter_ValidatesInputBeforeWriting(t *testing.T) {
	root, conn := identifierLibrary(t)
	writer := NewCalibreIdentifierWriter(root)
	before := identifierRows(t, conn)
	for _, tc := range []struct{ typ, val string }{
		{"isbn", "9780306406157"}, {"asin", "B000TEST"}, {"openlibrary_edition", "OL123M"},
		{"googlebooks", "someVolume"}, {"OpenLibrary", "OL123W"}, {"openlibrary ", "OL123W"},
		{"openlibrary", "OL123M"}, {"openlibrary", "/works/OL123W"}, {"openlibrary", "OL0W'; DELETE FROM books;--"},
		{"google", ""}, {"google", " value"}, {"google", "abc def"}, {"google", "gb:volume"},
		{"hardcover", "hc:1001"}, {"dnb", "dnb:12345"}, {"dnb", "foo/bar"},
	} {
		if err := writer.AddMissingIf(context.Background(), 1, tc.typ, tc.val, allowIdentifierTestWrite); err == nil {
			t.Errorf("accepted type=%q val=%q", tc.typ, tc.val)
		}
	}
	if got := identifierRows(t, conn); !reflect.DeepEqual(got, before) {
		t.Fatalf("invalid input changed identifiers: %v", got)
	}
}

func TestCalibreIdentifierWriter_FailsClosed(t *testing.T) {
	ctx := context.Background()
	missingRoot := t.TempDir()
	if err := NewCalibreIdentifierWriter(missingRoot).AddMissingIf(ctx, 1, "openlibrary", "OL123W", allowIdentifierTestWrite); err == nil {
		t.Fatal("missing metadata.db created or accepted")
	}
	if _, err := os.Stat(filepath.Join(missingRoot, "metadata.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing metadata.db was created: %v", err)
	}
	if err := NewCalibreIdentifierWriter("").AddMissingIf(ctx, 1, "openlibrary", "OL123W", allowIdentifierTestWrite); err == nil {
		t.Fatal("empty path accepted")
	}
	root, conn := identifierLibrary(t)
	if _, err := conn.Exec(`PRAGMA application_id = 0`); err != nil {
		t.Fatal(err)
	}
	if err := NewCalibreIdentifierWriter(root).AddMissingIf(ctx, 1, "openlibrary", "OL123W", allowIdentifierTestWrite); err == nil {
		t.Fatal("non-Calibre metadata.db accepted")
	}
	if _, err := conn.Exec(`PRAGMA application_id = 0x63616c69`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`DROP TABLE identifiers`); err != nil {
		t.Fatal(err)
	}
	if err := NewCalibreIdentifierWriter(root).AddMissingIf(ctx, 1, "openlibrary", "OL123W", allowIdentifierTestWrite); err == nil {
		t.Fatal("wrong schema accepted")
	}
}

func TestCalibreIdentifierWriter_RollsBackTriggerFailure(t *testing.T) {
	root, conn := identifierLibrary(t)
	before := identifierRows(t, conn)
	for _, stmt := range []string{
		`CREATE TABLE audit_side_effect (val TEXT)`,
		`CREATE TRIGGER reject_identifier AFTER INSERT ON identifiers BEGIN
			INSERT INTO audit_side_effect VALUES (NEW.val);
			SELECT RAISE(ABORT, 'simulated Calibre trigger failure'); END`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	err := NewCalibreIdentifierWriter(root).AddMissingIf(context.Background(), 1, "openlibrary", "OL123W", allowIdentifierTestWrite)
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("trigger failure lost its underlying database error: %v", err)
	}
	if got := identifierRows(t, conn); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed transaction retained identifier: %v", got)
	}
	var count int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM audit_side_effect`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed transaction retained side effect: %d, %v", count, err)
	}
	if _, err := conn.Exec(`DROP TRIGGER reject_identifier`); err != nil {
		t.Fatal(err)
	}
	if err := NewCalibreIdentifierWriter(root).AddMissingIf(context.Background(), 1, "openlibrary", "OL123W", allowIdentifierTestWrite); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}

func TestCalibreIdentifierWriter_ConcurrentCAS(t *testing.T) {
	root, conn := identifierLibrary(t)
	writer := NewCalibreIdentifierWriter(root)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, val := range []string{"OL123W", "OL456W"} {
		wg.Add(1)
		go func(value string) {
			defer wg.Done()
			<-start
			results <- writer.AddMissingIf(context.Background(), 1, "openlibrary", value, allowIdentifierTestWrite)
		}(val)
	}
	close(start)
	wg.Wait()
	close(results)
	ok, conflicts := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrCalibreIdentifierConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrency failure: %v", err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("race: %d added, %d conflicts; want 1 each", ok, conflicts)
	}
	var count int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM identifiers WHERE book=1 AND type='openlibrary'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("race left %d work identifiers, %v", count, err)
	}
}
