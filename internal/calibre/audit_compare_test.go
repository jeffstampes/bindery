package calibre

import (
	"slices"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func testAuditComparison() (*models.Book, *CalibreBook, *models.CalibreWorkCrossReference, []db.BookSeriesMembership) {
	isbn10 := "0-306-40615-2"
	published := time.Date(2021, 1, 5, 0, 0, 0, 0, time.UTC)
	owned := time.Date(2021, 9, 10, 0, 0, 0, 0, time.UTC)
	book := &models.Book{ID: 7, ForeignID: "OL123W", MetadataProvider: "openlibrary",
		Title: "A Rose Book", Language: "en-US", Author: &models.Author{ID: 9, ForeignID: "OL19A", Name: "Author, Alice"},
		Editions: []models.Edition{{ID: 11, ForeignID: "OL20M", Title: "  A Róse   Book! ",
			ISBN10: &isbn10, Language: "en-US", PublishDate: &published, Format: "EPUB", IsEbook: true}}}
	cb := &CalibreBook{CalibreID: 42, Title: "a rose book", Language: "eng", PublishDate: &owned,
		ISBN: "9780306406157", Identifiers: map[string]string{"isbn": "9780306406157", "openlibrary": "OL123W", "openlibrary_edition": "OL20M"},
		Authors: []CalibreAuthor{{CalibreID: 7, Name: "Alice Author"}},
		Series:  &CalibreSeries{Name: "Rose Saga", Position: 1}}
	ref := &models.CalibreWorkCrossReference{BookID: 7, CalibreID: 42,
		MatchMethod: "identifier:isbn", Confidence: models.CalibreMatchConfidenceExact,
		Status: models.CalibreMatchStatusMatched}
	series := []db.BookSeriesMembership{{BookID: 7, SeriesID: 5, SeriesForeignID: "ol-series:rose",
		SeriesTitle: "Róse Saga", Position: "01.0001", Primary: true}}
	return book, cb, ref, series
}

func TestCalibreAuditComparison_NormalizedClean(t *testing.T) {
	book, cb, ref, series := testAuditComparison()
	got := compareAuditBook(book, cb, ref, series)
	keys := []auditFieldKey{
		{models.CalibreAuditFieldIdentifiers, "openlibrary"}, {models.CalibreAuditFieldIdentifiers, "openlibrary_edition"},
		{models.CalibreAuditFieldIdentifiers, "isbn"},
		{models.CalibreAuditFieldTitle, ""}, {models.CalibreAuditFieldAuthors, ""},
		{models.CalibreAuditFieldSeries, ""}, {models.CalibreAuditFieldPosition, "ol-series:rose"},
		{models.CalibreAuditFieldLanguage, ""}, {models.CalibreAuditFieldPubDate, ""},
	}
	for _, key := range keys {
		outcome, exists := got[key]
		if !exists || outcome.different {
			t.Errorf("expected clean normalized comparison %v: %+v (exists %v)", key, outcome, exists)
		}
	}
	if len(got) != len(keys) {
		t.Errorf("unexpected comparisons: %#v", got)
	}
	original := got[auditFieldKey{models.CalibreAuditFieldTitle, ""}].finding.ComparisonFingerprint
	book.Editions[0].Title = "A rose book"
	if updated := compareAuditBook(book, cb, ref, series)[auditFieldKey{models.CalibreAuditFieldTitle, ""}].finding.ComparisonFingerprint; updated != original {
		t.Errorf("formatting-only change reopened title: %s vs %s", original, updated)
	}
}

func TestCalibreAuditComparison_FindingTypes(t *testing.T) {
	tests := []struct {
		name       string
		change     func(*models.Book, *CalibreBook, *[]db.BookSeriesMembership)
		field      string
		key        string
		kind       string
		assessment string
	}{
		{"missing work identifier", func(_ *models.Book, cb *CalibreBook, _ *[]db.BookSeriesMembership) {
			delete(cb.Identifiers, "openlibrary")
		}, models.CalibreAuditFieldIdentifiers, "openlibrary", models.CalibreAuditIdentifierMissing, models.CalibreAuditNeedsReview},
		{"conflicting work identifier", func(_ *models.Book, cb *CalibreBook, _ *[]db.BookSeriesMembership) {
			cb.Identifiers["openlibrary"] = "OL999W"
		}, models.CalibreAuditFieldIdentifiers, "openlibrary", models.CalibreAuditIdentifierConflict, models.CalibreAuditNeedsReview},
		{"conflicting edition ISBN", func(_ *models.Book, cb *CalibreBook, _ *[]db.BookSeriesMembership) {
			cb.Identifiers["isbn"] = "9781861972712"
		}, models.CalibreAuditFieldIdentifiers, "isbn", models.CalibreAuditIdentifierConflict, models.CalibreAuditAmbiguous},
		{"title", func(_ *models.Book, cb *CalibreBook, _ *[]db.BookSeriesMembership) {
			cb.Title = "A Different Book"
		}, models.CalibreAuditFieldTitle, "", models.CalibreAuditTitleDifference, models.CalibreAuditNeedsReview},
		{"author", func(_ *models.Book, cb *CalibreBook, _ *[]db.BookSeriesMembership) {
			cb.Authors = []CalibreAuthor{{Name: "Someone Else"}}
		}, models.CalibreAuditFieldAuthors, "", models.CalibreAuditAuthorDifference, models.CalibreAuditAmbiguous},
		{"series membership", func(_ *models.Book, cb *CalibreBook, _ *[]db.BookSeriesMembership) {
			cb.Series.Name = "Another Series"
		}, models.CalibreAuditFieldSeries, "", models.CalibreAuditSeriesDifference, models.CalibreAuditAmbiguous},
		{"series position", func(_ *models.Book, cb *CalibreBook, _ *[]db.BookSeriesMembership) {
			cb.Series.Position = 2
		}, models.CalibreAuditFieldPosition, "ol-series:rose", models.CalibreAuditPositionDifference, models.CalibreAuditNeedsReview},
		{"language", func(_ *models.Book, cb *CalibreBook, _ *[]db.BookSeriesMembership) {
			cb.Language = "fra"
		}, models.CalibreAuditFieldLanguage, "", models.CalibreAuditLanguageDifference, models.CalibreAuditNeedsReview},
		{"publication year", func(_ *models.Book, cb *CalibreBook, _ *[]db.BookSeriesMembership) {
			other := time.Date(2017, 9, 10, 0, 0, 0, 0, time.UTC)
			cb.PublishDate = &other
		}, models.CalibreAuditFieldPubDate, "", models.CalibreAuditPubDateDifference, models.CalibreAuditNeedsReview},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			book, cb, ref, series := testAuditComparison()
			tt.change(book, cb, &series)
			got := compareAuditBook(book, cb, ref, series)
			finding, ok := got[auditFieldKey{tt.field, tt.key}]
			if !ok || !finding.different || finding.finding.FindingType != tt.kind ||
				finding.finding.Assessment != tt.assessment || finding.finding.Reason == "" ||
				finding.finding.ComparisonFingerprint == "" || len(finding.finding.BinderyEvidence) == 0 ||
				len(finding.finding.CalibreEvidence) == 0 {
				t.Fatalf("missing or unexplained finding: %+v, exists=%v", finding, ok)
			}
			if finding.finding.MatchMethod != "identifier:isbn" || finding.finding.MatchConfidence != models.CalibreMatchConfidenceExact {
				t.Fatalf("match provenance missing: %+v", finding.finding)
			}
		})
	}
}

