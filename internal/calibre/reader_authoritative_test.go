package calibre

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestAuthoritativeReader_GetBook(t *testing.T) {
	root := buildFixtureLibrary(t)
	auth, err := OpenAuthoritativeReader(root)
	if err != nil {
		t.Fatalf("OpenAuthoritativeReader: %v", err)
	}
	defer auth.Close()

	ctx := context.Background()

	// Existing book in fixture: ID 1
	b, err := auth.GetBook(ctx, 1)
	if err != nil {
		t.Fatalf("GetBook(1): %v", err)
	}
	if b.CalibreID != 1 {
		t.Errorf("CalibreID = %d, want 1", b.CalibreID)
	}
	if b.Title != "Book One" {
		t.Errorf("Title = %q, want %q", b.Title, "Book One")
	}
	if b.ISBN != "9781234567890" {
		t.Errorf("ISBN = %q, want %q", b.ISBN, "9781234567890")
	}
	if b.Identifiers == nil || b.Identifiers["isbn"] != "9781234567890" {
		t.Errorf("Identifiers[\"isbn\"] = %q, want %q", b.Identifiers["isbn"], "9781234567890")
	}
	if len(b.Authors) != 1 || b.Authors[0].Name != "Alice Author" {
		t.Errorf("Authors = %+v, want Alice Author", b.Authors)
	}
	if b.Series == nil || b.Series.Name != "Example Saga" || b.Series.Position != 1.0 {
		t.Errorf("Series = %+v, want Example Saga #1", b.Series)
	}
	if len(b.Formats) != 1 || b.Formats[0].Format != "EPUB" {
		t.Errorf("Formats = %+v, want EPUB", b.Formats)
	}
	if b.CoverPath == "" {
		t.Error("expected CoverPath to be set")
	}

	// Missing book ID returns ErrBookNotFound
	_, err = auth.GetBook(ctx, 999)
	if !errors.Is(err, ErrBookNotFound) {
		t.Fatalf("GetBook(999) error = %v, want ErrBookNotFound", err)
	}
}

