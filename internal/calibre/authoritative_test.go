package calibre

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func setupAuthoritativeTest(t *testing.T) (*AuthoritativeService, *db.SettingsRepo, *db.CalibreCrossReferenceRepo, *db.BookRepo, *db.EditionRepo, *models.Author, string) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}

	root := buildFixtureLibrary(t)

	settings := db.NewSettingsRepo(database)
	crossRef := db.NewCalibreCrossReferenceRepo(database)
	books := db.NewBookRepo(database)
	editions := db.NewEditionRepo(database)
	authors := db.NewAuthorRepo(database)

	ctx := context.Background()
	_ = settings.Set(ctx, "calibre.authoritative_library_enabled", "true")
	_ = settings.Set(ctx, "calibre.library_path", root)

	author := &models.Author{Name: "Alice Author"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}

	svc := NewAuthoritativeService(settings, crossRef, books).WithEditions(editions)
	return svc, settings, crossRef, books, editions, author, root
}

func TestAuthoritativeService_IsEnabledAndLibraryPath(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}

	settings := db.NewSettingsRepo(database)
	crossRef := db.NewCalibreCrossReferenceRepo(database)
	books := db.NewBookRepo(database)
	svc := NewAuthoritativeService(settings, crossRef, books)

	ctx := context.Background()

	const (
		settingEnabled = "calibre.authoritative_library_enabled"
		settingPath    = "calibre.library_path"
	)

	// 1. Off by default
	if svc.IsEnabled(ctx) {
		t.Error("IsEnabled should be false by default")
	}
	if svc.LibraryPath(ctx) != "" {
		t.Errorf("LibraryPath = %q, want empty when disabled", svc.LibraryPath(ctx))
	}

	// 2. Enabled without library path
	_ = settings.Set(ctx, settingEnabled, "true")
	if svc.IsEnabled(ctx) {
		t.Error("IsEnabled should be false when library_path is empty")
	}

	// 3. Enabled with library path
	_ = settings.Set(ctx, settingPath, "/tmp/calibre")
	if !svc.IsEnabled(ctx) {
		t.Error("IsEnabled should be true when enabled and library_path set")
	}
	if svc.LibraryPath(ctx) != "/tmp/calibre" {
		t.Errorf("LibraryPath = %q, want /tmp/calibre", svc.LibraryPath(ctx))
	}
}