func TestCalibreAuditComparison_AmbiguousAndAbsentEvidence(t *testing.T) {
	book, cb, ref, series := testAuditComparison()
	book.Editions[0].ISBN10 = nil // no unambiguous match to a stored edition
	delete(cb.Identifiers, "openlibrary_edition")
	book.ReleaseDate = book.Editions[0].PublishDate
	got := compareAuditBook(book, cb, ref, series)
	if _, ok := got[auditFieldKey{models.CalibreAuditFieldPubDate, ""}]; ok {
		t.Fatal("work release date must not be compared against the owned edition")
	}
	cb.Title = "A Very Different Title"
	got = compareAuditBook(book, cb, ref, series)
	if title := got[auditFieldKey{models.CalibreAuditFieldTitle, ""}]; !title.different || title.finding.Assessment != models.CalibreAuditAmbiguous {
		t.Fatalf("work title not labelled ambiguous: %+v", title)
	}
	book, cb, ref, series = testAuditComparison()
	book.Editions = append(book.Editions, book.Editions[0]) // two editions claim the same ISBN
	got = compareAuditBook(book, cb, ref, series)
	if _, ok := got[auditFieldKey{models.CalibreAuditFieldPubDate, ""}]; ok {
		t.Fatal("ambiguous duplicate edition identifiers should suppress date assertion")
	}
	series = append(series, db.BookSeriesMembership{SeriesForeignID: "hc-series:another-series", SeriesTitle: "Rose Saga", Position: "2"})
	got = compareAuditBook(book, cb, ref, series)
	if _, ok := got[auditFieldKey{models.CalibreAuditFieldPosition, "ol-series:rose"}]; ok {
		t.Fatal("duplicate same-name provider series should suppress position assertion")
	}

	book, cb, ref, _ = testAuditComparison()
	book.ForeignID = "manual:unknown"
	book.Title = ""
	book.Author = nil
	book.Editions = nil
	book.Language = ""
	if got := compareAuditBook(book, cb, ref, nil); len(got) != 0 || auditExternalBook(book) {
		t.Fatalf("absent provider evidence should not produce findings: %#v", got)
	}
}