func TestAuthoritativeReader_FindByIdentifier(t *testing.T) {
	root := buildFixtureLibrary(t)

	// Add additional identifiers (ASIN, Goodreads, OpenLibrary) to the fixture database.
	dbPath := filepath.Join(root, metadataDB)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	extraIdentifiers := []string{
		`INSERT INTO identifiers (book, type, val) VALUES (1, 'asin', 'B000TEST11')`,
		`INSERT INTO identifiers (book, type, val) VALUES (2, 'goodreads', '123456')`,
		`INSERT INTO identifiers (book, type, val) VALUES (2, 'isbn', '978-0-123456-78-9')`,
	}
	for _, stmt := range extraIdentifiers {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	_ = db.Close()

	auth, err := OpenAuthoritativeReader(root)
	if err != nil {
		t.Fatalf("OpenAuthoritativeReader: %v", err)
	}
	defer auth.Close()

	ctx := context.Background()

	// 1. Find by exact ISBN
	books, err := auth.FindByIdentifier(ctx, "isbn", "9781234567890")
	if err != nil {
		t.Fatalf("FindByIdentifier isbn: %v", err)
	}
	if len(books) != 1 || books[0].CalibreID != 1 {
		t.Fatalf("FindByIdentifier isbn got %+v, want book 1", books)
	}

	// 2. Find by hyphenated vs unhyphenated ISBN
	books, err = auth.FindByIdentifier(ctx, "isbn", "9780123456789")
	if err != nil {
		t.Fatalf("FindByIdentifier hyphenated isbn: %v", err)
	}
	if len(books) != 1 || books[0].CalibreID != 2 {
		t.Fatalf("FindByIdentifier hyphenated isbn got %+v, want book 2", books)
	}

	// 3. Find by ASIN
	books, err = auth.FindByIdentifier(ctx, "asin", "B000TEST11")
	if err != nil {
		t.Fatalf("FindByIdentifier asin: %v", err)
	}
	if len(books) != 1 || books[0].CalibreID != 1 {
		t.Fatalf("FindByIdentifier asin got %+v, want book 1", books)
	}

	// 4. Find by Goodreads
	books, err = auth.FindByIdentifier(ctx, "goodreads", "123456")
	if err != nil {
		t.Fatalf("FindByIdentifier goodreads: %v", err)
	}
	if len(books) != 1 || books[0].CalibreID != 2 {
		t.Fatalf("FindByIdentifier goodreads got %+v, want book 2", books)
	}

	// 5. Search for non-existent identifier returns empty slice, no error
	books, err = auth.FindByIdentifier(ctx, "asin", "NO_SUCH_ASIN")
	if err != nil {
		t.Fatalf("FindByIdentifier non-existent error: %v", err)
	}
	if len(books) != 0 {
		t.Errorf("FindByIdentifier non-existent got %d books, want 0", len(books))
	}
}

func TestAuthoritativeReader_AllBooks(t *testing.T) {
	root := buildFixtureLibrary(t)
	auth, err := OpenAuthoritativeReader(root)
	if err != nil {
		t.Fatalf("OpenAuthoritativeReader: %v", err)
	}
	defer auth.Close()

	ctx := context.Background()
	all, err := auth.AllBooks(ctx)
	if err != nil {
		t.Fatalf("AllBooks: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("AllBooks returned %d books, want 3", len(all))
	}

	// Assert full shape for all books
	if all[0].CalibreID != 1 || all[0].Title != "Book One" || all[0].ISBN != "9781234567890" {
		t.Errorf("book 0 shape = %+v", all[0])
	}
	if all[1].CalibreID != 2 || all[1].Title != "Book Two" || len(all[1].Authors) != 2 {
		t.Errorf("book 1 shape = %+v", all[1])
	}
	if all[2].CalibreID != 3 || all[2].Title != "No Cover" || all[2].CoverPath != "" {
		t.Errorf("book 2 shape = %+v", all[2])
	}
}

func TestAuthoritativeReader_OpenAuthoritativeLibraryFromConfig(t *testing.T) {
	root := buildFixtureLibrary(t)

	// Disabled in config
	cfg := Config{AuthoritativeLibraryEnabled: false, LibraryPath: root}
	_, err := OpenAuthoritativeLibraryFromConfig(cfg)
	if !errors.Is(err, ErrAuthoritativeDisabled) {
		t.Fatalf("expected ErrAuthoritativeDisabled when disabled, got %v", err)
	}

	// Enabled but path empty
	cfg = Config{AuthoritativeLibraryEnabled: true, LibraryPath: ""}
	_, err = OpenAuthoritativeLibraryFromConfig(cfg)
	if !errors.Is(err, ErrAuthoritativeDisabled) {
		t.Fatalf("expected ErrAuthoritativeDisabled when path empty, got %v", err)
	}

	// Enabled and path set
	cfg = Config{AuthoritativeLibraryEnabled: true, LibraryPath: root}
	auth, err := OpenAuthoritativeLibraryFromConfig(cfg)
	if err != nil {
		t.Fatalf("OpenAuthoritativeLibraryFromConfig: %v", err)
	}
	defer auth.Close()

	n, err := auth.Count(context.Background())
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Errorf("Count = %d, want 3", n)
	}
}

func TestAuthoritativeReader_NoWritesToDatabase(t *testing.T) {
	root := buildFixtureLibrary(t)
	dbPath := filepath.Join(root, metadataDB)

	beforeData, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read db before: %v", err)
	}
	beforeHash := sha256.Sum256(beforeData)

	auth, err := OpenAuthoritativeReader(root)
	if err != nil {
		t.Fatalf("OpenAuthoritativeReader: %v", err)
	}

	ctx := context.Background()
	_, _ = auth.AllBooks(ctx)
	_, _ = auth.GetBook(ctx, 1)
	_, _ = auth.FindByIdentifier(ctx, "isbn", "9781234567890")
	_ = auth.Close()

	afterData, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read db after: %v", err)
	}
	afterHash := sha256.Sum256(afterData)

	if beforeHash != afterHash {
		t.Fatal("metadata.db was mutated during authoritative reading")
	}
}

func TestAuthoritativeReader_SeesWALUpdates(t *testing.T) {
	root := buildFixtureLibrary(t)
	ctx := context.Background()

	w := openWALWriter(t, root)
	if _, err := w.ExecContext(ctx, `INSERT INTO books (id, title, sort, path) VALUES (4, 'Book Four WAL', 'Book Four WAL', 'Alice Author/Book Four (4)')`); err != nil {
		t.Fatalf("insert book 4: %v", err)
	}
	if _, err := w.ExecContext(ctx, `INSERT INTO identifiers (book, type, val) VALUES (4, 'asin', 'B00WALTEST')`); err != nil {
		t.Fatalf("insert identifier 4: %v", err)
	}

	auth, err := OpenAuthoritativeReader(root)
	if err != nil {
		t.Fatalf("OpenAuthoritativeReader: %v", err)
	}
	defer auth.Close()

	// 1. AllBooks sees book 4
	all, err := auth.AllBooks(ctx)
	if err != nil {
		t.Fatalf("AllBooks: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("AllBooks count = %d, want 4", len(all))
	}

	// 2. GetBook sees book 4
	b4, err := auth.GetBook(ctx, 4)
	if err != nil {
		t.Fatalf("GetBook(4): %v", err)
	}
	if b4.Title != "Book Four WAL" {
		t.Errorf("b4 title = %q, want Book Four WAL", b4.Title)
	}

	// 3. FindByIdentifier finds book 4 in WAL
	found, err := auth.FindByIdentifier(ctx, "asin", "B00WALTEST")
	if err != nil {
		t.Fatalf("FindByIdentifier in WAL: %v", err)
	}
	if len(found) != 1 || found[0].CalibreID != 4 {
		t.Fatalf("FindByIdentifier in WAL got %+v, want book 4", found)
	}
}