func TestAuthoritativeService_FormatSpecificOwnership(t *testing.T) {
	svc, settings, crossRef, books, _, author, _ := setupAuthoritativeTest(t)
	ctx := context.Background()

	// 1. Ebook-only wanted book
	bEbook := &models.Book{
		AuthorID:  author.ID,
		Title:     "Ebook Only Book",
		SortTitle: "ebook only book",
		ForeignID: "fid-ebook",
		MediaType: models.MediaTypeEbook,
		Status:    models.BookStatusWanted,
		Monitored: true,
	}
	if err := books.Create(ctx, bEbook); err != nil {
		t.Fatalf("create ebook book: %v", err)
	}

	// 2. Audiobook-only wanted book
	bAudio := &models.Book{
		AuthorID:  author.ID,
		Title:     "Audiobook Only Book",
		SortTitle: "audiobook only book",
		ForeignID: "fid-audio",
		MediaType: models.MediaTypeAudiobook,
		Status:    models.BookStatusWanted,
		Monitored: true,
	}
	if err := books.Create(ctx, bAudio); err != nil {
		t.Fatalf("create audio book: %v", err)
	}

	// 3. Dual-format wanted book (media_type=both)
	bBoth := &models.Book{
		AuthorID:  author.ID,
		Title:     "Dual Format Book",
		SortTitle: "dual format book",
		ForeignID: "fid-both",
		MediaType: models.MediaTypeBoth,
		Status:    models.BookStatusWanted,
		Monitored: true,
	}
	if err := books.Create(ctx, bBoth); err != nil {
		t.Fatalf("create dual book: %v", err)
	}

	// Attach matched cross-reference to all three books
	for _, b := range []*models.Book{bEbook, bAudio, bBoth} {
		ref := &models.CalibreWorkCrossReference{
			BookID:             b.ID,
			CalibreID:          1,
			MatchMethod:        "identifier:isbn",
			Confidence:         models.CalibreMatchConfidenceExact,
			Status:             models.CalibreMatchStatusMatched,
			CalibreFingerprint: "fp1",
		}
		if err := crossRef.UpsertCrossReference(ctx, ref); err != nil {
			t.Fatalf("upsert cross-reference for %d: %v", b.ID, err)
		}
	}

	// --- VERIFICATION: Ebook-only wanted + matching Calibre ebook ---
	if !svc.IsOwnedForFormat(ctx, bEbook, models.MediaTypeEbook) {
		t.Error("bEbook should be owned for ebook format")
	}
	if !svc.IsOwned(ctx, bEbook) {
		t.Error("bEbook should be fully owned")
	}
	if svc.EffectiveStatus(ctx, bEbook) != models.BookStatusImported {
		t.Errorf("bEbook EffectiveStatus = %q, want imported", svc.EffectiveStatus(ctx, bEbook))
	}

	// --- VERIFICATION: Audiobook-only behavior ---
	if svc.IsOwnedForFormat(ctx, bAudio, models.MediaTypeAudiobook) {
		t.Error("bAudio should NOT be owned for audiobook format via Calibre match")
	}
	if svc.IsOwned(ctx, bAudio) {
		t.Error("bAudio should NOT be fully owned via Calibre match alone")
	}
	if svc.EffectiveStatus(ctx, bAudio) != models.BookStatusWanted {
		t.Errorf("bAudio EffectiveStatus = %q, want wanted", svc.EffectiveStatus(ctx, bAudio))
	}

	// --- VERIFICATION: media_type=both where Calibre satisfies ebook but audiobook is still missing ---
	if !svc.IsOwnedForFormat(ctx, bBoth, models.MediaTypeEbook) {
		t.Error("bBoth should be owned for ebook format")
	}
	if svc.IsOwnedForFormat(ctx, bBoth, models.MediaTypeAudiobook) {
		t.Error("bBoth should NOT be owned for audiobook format")
	}
	if svc.IsOwned(ctx, bBoth) {
		t.Error("bBoth should NOT be fully owned while audiobook is missing")
	}
	if svc.EffectiveStatus(ctx, bBoth) != models.BookStatusWanted {
		t.Errorf("bBoth EffectiveStatus = %q, want wanted", svc.EffectiveStatus(ctx, bBoth))
	}

	// FilterWantedBooks should suppress bEbook, but KEEP bAudio and bBoth in wanted list!
	filtered := svc.FilterWantedBooks(ctx, []models.Book{*bEbook, *bAudio, *bBoth})
	if len(filtered) != 2 {
		t.Fatalf("FilterWantedBooks returned %d books, want 2 (bAudio and bBoth)", len(filtered))
	}
	for _, b := range filtered {
		if b.ID == bEbook.ID {
			t.Errorf("bEbook should have been suppressed from wanted list")
		}
	}

	// Now set AudiobookFilePath on bBoth (audiobook acquired)
	bBoth.AudiobookFilePath = "/path/to/audiobook.m4b"
	if !svc.IsOwnedForFormat(ctx, bBoth, models.MediaTypeAudiobook) {
		t.Error("bBoth should be owned for audiobook format with file on disk")
	}
	if !svc.IsOwned(ctx, bBoth) {
		t.Error("bBoth should now be fully owned with ebook in Calibre and audiobook on disk")
	}
	filtered2 := svc.FilterWantedBooks(ctx, []models.Book{*bEbook, *bAudio, *bBoth})
	if len(filtered2) != 1 || filtered2[0].ID != bAudio.ID {
		t.Fatalf("FilterWantedBooks returned %+v, want only bAudio", filtered2)
	}

	// --- VERIFICATION: feature disabled restores existing per-format behavior ---
	_ = settings.Set(ctx, "calibre.authoritative_library_enabled", "false")

	if svc.IsOwned(ctx, bEbook) {
		t.Error("bEbook IsOwned should be false when authoritative mode is disabled")
	}
	if svc.IsOwnedForFormat(ctx, bEbook, models.MediaTypeEbook) {
		t.Error("bEbook IsOwnedForFormat ebook should be false when authoritative mode is disabled")
	}
	restoredWanted := svc.FilterWantedBooks(ctx, []models.Book{*bEbook, *bAudio, *bBoth})
	if len(restoredWanted) != 3 {
		t.Fatalf("FilterWantedBooks returned %d books when disabled, want 3", len(restoredWanted))
	}
}

