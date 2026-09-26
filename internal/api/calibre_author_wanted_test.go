package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// Capture every dispatch, including large author searches (the general mock
// intentionally drops calls after its eight-slot buffer is full).
type authorSearchRecorder struct{ calls chan models.Book }

func (s *authorSearchRecorder) SearchAndGrabBook(_ context.Context, book models.Book) {
	s.calls <- book
}

func TestAuthorDetailAndSearch_AuthoritativeOwnership(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	settings := db.NewSettingsRepo(database)
	for key, value := range map[string]string{
		"calibre.authoritative_library_enabled": "true",
		"calibre.library_path":                  "/configured/calibre",
	} {
		if err := settings.Set(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	refs := db.NewCalibreCrossReferenceRepo(database)
	author := &models.Author{ForeignID: "OL_AUTHOR", Name: "Mixed Author", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	create := func(title, mediaType string, matched, monitored bool, audioPath, legacyPath string) *models.Book {
		t.Helper()
		book := &models.Book{
			ForeignID: title, Title: title, SortTitle: title, AuthorID: author.ID,
			MediaType: mediaType, Status: models.BookStatusWanted, Monitored: monitored,
			Genres: []string{}, MetadataProvider: "openlibrary",
		}
		if err := books.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		if audioPath != "" || legacyPath != "" {
			book.AudiobookFilePath = audioPath
			book.FilePath = legacyPath
			if err := books.Update(ctx, book); err != nil {
				t.Fatal(err)
			}
		}
		if matched {
			if err := refs.UpsertCrossReference(ctx, &models.CalibreWorkCrossReference{
				BookID: book.ID, CalibreID: book.ID, MatchMethod: "identifier:isbn",
				Confidence: models.CalibreMatchConfidenceExact, Status: models.CalibreMatchStatusMatched,
				CalibreFingerprint: "test",
			}); err != nil {
				t.Fatal(err)
			}
		}
		return book
	}
	ownedEbook := create("Owned ebook", models.MediaTypeEbook, true, true, "", "")
	unmatched := create("Unmatched ebook", models.MediaTypeEbook, false, true, "", "")
	bothMissingAudio := create("Both needs audio", models.MediaTypeBoth, true, true, "", "")
	bothSatisfied := create("Both satisfied", models.MediaTypeBoth, true, true, "/audio/book.m4b", "")
	audioOnly := create("Audio needs audio", models.MediaTypeAudiobook, true, true, "", "")
	legacyBoth := create("Legacy both needs formats", models.MediaTypeBoth, true, true, "", "/legacy/book.epub")
	unmatchedLegacyBoth := create("Unmatched legacy both", models.MediaTypeBoth, false, true, "", "/legacy/other.epub")
	create("Unmonitored", models.MediaTypeEbook, false, false, "", "")

	svc := calibre.NewAuthoritativeService(settings, refs, books)
	bookHandler := NewBookHandler(books, nil, nil, nil).WithAuthoritativeService(svc)
	list := func() []models.Book {
		t.Helper()
		rec := httptest.NewRecorder()
		bookHandler.List(rec, httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/api/v1/book?authorId=%d&limit=500", author.ID), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("author list: %d %s", rec.Code, rec.Body.String())
		}
		var page bookListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page.Items
	}
	items := list()
	if len(items) != 8 {
		t.Fatalf("author list has %d books, want 8", len(items))
	}
	wantFormats := map[int64]struct{ ebook, audio string }{
		bothMissingAudio.ID:    {models.BookStatusImported, models.BookStatusWanted},
		bothSatisfied.ID:       {models.BookStatusImported, models.BookStatusImported},
		legacyBoth.ID:          {models.BookStatusImported, models.BookStatusWanted},
		unmatchedLegacyBoth.ID: {models.BookStatusWanted, models.BookStatusWanted},
	}
	wantStatus := map[int64]string{
		ownedEbook.ID:          models.BookStatusImported,
		unmatched.ID:           models.BookStatusWanted,
		bothMissingAudio.ID:    models.BookStatusWanted,
		bothSatisfied.ID:       models.BookStatusImported,
		audioOnly.ID:           models.BookStatusWanted,
		legacyBoth.ID:          models.BookStatusWanted,
		unmatchedLegacyBoth.ID: models.BookStatusWanted,
	}
	imported, wanted := 0, 0
	for _, b := range items {
		if b.Status != models.BookStatusWanted {
			t.Errorf("%s persisted status projected as %q", b.Title, b.Status)
		}
		effective := b.EffectiveStatus
		if effective == "" {
			effective = b.Status
		}
		if expected, ok := wantStatus[b.ID]; ok && effective != expected {
			t.Errorf("%s effective status = %q, want %q", b.Title, effective, expected)
		}
		if formats, ok := wantFormats[b.ID]; ok {
			if b.EffectiveEbookStatus != formats.ebook || b.EffectiveAudiobookStatus != formats.audio {
				t.Errorf("%s format statuses = ebook %q, audio %q; want %q, %q",
					b.Title, b.EffectiveEbookStatus, b.EffectiveAudiobookStatus, formats.ebook, formats.audio)
			}
		} else if b.EffectiveEbookStatus != "" || b.EffectiveAudiobookStatus != "" {
			t.Errorf("%s projected per-format statuses for a single-format book", b.Title)
		}
		if effective == models.BookStatusImported {
			imported++
		} else if b.Monitored {
			wanted++
		}
	}
	if imported != 2 || wanted != 5 {
		t.Errorf("author counts: in library=%d wanted=%d, want 2 and 5", imported, wanted)
	}
	stored, err := books.GetByID(ctx, ownedEbook.ID)
	if err != nil || stored == nil || stored.Status != models.BookStatusWanted ||
		stored.EffectiveStatus != "" || stored.EffectiveEbookStatus != "" || stored.EffectiveAudiobookStatus != "" {
		t.Fatalf("author response changed stored ownership state: %+v, %v", stored, err)
	}
	storedBoth, err := books.GetByID(ctx, bothMissingAudio.ID)
	if err != nil || storedBoth == nil || storedBoth.Status != models.BookStatusWanted ||
		storedBoth.EffectiveEbookStatus != "" || storedBoth.EffectiveAudiobookStatus != "" {
		t.Fatalf("author response changed stored dual-format state: %+v, %v", storedBoth, err)
	}

	searcher := &authorSearchRecorder{calls: make(chan models.Book, 10)}
	bulk := NewBulkHandler(authors, books, nil, searcher).WithAuthoritativeService(svc)
	oldPace := searchPaceInterval
	searchPaceInterval = 0
	t.Cleanup(func() { searchPaceInterval = oldPace })
	search := func(expected map[int64]bool) {
		t.Helper()
		rec := postBulk(t, bulk.AuthorsBulk, fmt.Sprintf(`{"ids":[%d],"action":"search"}`, author.ID))
		if rec.Code != http.StatusOK {
			t.Fatalf("author search: %d %s", rec.Code, rec.Body.String())
		}
		var response bulkResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || !response.Results[fmt.Sprint(author.ID)].OK {
			t.Fatalf("author search response: %+v %v", response, err)
		}
		remaining := len(expected)
		for i := 0; i < remaining; i++ {
			select {
			case b := <-searcher.calls:
				if !expected[b.ID] {
					t.Errorf("unexpected or duplicate search for %s", b.Title)
				}
				delete(expected, b.ID)
			case <-time.After(3 * time.Second):
				t.Fatalf("search did not dispatch all unsatisfied books: remaining %v", expected)
			}
		}
		if len(expected) != 0 {
			t.Errorf("unsatisfied books not searched: %v", expected)
		}
		select {
		case b := <-searcher.calls:
			t.Errorf("unexpected extra search for %s", b.Title)
		case <-time.After(50 * time.Millisecond):
		}
	}
	search(map[int64]bool{
		unmatched.ID: true, bothMissingAudio.ID: true, audioOnly.ID: true,
		legacyBoth.ID: true, unmatchedLegacyBoth.ID: true,
	})

	if err := settings.Set(ctx, "calibre.authoritative_library_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	for _, b := range list() {
		if b.EffectiveStatus != "" || b.EffectiveEbookStatus != "" || b.EffectiveAudiobookStatus != "" {
			t.Errorf("disabled mode still projects %s: aggregate %q, ebook %q, audio %q",
				b.Title, b.EffectiveStatus, b.EffectiveEbookStatus, b.EffectiveAudiobookStatus)
		}
	}
	search(map[int64]bool{
		ownedEbook.ID: true, unmatched.ID: true, bothMissingAudio.ID: true,
		bothSatisfied.ID: true, audioOnly.ID: true, legacyBoth.ID: true,
		unmatchedLegacyBoth.ID: true,
	})
}

func TestBookDetail_AuthoritativeEffectiveStatus(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	settings := db.NewSettingsRepo(database)
	for key, value := range map[string]string{
		"calibre.authoritative_library_enabled": "true",
		"calibre.library_path":                  "/configured/calibre",
	} {
		if err := settings.Set(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	books := db.NewBookRepo(database)
	refs := db.NewCalibreCrossReferenceRepo(database)
	authors := db.NewAuthorRepo(database)
	author := &models.Author{ForeignID: "OL_DETAIL_AUTHOR", Name: "Detail Author", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	h := NewBookHandler(books, nil, nil, nil).WithAuthoritativeService(calibre.NewAuthoritativeService(settings, refs, books))

	create := func(title, mediaType, status string, matched bool, ebookPath, audioPath, legacyPath string) *models.Book {
		t.Helper()
		b := &models.Book{
			ForeignID: title, Title: title, SortTitle: title, AuthorID: author.ID,
			MediaType: mediaType, Status: status, Monitored: true, Genres: []string{},
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if ebookPath != "" {
			if err := books.AddBookFile(ctx, b.ID, models.MediaTypeEbook, ebookPath); err != nil {
				t.Fatal(err)
			}
		}
		if audioPath != "" {
			if err := books.AddBookFile(ctx, b.ID, models.MediaTypeAudiobook, audioPath); err != nil {
				t.Fatal(err)
			}
		}
		if legacyPath != "" {
			b.FilePath = legacyPath
			if err := books.Update(ctx, b); err != nil {
				t.Fatal(err)
			}
		}
		if matched {
			if err := refs.UpsertCrossReference(ctx, &models.CalibreWorkCrossReference{
				BookID: b.ID, CalibreID: b.ID, MatchMethod: "identifier:isbn",
				Confidence: models.CalibreMatchConfidenceExact, Status: models.CalibreMatchStatusMatched,
				CalibreFingerprint: "test",
			}); err != nil {
				t.Fatal(err)
			}
		}
		return b
	}

	tests := []struct {
		name, mediaType, status          string
		matched                          bool
		ebookPath, audioPath, legacyPath string
		wantStatus, wantEbook, wantAudio string
	}{
		{name: "CWA ebook", mediaType: models.MediaTypeEbook, status: models.BookStatusWanted, matched: true, wantStatus: models.BookStatusImported},
		{name: "ordinary wanted", mediaType: models.MediaTypeEbook, status: models.BookStatusWanted},
		{name: "local ebook", mediaType: models.MediaTypeEbook, status: models.BookStatusWanted, ebookPath: "/local/book.epub", wantStatus: models.BookStatusImported},
		{name: "legacy ebook", mediaType: models.MediaTypeEbook, status: models.BookStatusWanted, legacyPath: "/local/old.epub", wantStatus: models.BookStatusImported},
		{name: "audio match cannot satisfy audio", mediaType: models.MediaTypeAudiobook, status: models.BookStatusWanted, matched: true},
		{name: "dual missing audio", mediaType: models.MediaTypeBoth, status: models.BookStatusWanted, matched: true, wantEbook: models.BookStatusImported, wantAudio: models.BookStatusWanted},
		{name: "dual satisfied", mediaType: models.MediaTypeBoth, status: models.BookStatusWanted, matched: true, audioPath: "/local/book.m4b", wantStatus: models.BookStatusImported, wantEbook: models.BookStatusImported, wantAudio: models.BookStatusImported},
		{name: "dual legacy path is not typed", mediaType: models.MediaTypeBoth, status: models.BookStatusWanted, legacyPath: "/local/old.epub", wantEbook: models.BookStatusWanted, wantAudio: models.BookStatusWanted},
		{name: "skipped match stays skipped", mediaType: models.MediaTypeBoth, status: models.BookStatusSkipped, matched: true},
	}
	get := func(b *models.Book) models.Book {
		t.Helper()
		id := fmt.Sprint(b.ID)
		rec := httptest.NewRecorder()
		h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/book/"+id, nil), "id", id))
		if rec.Code != http.StatusOK {
			t.Fatalf("book detail: %d %s", rec.Code, rec.Body.String())
		}
		var got models.Book
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := create(tt.name, tt.mediaType, tt.status, tt.matched, tt.ebookPath, tt.audioPath, tt.legacyPath)
			before, err := books.GetByID(ctx, b.ID)
			if err != nil || before == nil {
				t.Fatalf("load stored book before detail: %+v, %v", before, err)
			}
			got := get(b)
			if got.Status != before.Status || got.EffectiveStatus != tt.wantStatus ||
				got.EffectiveEbookStatus != tt.wantEbook || got.EffectiveAudiobookStatus != tt.wantAudio {
				t.Errorf("detail status = %q (effective %q, ebook %q, audio %q), want %q (%q, %q, %q)",
					got.Status, got.EffectiveStatus, got.EffectiveEbookStatus, got.EffectiveAudiobookStatus,
					before.Status, tt.wantStatus, tt.wantEbook, tt.wantAudio)
			}
			if got.FilePath != before.FilePath || got.EbookFilePath != before.EbookFilePath || got.AudiobookFilePath != before.AudiobookFilePath {
				t.Errorf("detail invented/changed a file path: %+v", got)
			}
			stored, err := books.GetByID(ctx, b.ID)
			if err != nil || stored == nil || stored.Status != before.Status || stored.FilePath != before.FilePath ||
				stored.EffectiveStatus != "" || stored.EffectiveEbookStatus != "" || stored.EffectiveAudiobookStatus != "" {
				t.Errorf("detail mutated stored book: %+v, %v", stored, err)
			}
		})
	}
	if err := settings.Set(ctx, "calibre.authoritative_library_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	b := create("disabled match", models.MediaTypeEbook, models.BookStatusWanted, true, "", "", "")
	got := get(b)
	if got.Status != models.BookStatusWanted || got.EffectiveStatus != "" || got.EffectiveEbookStatus != "" || got.EffectiveAudiobookStatus != "" {
		t.Errorf("disabled mode projects ownership: %+v", got)
	}
}

func TestAuthorDetailAndSearch_LargeAuthor(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	settings := db.NewSettingsRepo(database)
	if err := settings.Set(ctx, "calibre.authoritative_library_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, "calibre.library_path", "/configured/calibre"); err != nil {
		t.Fatal(err)
	}
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	refs := db.NewCalibreCrossReferenceRepo(database)
	author := &models.Author{ForeignID: "OL_LARGE_AUTHOR", Name: "Large Author", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	owned := make(map[int64]bool)
	for i := range 300 {
		book := &models.Book{
			ForeignID: fmt.Sprintf("OL_LARGE_%d", i), AuthorID: author.ID,
			Title: fmt.Sprintf("Book %03d", i), SortTitle: fmt.Sprintf("Book %03d", i),
			MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted, Monitored: true,
			Genres: []string{}, MetadataProvider: "openlibrary",
		}
		if err := books.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		if i < 82 {
			owned[book.ID] = true
			if err := refs.UpsertCrossReference(ctx, &models.CalibreWorkCrossReference{
				BookID: book.ID, CalibreID: book.ID, MatchMethod: "identifier:isbn",
				Confidence: models.CalibreMatchConfidenceExact, Status: models.CalibreMatchStatusMatched,
				CalibreFingerprint: "test",
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	svc := calibre.NewAuthoritativeService(settings, refs, books)
	h := NewBookHandler(books, nil, nil, nil).WithAuthoritativeService(svc)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/book?authorId=%d&limit=500", author.ID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("author list: %d %s", rec.Code, rec.Body.String())
	}
	var page bookListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	inLibrary, wanted := 0, 0
	for _, b := range page.Items {
		if b.EffectiveStatus == models.BookStatusImported {
			inLibrary++
		} else if b.Status == models.BookStatusWanted {
			wanted++
		}
	}
	if page.Total != 300 || inLibrary != 82 || wanted != 218 {
		t.Fatalf("author view: total=%d in library=%d wanted=%d, want 300/82/218", page.Total, inLibrary, wanted)
	}

	searcher := &authorSearchRecorder{calls: make(chan models.Book, 300)}
	bulk := NewBulkHandler(authors, books, nil, searcher).WithAuthoritativeService(svc)
	oldPace := searchPaceInterval
	searchPaceInterval = 0
	t.Cleanup(func() { searchPaceInterval = oldPace })
	rec = postBulk(t, bulk.AuthorsBulk, fmt.Sprintf(`{"ids":[%d],"action":"search"}`, author.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("author search: %d %s", rec.Code, rec.Body.String())
	}
	seen := make(map[int64]bool)
	for range 218 {
		select {
		case b := <-searcher.calls:
			if owned[b.ID] || seen[b.ID] {
				t.Errorf("author search dispatched owned or duplicate book %d", b.ID)
			}
			seen[b.ID] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("author search dispatched %d/218 unsatisfied books", len(seen))
		}
	}
	if len(seen) != 218 {
		t.Errorf("author search dispatched %d, want 218", len(seen))
	}
	select {
	case b := <-searcher.calls:
		t.Errorf("author search dispatched extra book %d", b.ID)
	case <-time.After(50 * time.Millisecond):
	}
}