func TestCalibreAuditComparison_NoSelfEvidence(t *testing.T) {
	book, _, _, _ := testAuditComparison()
	for _, id := range []string{"calibre:42", "abs:other", "manual:entry", "OLnot-a-workW"} {
		book.ForeignID = id
		if auditExternalBook(book) {
			t.Errorf("not a valid external work identity: %q", id)
		}
	}
	for _, id := range []string{"calibre:42:EPUB", "abs:book", "manual:edition"} {
		if auditExternalEdition(&models.Edition{ForeignID: id}) {
			t.Errorf("owned or synthetic edition counted as provider: %q", id)
		}
	}
}

func TestCalibreAuditComparison_MultipleCalibreLanguages(t *testing.T) {
	book, cb, ref, _ := testAuditComparison()
	book.Editions = nil // compare work-level language, not an edition
	book.Language = "fra"
	cb.Languages = []string{"eng", "fre"}
	outcome := compareAuditBook(book, cb, ref, nil)[auditFieldKey{models.CalibreAuditFieldLanguage, ""}]
	if outcome.different || len(outcome.finding.CalibreEvidence) != 2 {
		t.Fatalf("a matching secondary Calibre language must not be flagged: %+v", outcome)
	}
	book.Language = "spa"
	if next := compareAuditBook(book, cb, ref, nil)[auditFieldKey{models.CalibreAuditFieldLanguage, ""}]; !next.different {
		t.Fatalf("language absent from all Calibre values should be reported: %+v", next)
	}
}

func TestCalibreAuditComparison_InvalidISBNDoesNotSelectEdition(t *testing.T) {
	book, cb, ref, series := testAuditComparison()
	delete(cb.Identifiers, "openlibrary_edition")
	book.Editions[0].ISBN10 = nil
	bad := "9781234567890" // malformed checksum, but still reportable identifier evidence
	book.Editions[0].ISBN13 = &bad
	cb.Identifiers["isbn"], cb.ISBN = bad, bad
	if selected := auditMatchedEdition(book.Editions, calibreAuditIdentifiers(cb)); selected != nil {
		t.Fatalf("malformed ISBN selected an edition: %+v", selected)
	}
	outcomes := compareAuditBook(book, cb, ref, series)
	if _, ok := outcomes[auditFieldKey{models.CalibreAuditFieldPubDate, ""}]; ok {
		t.Fatal("malformed ISBN must not support an edition publication-date comparison")
	}
	if title := outcomes[auditFieldKey{models.CalibreAuditFieldTitle, ""}]; len(title.finding.BinderyEvidence) == 0 ||
		title.finding.BinderyEvidence[0].Source != "books.title" {
		t.Fatalf("malformed ISBN selected edition title: %+v", title)
	}
	valid := "9780306406157"
	book.Editions[0].ISBN13 = &valid
	cb.Identifiers["isbn"], cb.ISBN = valid, valid
	if selected := auditMatchedEdition(book.Editions, calibreAuditIdentifiers(cb)); selected == nil {
		t.Fatal("valid common ISBN should select the unique provider edition")
	}
}