func TestAuthoritativeService_ReconciliationInputData_IdentifiersAndEditions(t *testing.T) {
	svc, _, crossRef, books, editions, author, _ := setupAuthoritativeTest(t)
	ctx := context.Background()

	// Create a book with completely different title/author so title/author fallback cannot match it.
	b := &models.Book{
		AuthorID:  author.ID,
		Title:     "Completely Unmatched Title XYZ",
		SortTitle: "completely unmatched title xyz",
		ForeignID: "fid-unmatched",
		Status:    models.BookStatusWanted,
		Monitored: true,
	}
	if err := books.Create(ctx, b); err != nil {
		t.Fatalf("create book: %v", err)
	}

	// Persist an Edition row for this book containing the fixture's ISBN (9781234567890 for Calibre ID 1)
	ed := &models.Edition{
		BookID: b.ID,
		ISBN13: stringPtr("9781234567890"),
	}
	if err := editions.Upsert(ctx, ed); err != nil {
		t.Fatalf("upsert edition: %v", err)
	}

	// Persist a BookIdentifier for this book
	if err := books.UpsertBookIdentifier(ctx, b.ID, "9781234567890"); err != nil {
		t.Fatalf("upsert book identifier: %v", err)
	}

	// Run Reconcile. ListIncludingExcluded won't return Editions or Identifiers on the Book struct directly,
	// so Reconcile must query/attach them before calling MatchWork.
	res, err := svc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if res.Matched != 1 {
		t.Fatalf("Reconcile Matched = %d, want 1 (must match via persisted ISBN/identifiers despite title mismatch)", res.Matched)
	}

	ref, err := crossRef.GetByBookID(ctx, b.ID)
	if err != nil || ref == nil {
		t.Fatalf("GetByBookID failed: %v", err)
	}
	if ref.CalibreID != 1 || ref.Status != models.CalibreMatchStatusMatched {
		t.Errorf("cross reference = %+v, want CalibreID 1 matched", ref)
	}
	if ref.MatchMethod != "identifier:isbn" {
		t.Errorf("MatchMethod = %q, want identifier:isbn (proves ISBN match occurred, not title/author fallback)", ref.MatchMethod)
	}
}

