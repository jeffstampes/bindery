package calibre

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestCalculateFingerprint(t *testing.T) {
	b1 := &CalibreBook{
		CalibreID: 1,
		Title:     "The Way of Kings",
		SortTitle: "Way of Kings, The",
		ISBN:      "9780765326355",
		Language:  "eng",
		Authors:   []CalibreAuthor{{CalibreID: 10, Name: "Brandon Sanderson"}},
		Series:    &CalibreSeries{Name: "The Stormlight Archive", Position: 1.0},
		Formats:   []CalibreFormat{{Format: "EPUB", SizeBytes: 1024567}},
		Identifiers: map[string]string{
			"isbn": "9780765326355",
			"asin": "B003P2WO5E",
		},
	}

	fp1 := CalculateFingerprint(b1)
	if fp1 == "" {
		t.Fatal("expected non-empty fingerprint")
	}

	// Identical copy produces identical fingerprint
	b1Copy := *b1
	fp1Copy := CalculateFingerprint(&b1Copy)
	if fp1 != fp1Copy {
		t.Fatalf("expected identical fingerprint, got %q vs %q", fp1, fp1Copy)
	}

	// Modify title changes fingerprint
	b2 := *b1
	b2.Title = "The Way of Kings (Edited)"
	if CalculateFingerprint(&b2) == fp1 {
		t.Fatal("expected different fingerprint when title changes")
	}

	// Modify author changes fingerprint
	b3 := *b1
	b3.Authors = []CalibreAuthor{{CalibreID: 10, Name: "Brandon Sanderson"}, {CalibreID: 11, Name: "Co Author"}}
	if CalculateFingerprint(&b3) == fp1 {
		t.Fatal("expected different fingerprint when authors change")
	}

	// Modify identifier changes fingerprint
	b4 := *b1
	b4.Identifiers = map[string]string{"isbn": "9780765326355", "asin": "B003P2WO5E_NEW"}
	if CalculateFingerprint(&b4) == fp1 {
		t.Fatal("expected different fingerprint when identifiers change")
	}
}