func TestCalibreAuditComparison_AudioIdentifiersNotEbookEvidence(t *testing.T) {
	book, cb, _, _ := testAuditComparison()
	book.MediaType = models.MediaTypeBoth
	book.ASIN = "B00AUDIO01" // work ASIN can come from ABS or a Hardcover audiobook
	book.Identifiers = append(book.Identifiers, models.BookIdentifier{Provider: "asin", ForeignID: "B00AUDIO03"})
	audioASIN, audioISBN := "B00AUDIO02", "9781861972712"
	book.Editions = append(book.Editions, models.Edition{
		ForeignID: "hc:audio", Format: "Audible", ASIN: &audioASIN, ISBN13: &audioISBN,
	})
	book.Editions = append(book.Editions, models.Edition{
		ForeignID: "hc:audio-cd", Format: "Audio CD", ISBN13: &audioISBN,
	})
	book.Editions = append(book.Editions, models.Edition{
		ForeignID: "hc:paper", Format: "Paperback", ISBN13: &audioISBN,
	})
	book.Editions = append(book.Editions, models.Edition{
		ForeignID: "OL22M", ISBN13: &audioISBN, // unknown format/medium
	})
	cb.Identifiers["asin"] = "B00EBOOK01"
	ids := externalAuditIdentifiers(book)
	if len(ids["asin"]) != 0 || len(ids["isbn"]) != 1 {
		t.Fatalf("audiobook identifiers became independent ebook evidence: %#v", ids)
	}
	for _, ed := range book.Editions[1:] {
		if auditExternalEdition(&ed) {
			t.Errorf("audio edition treated as ebook: %+v", ed)
		}
	}
	candidate := auditMatchEvidence(book)
	audioOnly := NewLibraryIndex([]CalibreBook{{CalibreID: 42, Title: "Unrelated work",
		Identifiers: map[string]string{"asin": book.ASIN}}})
	if match := audioOnly.MatchWork(candidate); match.Status == models.CalibreMatchStatusMatched {
		t.Fatalf("work-level audiobook ASIN established an ebook match: %+v", match)
	}
}

func TestCalibreAuditComparison_AliasEvidenceStable(t *testing.T) {
	cb := &CalibreBook{Identifiers: map[string]string{"ol": "OL123W", "openlibrary": "OL123W"}}
	first := calibreAuditIdentifiers(cb)["openlibrary"]
	for range 100 {
		if next := calibreAuditIdentifiers(cb)["openlibrary"]; !slices.Equal(first, next) {
			t.Fatalf("unordered identifier aliases cause no-op finding writes: %+v vs %+v", first, next)
		}
	}
}

func TestCalibreAuditComparison_FingerprintTracksMaterialIdentity(t *testing.T) {
	book, cb, ref, series := testAuditComparison()
	book.Editions[0].Title = "Different edition title"
	initial := compareAuditBook(book, cb, ref, series)
	fp := func(outcomes map[auditFieldKey]auditOutcome, field, key string) string {
		return outcomes[auditFieldKey{field, key}].finding.ComparisonFingerprint
	}
	book.ForeignID = "/works/OL123W"
	alias := compareAuditBook(book, cb, ref, series)
	if fp(alias, models.CalibreAuditFieldTitle, "") != fp(initial, models.CalibreAuditFieldTitle, "") {
		t.Fatal("canonical-equivalent work IDs should not reopen an ignore")
	}
	book.Editions[0].ForeignID = "OL21M"
	changedEdition := compareAuditBook(book, cb, ref, series)
	if fp(changedEdition, models.CalibreAuditFieldIdentifiers, "isbn") == fp(alias, models.CalibreAuditFieldIdentifiers, "isbn") {
		t.Fatal("a different provider edition with the same ISBN must recheck ignored identifier findings")
	}
	ref.MatchMethod = "fallback_title_author"
	ref.Confidence = models.CalibreMatchConfidenceMedium
	changedMatch := compareAuditBook(book, cb, ref, series)
	if fp(changedMatch, models.CalibreAuditFieldTitle, "") == fp(changedEdition, models.CalibreAuditFieldTitle, "") {
		t.Fatal("a changed match basis must recheck an ignored finding")
	}
}

func TestCalibreAuditComparison_CrossProviderEvidenceProvenance(t *testing.T) {
	book, cb, ref, series := testAuditComparison()
	book.Author.ForeignID = "hc:author-1"
	book.Editions[0].ForeignID = "hc:edition-2"
	series[0].SeriesForeignID = "hc-series:3"
	series[0].Position = "2"
	got := compareAuditBook(book, cb, ref, series)
	for _, key := range []auditFieldKey{
		{models.CalibreAuditFieldAuthors, ""},
		{models.CalibreAuditFieldTitle, ""},
		{models.CalibreAuditFieldPosition, "hc-series:3"},
	} {
		outcome, exists := got[key]
		if !exists || len(outcome.finding.BinderyEvidence) == 0 || outcome.finding.BinderyEvidence[0].Provider != "hardcover" {
			t.Errorf("comparison used the work's provider instead of the evidence's: %+v exists=%v", outcome, exists)
		}
	}
	book.Author.ForeignID = "manual:author"
	if _, exists := compareAuditBook(book, cb, ref, series)[auditFieldKey{models.CalibreAuditFieldAuthors, ""}]; exists {
		t.Fatal("manual author name is not independent provider evidence")
	}
}