func TestAuthoritativeService_LegacyFilePathSemantics(t *testing.T) {
	svc, _, crossRef, books, _, author, _ := setupAuthoritativeTest(t)
	ctx := context.Background()

	// 1. media_type=ebook + legacy FilePath
	bEbook := &models.Book{
		AuthorID: author.ID, Title: "Legacy Ebook", ForeignID: "fid-leg-1",
		MediaType: models.MediaTypeEbook, FilePath: "/path/to/ebook.epub",
		Status: models.BookStatusWanted, Monitored: true,
	}
	if err := books.Create(ctx, bEbook); err != nil {
		t.Fatalf("create ebook: %v", err)
	}
	if !svc.IsOwned(ctx, bEbook) {
		t.Error("media_type=ebook + FilePath should be owned")
	}

	// 2. media_type=audiobook + legacy FilePath
	bAudio := &models.Book{
		AuthorID: author.ID, Title: "Legacy Audiobook", ForeignID: "fid-leg-2",
		MediaType: models.MediaTypeAudiobook, FilePath: "/path/to/audio.m4b",
		Status: models.BookStatusWanted, Monitored: true,
	}
	if err := books.Create(ctx, bAudio); err != nil {
		t.Fatalf("create audio: %v", err)
	}
	if !svc.IsOwned(ctx, bAudio) {
		t.Error("media_type=audiobook + FilePath should be owned")
	}

	// 3. media_type=both + legacy FilePath only
	bBothPath := &models.Book{
		AuthorID: author.ID, Title: "Legacy Both Path Only", ForeignID: "fid-leg-3",
		MediaType: models.MediaTypeBoth, FilePath: "/path/to/single.epub",
		Status: models.BookStatusWanted, Monitored: true,
	}
	if err := books.Create(ctx, bBothPath); err != nil {
		t.Fatalf("create both path: %v", err)
	}
	if svc.IsOwned(ctx, bBothPath) {
		t.Error("media_type=both + FilePath only must NOT be owned for both formats")
	}

	// 4. media_type=both + Calibre ebook match + legacy FilePath
	bBothMatchPath := &models.Book{
		AuthorID: author.ID, Title: "Both Match Path", ForeignID: "fid-leg-4",
		MediaType: models.MediaTypeBoth, FilePath: "/path/to/single.epub",
		Status: models.BookStatusWanted, Monitored: true,
	}
	if err := books.Create(ctx, bBothMatchPath); err != nil {
		t.Fatalf("create both match path: %v", err)
	}
	_ = crossRef.UpsertCrossReference(ctx, &models.CalibreWorkCrossReference{
		BookID: bBothMatchPath.ID, CalibreID: 1, Status: models.CalibreMatchStatusMatched,
	})
	if svc.IsOwned(ctx, bBothMatchPath) {
		t.Error("media_type=both + Calibre match + legacy FilePath must NOT independently satisfy audiobook")
	}

	// 5. media_type=both + explicit AudiobookFilePath + Calibre ebook match
	bBothMatchAudio := &models.Book{
		AuthorID: author.ID, Title: "Both Match AudioPath", ForeignID: "fid-leg-5",
		MediaType: models.MediaTypeBoth, AudiobookFilePath: "/path/to/audio.m4b",
		Status: models.BookStatusWanted, Monitored: true,
	}
	if err := books.Create(ctx, bBothMatchAudio); err != nil {
		t.Fatalf("create both match audio: %v", err)
	}
	_ = crossRef.UpsertCrossReference(ctx, &models.CalibreWorkCrossReference{
		BookID: bBothMatchAudio.ID, CalibreID: 1, Status: models.CalibreMatchStatusMatched,
	})
	if !svc.IsOwned(ctx, bBothMatchAudio) {
		t.Error("media_type=both + Calibre match + explicit AudiobookFilePath MUST be owned")
	}
}

func stringPtr(s string) *string { return &s }

func TestAuthoritativeService_Reconcile_FailsClosedOnHydrationErrors(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	root := buildFixtureLibrary(t)

	settings := db.NewSettingsRepo(database)
	crossRef := db.NewCalibreCrossReferenceRepo(database)
	books := db.NewBookRepo(database)
	editions := db.NewEditionRepo(database)
	authors := db.NewAuthorRepo(database)

	ctx := context.Background()
	_ = settings.Set(ctx, "calibre.authoritative_library_enabled", "true")
	_ = settings.Set(ctx, "calibre.library_path", root)

	author := &models.Author{Name: "Alice Author"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}

	b := &models.Book{
		AuthorID:  author.ID,
		Title:     "Work Title",
		SortTitle: "work title",
		ForeignID: "fid-work-hydration-err",
		Status:    models.BookStatusWanted,
		Monitored: true,
	}
	if err := books.Create(ctx, b); err != nil {
		t.Fatalf("create book: %v", err)
	}

	// 1. Test ListAllBookIdentifiers failure: drop table book_identifiers
	if _, err := database.Exec("DROP TABLE book_identifiers"); err != nil {
		t.Fatalf("drop table book_identifiers: %v", err)
	}

	svc := NewAuthoritativeService(settings, crossRef, books).WithEditions(editions)
	res, err := svc.Reconcile(ctx)
	if err == nil {
		t.Fatal("Reconcile expected error when ListAllBookIdentifiers fails, got nil")
	}
	if res != nil {
		t.Errorf("Reconcile returned result %+v on hydration error, want nil", res)
	}

	// 2. Test ListAllEditions failure: recreate book_identifiers, drop table editions
	if _, err := database.Exec("CREATE TABLE book_identifiers (id INTEGER PRIMARY KEY, book_id INTEGER, identifier TEXT)"); err != nil {
		t.Fatalf("recreate book_identifiers: %v", err)
	}
	if _, err := database.Exec("DROP TABLE editions"); err != nil {
		t.Fatalf("drop table editions: %v", err)
	}

	res2, err2 := svc.Reconcile(ctx)
	if err2 == nil {
		t.Fatal("Reconcile expected error when ListAllEditions fails, got nil")
	}
	if res2 != nil {
		t.Errorf("Reconcile returned result %+v on hydration error, want nil", res2)
	}
}