func TestNormalizationHelpers(t *testing.T) {
	// 1. NormalizeTitle
	testsTitle := []struct {
		input, want string
	}{
		{"The Way of Kings", "the way of kings"},
		{"The Way of Kings: Book One!", "the way of kings book one"},
		{"  Words -- of -- Radiance  ", "words of radiance"},
		{"Oathbringer (The Stormlight Archive #3)", "oathbringer the stormlight archive 3"},
	}
	for _, tt := range testsTitle {
		got := NormalizeTitle(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}

	// 2. ExtractPrimaryTitle
	testsPrimary := []struct {
		input, want string
	}{
		{"The Way of Kings: Book One", "the way of kings"},
		{"Rhythm of War - Part 1", "rhythm of war"},
		{"Elantris (10th Anniversary Edition)", "elantris"},
		{"Standalone Title", "standalone title"},
	}
	for _, tt := range testsPrimary {
		got := ExtractPrimaryTitle(tt.input)
		if got != tt.want {
			t.Errorf("ExtractPrimaryTitle(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}

	// 3. NormalizeAuthor
	testsAuthor := []struct {
		input, want string
	}{
		{"Sanderson, Brandon", "brandon sanderson"},
		{"Brandon Sanderson", "brandon sanderson"},
		{"  King, Stephen  ", "stephen king"},
		{"J. R. R. Tolkien", "j r r tolkien"},
	}
	for _, tt := range testsAuthor {
		got := NormalizeAuthor(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeAuthor(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}

	// 4. AuthorTokensMatch
	if !AuthorTokensMatch("Brandon Sanderson", "Sanderson, Brandon") {
		t.Error("expected Brandon Sanderson to match Sanderson, Brandon")
	}
	if !AuthorTokensMatch("Stephen King", "Stephen King & Peter Straub") {
		t.Error("expected Stephen King to match co-authored name")
	}
	if AuthorTokensMatch("Brandon Sanderson", "Stephen King") {
		t.Error("expected Brandon Sanderson not to match Stephen King")
	}
}

func TestExtractWorkIdentifiers(t *testing.T) {
	isbn := "978-0-7653-2635-5"
	asin := "B003P2WO5E"
	book := &models.Book{
		ForeignID:        "OL123W",
		MetadataProvider: "openlibrary",
		ASIN:             asin,
		Editions: []models.Edition{
			{ISBN13: &isbn, ForeignID: "OL456M"},
		},
		Identifiers: []models.BookIdentifier{
			{Provider: "hardcover", ForeignID: "hc:1001"},
			{Provider: "goodreads", ForeignID: "7235538"},
		},
	}

	ids := ExtractWorkIdentifiers(book)
	if len(ids["isbn"]) == 0 || ids["isbn"][0] != "9780765326355" {
		t.Errorf("expected clean isbn 9780765326355, got %v", ids["isbn"])
	}
	if len(ids["asin"]) == 0 || ids["asin"][0] != "B003P2WO5E" {
		t.Errorf("expected asin B003P2WO5E, got %v", ids["asin"])
	}
	if len(ids["openlibrary"]) == 0 || ids["openlibrary"][0] != "OL123W" {
		t.Errorf("expected openlibrary OL123W, got %v", ids["openlibrary"])
	}
	if len(ids["openlibrary_edition"]) == 0 || ids["openlibrary_edition"][0] != "OL456M" {
		t.Errorf("expected openlibrary_edition OL456M, got %v", ids["openlibrary_edition"])
	}
	if len(ids["hardcover"]) == 0 || ids["hardcover"][0] != "1001" {
		t.Errorf("expected hardcover 1001, got %v", ids["hardcover"])
	}
	if len(ids["goodreads"]) == 0 || ids["goodreads"][0] != "7235538" {
		t.Errorf("expected goodreads 7235538, got %v", ids["goodreads"])
	}
}

func TestMatcher_DeterministicIdentifierMatching(t *testing.T) {
	calibreBooks := []CalibreBook{
		{
			CalibreID: 10,
			Title:     "The Way of Kings",
			ISBN:      "9780765326355",
			Authors:   []CalibreAuthor{{Name: "Brandon Sanderson"}},
			Identifiers: map[string]string{
				"isbn":      "9780765326355",
				"asin":      "B003P2WO5E",
				"hardcover": "1001",
			},
		},
		{
			CalibreID: 20,
			Title:     "Words of Radiance",
			ISBN:      "9780765326362",
			Authors:   []CalibreAuthor{{Name: "Brandon Sanderson"}},
			Identifiers: map[string]string{
				"isbn":        "9780765326362",
				"openlibrary": "OL200W",
			},
		},
	}

	idx := NewLibraryIndex(calibreBooks)

	// 1. Match by ISBN
	isbn := "978-0-7653-2635-5"
	b1 := &models.Book{
		ID:    1,
		Title: "Way of Kings",
		Editions: []models.Edition{
			{ISBN13: &isbn},
		},
	}
	res1 := idx.MatchWork(b1)
	if res1.Status != models.CalibreMatchStatusMatched || res1.CalibreID != 10 {
		t.Fatalf("ISBN match failed: got status=%s, calibreID=%d, want matched 10", res1.Status, res1.CalibreID)
	}
	if res1.Confidence != models.CalibreMatchConfidenceExact {
		t.Errorf("expected exact confidence, got %s", res1.Confidence)
	}

	// 2. Match by Hardcover ID
	b2 := &models.Book{
		ID:        2,
		Title:     "Way of Kings",
		ForeignID: "hc:1001",
	}
	res2 := idx.MatchWork(b2)
	if res2.Status != models.CalibreMatchStatusMatched || res2.CalibreID != 10 {
		t.Fatalf("Hardcover match failed: got status=%s, calibreID=%d, want matched 10", res2.Status, res2.CalibreID)
	}

	// 3. Match by OpenLibrary Work ID
	b3 := &models.Book{
		ID:        3,
		Title:     "Words of Radiance",
		ForeignID: "OL200W",
	}
	res3 := idx.MatchWork(b3)
	if res3.Status != models.CalibreMatchStatusMatched || res3.CalibreID != 20 {
		t.Fatalf("OpenLibrary match failed: got status=%s, calibreID=%d, want matched 20", res3.Status, res3.CalibreID)
	}
}

func TestMatcher_AmbiguousIdentifierMatching(t *testing.T) {
	// Two Calibre books sharing the same ISBN (corrupted or duplicate metadata in Calibre)
	calibreBooks := []CalibreBook{
		{
			CalibreID: 10,
			Title:     "The Way of Kings (Copy A)",
			Identifiers: map[string]string{
				"isbn": "9780765326355",
			},
		},
		{
			CalibreID: 11,
			Title:     "The Way of Kings (Copy B)",
			Identifiers: map[string]string{
				"isbn": "9780765326355",
			},
		},
	}

	idx := NewLibraryIndex(calibreBooks)

	isbn := "9780765326355"
	book := &models.Book{
		ID: 1,
		Editions: []models.Edition{
			{ISBN13: &isbn},
		},
	}

	res := idx.MatchWork(book)
	if res.Status != models.CalibreMatchStatusAmbiguous {
		t.Fatalf("expected ambiguous status, got %s", res.Status)
	}
	if len(res.CandidateIDs) != 2 || res.CandidateIDs[0] != 10 || res.CandidateIDs[1] != 11 {
		t.Fatalf("expected candidates [10, 11], got %v", res.CandidateIDs)
	}
	if res.CalibreID != 0 {
		t.Errorf("ambiguous match must not assign a CalibreID, got %d", res.CalibreID)
	}
}

func TestMatcher_ConservativeFallbackMatching(t *testing.T) {
	calibreBooks := []CalibreBook{
		{
			CalibreID: 100,
			Title:     "Mistborn: The Final Empire",
			Authors:   []CalibreAuthor{{Name: "Sanderson, Brandon"}},
		},
		{
			CalibreID: 200,
			Title:     "The Talisman",
			Authors:   []CalibreAuthor{{Name: "Stephen King"}, {Name: "Peter Straub"}},
		},
	}

	idx := NewLibraryIndex(calibreBooks)

	// 1. Title with punctuation + Last, First author match
	b1 := &models.Book{
		ID:     1,
		Title:  "Mistborn -- The Final Empire!",
		Author: &models.Author{Name: "Brandon Sanderson"},
	}
	res1 := idx.MatchWork(b1)
	if res1.Status != models.CalibreMatchStatusMatched || res1.CalibreID != 100 {
		t.Fatalf("fallback match 1 failed: status=%s, calibreID=%d", res1.Status, res1.CalibreID)
	}
	if res1.MatchMethod != "fallback_title_author" {
		t.Errorf("expected fallback_title_author method, got %s", res1.MatchMethod)
	}

	// 2. Co-authored title match
	b2 := &models.Book{
		ID:     2,
		Title:  "The Talisman",
		Author: &models.Author{Name: "Stephen King"},
	}
	res2 := idx.MatchWork(b2)
	if res2.Status != models.CalibreMatchStatusMatched || res2.CalibreID != 200 {
		t.Fatalf("fallback match 2 failed: status=%s, calibreID=%d", res2.Status, res2.CalibreID)
	}

	// 3. Unmatched book (wrong author)
	b3 := &models.Book{
		ID:     3,
		Title:  "The Talisman",
		Author: &models.Author{Name: "George R. R. Martin"},
	}
	res3 := idx.MatchWork(b3)
	if res3.Status != "unmatched" {
		t.Fatalf("expected unmatched for wrong author, got %s", res3.Status)
	}
}

func TestMatcher_AmbiguousFallbackMatching(t *testing.T) {
	// Two Calibre entries for the same title and author (e.g. EPUB vs MOBI entries)
	calibreBooks := []CalibreBook{
		{
			CalibreID: 101,
			Title:     "Dune",
			Authors:   []CalibreAuthor{{Name: "Frank Herbert"}},
		},
		{
			CalibreID: 102,
			Title:     "Dune (Special Edition)",
			Authors:   []CalibreAuthor{{Name: "Frank Herbert"}},
		},
	}

	idx := NewLibraryIndex(calibreBooks)

	b := &models.Book{
		ID:     1,
		Title:  "Dune",
		Author: &models.Author{Name: "Frank Herbert"},
	}

	res := idx.MatchWork(b)
	if res.Status != models.CalibreMatchStatusAmbiguous {
		t.Fatalf("expected ambiguous fallback status, got %s", res.Status)
	}
	if len(res.CandidateIDs) != 2 || res.CandidateIDs[0] != 101 || res.CandidateIDs[1] != 102 {
		t.Fatalf("expected candidates [101, 102], got %v", res.CandidateIDs)
	}
}

func TestRevalidateCrossReference(t *testing.T) {
	root := buildFixtureLibrary(t)
	auth, err := OpenAuthoritativeReader(root)
	if err != nil {
		t.Fatalf("OpenAuthoritativeReader: %v", err)
	}
	defer auth.Close()

	ctx := context.Background()

	// Get Calibre book 1
	cb1, err := auth.GetBook(ctx, 1)
	if err != nil {
		t.Fatalf("GetBook(1): %v", err)
	}
	fp1 := CalculateFingerprint(cb1)

	ref := &models.CalibreWorkCrossReference{
		BookID:             1,
		CalibreID:          1,
		MatchMethod:        "identifier:isbn",
		Confidence:         models.CalibreMatchConfidenceExact,
		Status:             models.CalibreMatchStatusMatched,
		CalibreFingerprint: fp1,
	}

	book := &models.Book{
		ID:    1,
		Title: "Book One",
		Editions: []models.Edition{
			{ISBN13: &cb1.ISBN},
		},
	}

	// 1. Valid cross-reference remains valid
	updated, stale, err := RevalidateCrossReference(ctx, ref, book, auth)
	if err != nil {
		t.Fatalf("RevalidateCrossReference valid: %v", err)
	}
	if stale {
		t.Fatal("expected non-stale for unchanged book")
	}
	if updated.Status != models.CalibreMatchStatusMatched {
		t.Errorf("status = %s, want matched", updated.Status)
	}

	// 2. Missing Calibre book becomes stale
	refMissing := *ref
	refMissing.CalibreID = 999
	updatedMissing, staleMissing, err := RevalidateCrossReference(ctx, &refMissing, book, auth)
	if err != nil {
		t.Fatalf("RevalidateCrossReference missing: %v", err)
	}
	if !staleMissing {
		t.Fatal("expected stale for missing book")
	}
	if updatedMissing.Status != models.CalibreMatchStatusStale {
		t.Errorf("status = %s, want stale", updatedMissing.Status)
	}

	// 3. Stale fingerprint revalidates if matcher still matches
	refOutdatedFP := *ref
	refOutdatedFP.CalibreFingerprint = "sha256:outdated00000000000"
	updatedFP, staleFP, err := RevalidateCrossReference(ctx, &refOutdatedFP, book, auth)
	if err != nil {
		t.Fatalf("RevalidateCrossReference outdated FP: %v", err)
	}
	if staleFP {
		t.Fatal("expected non-stale after re-matching modified book")
	}
	if updatedFP.CalibreFingerprint != fp1 {
		t.Errorf("expected updated fingerprint %q, got %q", fp1, updatedFP.CalibreFingerprint)
	}
}

func TestLargeLibraryIndex_Performance(t *testing.T) {
	// Build an index of 80,000 mock Calibre books
	const count = 80000
	mockBooks := make([]CalibreBook, count)

	for i := 0; i < count; i++ {
		id := int64(i + 1)
		mockBooks[i] = CalibreBook{
			CalibreID: id,
			Title:     "Book Title " + strconv.Itoa(i),
			ISBN:      "978" + fmt.Sprintf("%010d", i),
			Authors:   []CalibreAuthor{{CalibreID: int64(i), Name: "Author " + strconv.Itoa(i)}},
			Identifiers: map[string]string{
				"isbn": "978" + fmt.Sprintf("%010d", i),
				"asin": "B" + fmt.Sprintf("%09d", i),
			},
		}
	}

	// 1. Build index
	idx := NewLibraryIndex(mockBooks)

	// 2. Perform 1000 lookups
	isbnToFind := "978" + fmt.Sprintf("%010d", 42000)
	targetBook := &models.Book{
		ID: 1,
		Editions: []models.Edition{
			{ISBN13: &isbnToFind},
		},
	}

	for i := 0; i < 1000; i++ {
		res := idx.MatchWork(targetBook)
		if res.Status != models.CalibreMatchStatusMatched || res.CalibreID != 42001 {
			t.Fatalf("iter %d: expected match to calibre ID 42001, got %d (status %s)", i, res.CalibreID, res.Status)
		}
	}
}

func fileExistsRel(p string) bool {
	return fileExists(filepath.Clean(p))
}