type trackingAuthoritativeLibrary struct {
	calibreBooks          []CalibreBook
	countCalls            atomic.Int32
	getBookCalls          atomic.Int32
	booksCalls            atomic.Int32
	allBooksCalls         atomic.Int32
	findByIdentifierCalls atomic.Int32
	closeCalls            atomic.Int32
}

func (t *trackingAuthoritativeLibrary) Count(ctx context.Context) (int, error) {
	t.countCalls.Add(1)
	return len(t.calibreBooks), nil
}

func (t *trackingAuthoritativeLibrary) GetBook(ctx context.Context, id int64) (*CalibreBook, error) {
	t.getBookCalls.Add(1)
	for _, b := range t.calibreBooks {
		if b.CalibreID == id {
			return &b, nil
		}
	}
	return nil, ErrBookNotFound
}

func (t *trackingAuthoritativeLibrary) Books(ctx context.Context, fn func(CalibreBook) error) error {
	t.booksCalls.Add(1)
	for _, b := range t.calibreBooks {
		if err := fn(b); err != nil {
			return err
		}
	}
	return nil
}

func (t *trackingAuthoritativeLibrary) AllBooks(ctx context.Context) ([]CalibreBook, error) {
	t.allBooksCalls.Add(1)
	return t.calibreBooks, nil
}

func (t *trackingAuthoritativeLibrary) FindByIdentifier(ctx context.Context, idType, idVal string) ([]CalibreBook, error) {
	t.findByIdentifierCalls.Add(1)
	var found []CalibreBook
	for _, b := range t.calibreBooks {
		if b.Identifiers[idType] == idVal {
			found = append(found, b)
		}
	}
	return found, nil
}

func (t *trackingAuthoritativeLibrary) Close() error {
	t.closeCalls.Add(1)
	return nil
}

func TestAuthoritativeService_Reconcile_BulkSnapshotNoPerReferenceReads(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	settings := db.NewSettingsRepo(database)
	crossRef := db.NewCalibreCrossReferenceRepo(database)
	books := db.NewBookRepo(database)
	editions := db.NewEditionRepo(database)
	authors := db.NewAuthorRepo(database)

	ctx := context.Background()
	_ = settings.Set(ctx, "calibre.authoritative_library_enabled", "true")
	_ = settings.Set(ctx, "calibre.library_path", "/fake/calibre/path")

	author := &models.Author{Name: "Alice Author"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}

	var calibreBooks []CalibreBook

	// 1. Many unchanged existing matches (10 books)
	const unchangedCount = 10
	for i := 1; i <= unchangedCount; i++ {
		cID := int64(i)
		isbn := fmt.Sprintf("978100000000%d", i)
		cb := CalibreBook{
			CalibreID: cID,
			Title:     fmt.Sprintf("Unchanged Book %d", i),
			ISBN:      isbn,
			Authors:   []CalibreAuthor{{CalibreID: cID, Name: "Alice Author"}},
			Identifiers: map[string]string{
				"isbn": isbn,
			},
		}
		calibreBooks = append(calibreBooks, cb)

		b := &models.Book{
			AuthorID:  author.ID,
			Title:     cb.Title,
			SortTitle: NormalizeTitle(cb.Title),
			ForeignID: fmt.Sprintf("fid-unchanged-%d", i),
			Status:    models.BookStatusWanted,
			Monitored: true,
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatalf("create unchanged book %d: %v", i, err)
		}
		ed := &models.Edition{
			BookID:    b.ID,
			ForeignID: fmt.Sprintf("ed-unchanged-%d", i),
			ISBN13:    &isbn,
		}
		if err := editions.Upsert(ctx, ed); err != nil {
			t.Fatalf("create edition for unchanged book %d: %v", i, err)
		}

		fp := CalculateFingerprint(&cb)
		ref := &models.CalibreWorkCrossReference{
			BookID:             b.ID,
			CalibreID:          cID,
			MatchMethod:        "identifier:isbn",
			Confidence:         models.CalibreMatchConfidenceExact,
			Status:             models.CalibreMatchStatusMatched,
			CalibreFingerprint: fp,
		}
		if err := crossRef.UpsertCrossReference(ctx, ref); err != nil {
			t.Fatalf("upsert cross-reference for unchanged book %d: %v", i, err)
		}
	}

	// 2. Changed fingerprint that still matches the same Calibre book
	cb11 := CalibreBook{
		CalibreID: 11,
		Title:     "Book Eleven (Edited)",
		ISBN:      "9781111111111",
		Authors:   []CalibreAuthor{{CalibreID: 11, Name: "Alice Author"}},
		Identifiers: map[string]string{
			"isbn": "9781111111111",
		},
	}
	calibreBooks = append(calibreBooks, cb11)

	b11 := &models.Book{
		AuthorID:  author.ID,
		Title:     "Book Eleven",
		SortTitle: "book eleven",
		ForeignID: "fid-changed-same-11",
		Status:    models.BookStatusWanted,
		Monitored: true,
	}
	if err := books.Create(ctx, b11); err != nil {
		t.Fatalf("create book 11: %v", err)
	}
	isbn11 := "9781111111111"
	if err := editions.Upsert(ctx, &models.Edition{BookID: b11.ID, ForeignID: "ed-11", ISBN13: &isbn11}); err != nil {
		t.Fatalf("create edition 11: %v", err)
	}
	oldFP11 := "sha256:stale_fingerprint_eleven_0000"
	ref11 := &models.CalibreWorkCrossReference{
		BookID:             b11.ID,
		CalibreID:          11,
		MatchMethod:        "identifier:isbn",
		Confidence:         models.CalibreMatchConfidenceExact,
		Status:             models.CalibreMatchStatusMatched,
		CalibreFingerprint: oldFP11,
	}
	if err := crossRef.UpsertCrossReference(ctx, ref11); err != nil {
		t.Fatalf("upsert cross-reference 11: %v", err)
	}

	// 3. Changed fingerprint that resolves to another Calibre book
	cb12 := CalibreBook{
		CalibreID: 12,
		Title:     "Old Book 12 Changed",
		ISBN:      "9781212121212",
		Authors:   []CalibreAuthor{{CalibreID: 12, Name: "Alice Author"}},
		Identifiers: map[string]string{
			"isbn": "9781212121212",
		},
	}
	cb13 := CalibreBook{
		CalibreID: 13,
		Title:     "Book Moved To 13",
		ISBN:      "9781313131313",
		Authors:   []CalibreAuthor{{CalibreID: 13, Name: "Alice Author"}},
		Identifiers: map[string]string{
			"isbn": "9781313131313",
		},
	}
	calibreBooks = append(calibreBooks, cb12, cb13)

	b12 := &models.Book{
		AuthorID:  author.ID,
		Title:     "Book Moved To 13",
		SortTitle: "book moved to 13",
		ForeignID: "fid-changed-moved-12",
		Status:    models.BookStatusWanted,
		Monitored: true,
	}
	if err := books.Create(ctx, b12); err != nil {
		t.Fatalf("create book 12: %v", err)
	}
	isbn13 := "9781313131313"
	if err := editions.Upsert(ctx, &models.Edition{BookID: b12.ID, ForeignID: "ed-12", ISBN13: &isbn13}); err != nil {
		t.Fatalf("create edition 12: %v", err)
	}
	ref12 := &models.CalibreWorkCrossReference{
		BookID:             b12.ID,
		CalibreID:          12, // originally pointed to 12
		MatchMethod:        "identifier:isbn",
		Confidence:         models.CalibreMatchConfidenceExact,
		Status:             models.CalibreMatchStatusMatched,
		CalibreFingerprint: "sha256:stale_fp_pointing_to_12",
	}
	if err := crossRef.UpsertCrossReference(ctx, ref12); err != nil {
		t.Fatalf("upsert cross-reference 12: %v", err)
	}

	// 4. Ambiguous persisted reference revalidation
	cb14a := CalibreBook{
		CalibreID: 14,
		Title:     "Ambiguous Work",
		ISBN:      "9781414141414",
		Authors:   []CalibreAuthor{{CalibreID: 14, Name: "Alice Author"}},
		Identifiers: map[string]string{
			"isbn": "9781414141414",
		},
	}
	cb14b := CalibreBook{
		CalibreID: 15,
		Title:     "Ambiguous Work Duplicate",
		ISBN:      "9781414141414",
		Authors:   []CalibreAuthor{{CalibreID: 15, Name: "Alice Author"}},
		Identifiers: map[string]string{
			"isbn": "9781414141414",
		},
	}
	calibreBooks = append(calibreBooks, cb14a, cb14b)

	b14 := &models.Book{
		AuthorID:  author.ID,
		Title:     "Ambiguous Work",
		SortTitle: "ambiguous work",
		ForeignID: "fid-ambiguous-14",
		Status:    models.BookStatusWanted,
		Monitored: true,
	}
	if err := books.Create(ctx, b14); err != nil {
		t.Fatalf("create book 14: %v", err)
	}
	isbn14 := "9781414141414"
	if err := editions.Upsert(ctx, &models.Edition{BookID: b14.ID, ForeignID: "ed-14", ISBN13: &isbn14}); err != nil {
		t.Fatalf("create edition 14: %v", err)
	}
	ref14 := &models.CalibreWorkCrossReference{
		BookID:           b14.ID,
		CalibreID:        0,
		MatchMethod:      "identifier:ambiguous",
		Confidence:       models.CalibreMatchConfidenceAmbiguous,
		Status:           models.CalibreMatchStatusAmbiguous,
		MatchDetailsJSON: `{"candidates":[14,15]}`,
	}
	if err := crossRef.UpsertCrossReference(ctx, ref14); err != nil {
		t.Fatalf("upsert cross-reference 14: %v", err)
	}

	// 5. Missing Calibre ID becoming stale
	b15 := &models.Book{
		AuthorID:  author.ID,
		Title:     "Missing Book",
		SortTitle: "missing book",
		ForeignID: "fid-missing-15",
		Status:    models.BookStatusWanted,
		Monitored: true,
	}
	if err := books.Create(ctx, b15); err != nil {
		t.Fatalf("create book 15: %v", err)
	}
	isbn15 := "9781515151515"
	if err := editions.Upsert(ctx, &models.Edition{BookID: b15.ID, ForeignID: "ed-15", ISBN13: &isbn15}); err != nil {
		t.Fatalf("create edition 15: %v", err)
	}
	ref15 := &models.CalibreWorkCrossReference{
		BookID:             b15.ID,
		CalibreID:          9999, // 9999 does NOT exist in Calibre library
		MatchMethod:        "identifier:isbn",
		Confidence:         models.CalibreMatchConfidenceExact,
		Status:             models.CalibreMatchStatusMatched,
		CalibreFingerprint: "sha256:fp_missing_9999",
	}
	if err := crossRef.UpsertCrossReference(ctx, ref15); err != nil {
		t.Fatalf("upsert cross-reference 15: %v", err)
	}

	tracker := &trackingAuthoritativeLibrary{calibreBooks: calibreBooks}

	svc := NewAuthoritativeService(settings, crossRef, books).
		WithEditions(editions).
		WithReaderFactory(func(path string) (AuthoritativeLibrary, error) {
			return tracker, nil
		})

	res, err := svc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// --- DETERMINISTIC EVIDENCE: Proving no per-reference Calibre reads ---
	if calls := tracker.allBooksCalls.Load(); calls != 1 {
		t.Errorf("AllBooks calls = %d, want 1 (single bulk snapshot load)", calls)
	}
	if calls := tracker.getBookCalls.Load(); calls != 0 {
		t.Errorf("GetBook calls = %d, want 0 (NO per-reference reads allowed)", calls)
	}
	if calls := tracker.findByIdentifierCalls.Load(); calls != 0 {
		t.Errorf("FindByIdentifier calls = %d, want 0 (NO per-reference reads allowed)", calls)
	}
	if calls := tracker.booksCalls.Load(); calls != 0 {
		t.Errorf("Books calls = %d, want 0", calls)
	}
	if calls := tracker.countCalls.Load(); calls != 0 {
		t.Errorf("Count calls = %d, want 0", calls)
	}

	// --- VERIFY REVALIDATION RESULTS FOR ALL 5 COVERAGE CASES ---
	// Total books: 10 unchanged + 1 changed FP same + 1 changed FP moved + 1 ambiguous + 1 missing ID = 14 books
	if res.TotalBinderyBooks != 14 {
		t.Errorf("TotalBinderyBooks = %d, want 14", res.TotalBinderyBooks)
	}
	// Revalidated = 10 (unchanged) + 1 (changed FP same) + 1 (changed FP moved) + 1 (ambiguous) = 13
	if res.Revalidated != 13 {
		t.Errorf("Revalidated = %d, want 13", res.Revalidated)
	}
	// Stale = 1 (missing ID)
	if res.Stale != 1 {
		t.Errorf("Stale = %d, want 1", res.Stale)
	}

	// 1. Unchanged existing matches remain matched
	for i := 1; i <= unchangedCount; i++ {
		r, err := crossRef.GetByBookID(ctx, int64(i))
		if err != nil || r == nil {
			t.Fatalf("GetByBookID(%d): %v", i, err)
		}
		if r.Status != models.CalibreMatchStatusMatched || r.CalibreID != int64(i) {
			t.Errorf("unchanged book %d crossRef status=%s ID=%d, want matched/%d", i, r.Status, r.CalibreID, i)
		}
	}

	// 2. Changed fingerprint that still matches same Calibre book
	r11, err := crossRef.GetByBookID(ctx, b11.ID)
	if err != nil || r11 == nil {
		t.Fatalf("GetByBookID(b11): %v", err)
	}
	expectedFP11 := CalculateFingerprint(&cb11)
	if r11.Status != models.CalibreMatchStatusMatched || r11.CalibreID != 11 {
		t.Errorf("changed FP same book status=%s ID=%d, want matched/11", r11.Status, r11.CalibreID)
	}
	if r11.CalibreFingerprint != expectedFP11 {
		t.Errorf("changed FP same book FP=%q, want updated %q", r11.CalibreFingerprint, expectedFP11)
	}

	// 3. Changed fingerprint that resolves to another Calibre book (moved to Calibre ID 13)
	r12, err := crossRef.GetByBookID(ctx, b12.ID)
	if err != nil || r12 == nil {
		t.Fatalf("GetByBookID(b12): %v", err)
	}
	expectedFP13 := CalculateFingerprint(&cb13)
	if r12.Status != models.CalibreMatchStatusMatched || r12.CalibreID != 13 {
		t.Errorf("changed FP moved book status=%s ID=%d, want matched/13", r12.Status, r12.CalibreID)
	}
	if r12.CalibreFingerprint != expectedFP13 {
		t.Errorf("changed FP moved book FP=%q, want %q", r12.CalibreFingerprint, expectedFP13)
	}

	// 4. Ambiguous persisted reference revalidation
	r14, err := crossRef.GetByBookID(ctx, b14.ID)
	if err != nil || r14 == nil {
		t.Fatalf("GetByBookID(b14): %v", err)
	}
	if r14.Status != models.CalibreMatchStatusAmbiguous || r14.CalibreID != 0 {
		t.Errorf("ambiguous ref status=%s ID=%d, want ambiguous/0", r14.Status, r14.CalibreID)
	}

	// 5. Missing Calibre ID becoming stale
	r15, err := crossRef.GetByBookID(ctx, b15.ID)
	if err != nil || r15 == nil {
		t.Fatalf("GetByBookID(b15): %v", err)
	}
	if r15.Status != models.CalibreMatchStatusStale {
		t.Errorf("missing Calibre ID status=%s, want stale", r15.Status)
	}
}
