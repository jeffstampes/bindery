package calibre

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"

	_ "modernc.org/sqlite"
)

type auditTestFixture struct {
	svc      *AuthoritativeService
	database *sql.DB
	settings *db.SettingsRepo
	crossRef *db.CalibreCrossReferenceRepo
	books    *db.BookRepo
	editions *db.EditionRepo
	findings *db.CalibreAuditRepo
	book     *models.Book
	edition  *models.Edition
	root     string
}

func newAuditTestFixture(t *testing.T) auditTestFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx := context.Background()
	root := buildFixtureLibrary(t)
	updateFixtureCalibreISBN(t, root, "9780306406157") // valid ISBN-13 for edition matching
	settings := db.NewSettingsRepo(database)
	if err := settings.Set(ctx, "calibre.authoritative_library_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, "calibre.library_path", root); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{Name: "Alice Author", ForeignID: "OL19A"}
	if err := db.NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	books := db.NewBookRepo(database)
	book := &models.Book{AuthorID: author.ID, ForeignID: "OL100W", MetadataProvider: "openlibrary",
		Title: "Provider Work Title", Language: "fra", MediaType: models.MediaTypeEbook,
		Status: models.BookStatusWanted}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	editions := db.NewEditionRepo(database)
	pubdate := time.Date(2018, 6, 4, 0, 0, 0, 0, time.UTC)
	isbn := "9780306406157" // audit fixture's checksum-valid ISBN
	edition := &models.Edition{ForeignID: "OL40M", BookID: book.ID, Title: "Provider Edition Title",
		ISBN13: &isbn, Language: "fra", PublishDate: &pubdate, Format: "EPUB", IsEbook: true}
	if err := editions.Upsert(ctx, edition); err != nil {
		t.Fatal(err)
	}
	crossRef := db.NewCalibreCrossReferenceRepo(database)
	findings := db.NewCalibreAuditRepo(database)
	svc := NewAuthoritativeService(settings, crossRef, books).WithEditions(editions).WithAudit(findings)
	return auditTestFixture{svc, database, settings, crossRef, books, editions, findings, book, edition, root}
}

func auditFindingFor(t *testing.T, repo *db.CalibreAuditRepo, field, key string) models.CalibreAuditFinding {
	t.Helper()
	list, err := repo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range list {
		if f.Field == field && f.EvidenceKey == key {
			return f
		}
	}
	t.Fatalf("no finding %s:%s in %+v", field, key, list)
	return models.CalibreAuditFinding{}
}

func updateFixtureCalibreISBN(t *testing.T, root, isbn string) {
	t.Helper()
	conn, err := sql.Open("sqlite", filepath.Join(root, metadataDB))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `UPDATE identifiers SET val = ? WHERE book = 1 AND type = 'isbn'`, isbn); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func updateFixtureCalibreTitle(t *testing.T, root, title string) {
	t.Helper()
	conn, err := sql.Open("sqlite", filepath.Join(root, metadataDB))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `UPDATE books SET title = ? WHERE id = 1`, title); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func calibreDBContents(t *testing.T, root string) []byte {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, metadataDB))
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func TestCalibreAudit_ReaderRetainsAllLanguagesWithoutCovers(t *testing.T) {
	root := buildFixtureLibrary(t)
	conn, err := sql.Open("sqlite", filepath.Join(root, metadataDB))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE languages (id INTEGER PRIMARY KEY, lang_code TEXT NOT NULL)`,
		`CREATE TABLE books_languages_link (book INTEGER, lang_code INTEGER, item_order INTEGER)`,
		`INSERT INTO languages (id, lang_code) VALUES (1, 'eng'), (2, 'fre')`,
		`INSERT INTO books_languages_link (book, lang_code, item_order) VALUES (1, 1, 0), (1, 2, 1)`,
	} {
		if _, err := conn.ExecContext(context.Background(), stmt); err != nil {
			_ = conn.Close()
			t.Fatal(err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	books, err := reader.AllBooksMetadata(context.Background())
	if err != nil || len(books) != 3 {
		t.Fatalf("read metadata-only snapshot: %d books, %v", len(books), err)
	}
	first := books[0]
	if first.Language != "eng" || len(first.Languages) != 2 || first.Languages[1] != "fre" ||
		first.CoverPath != "" || first.ISBN == "" || len(first.Authors) != 1 {
		t.Fatalf("metadata snapshot lost languages or probed cover: %+v", first)
	}
	full, err := reader.AllBooks(context.Background())
	if err != nil || len(full) != 3 || full[0].CoverPath == "" {
		t.Fatalf("legacy full snapshot lost cover path: %+v %v", full, err)
	}
}

func TestCalibreAudit_ReconcileAndRecheck(t *testing.T) {
	f := newAuditTestFixture(t)
	ctx := context.Background()
	before := calibreDBContents(t, f.root)
	res, err := f.svc.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != 1 || res.Audit == nil || res.Audit.ComparedBooks != 1 || res.Audit.Findings < 4 {
		t.Fatalf("reconcile did not audit the matched book: %+v", res)
	}
	if !bytes.Equal(before, calibreDBContents(t, f.root)) {
		t.Fatal("reconciliation/audit modified Calibre metadata.db")
	}
	ref, err := f.crossRef.GetByBookID(ctx, f.book.ID)
	if err != nil || ref == nil || ref.Status != models.CalibreMatchStatusMatched {
		t.Fatalf("ownership cross-reference was changed by audit: %+v err=%v", ref, err)
	}
	finding := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, "")
	if finding.State != models.CalibreAuditUnresolved || finding.MatchMethod != "identifier:isbn" ||
		finding.CalibreEvidence[0].Source != "calibre.books.title" ||
		finding.BinderyEvidence[0].ForeignID != f.edition.ForeignID {
		t.Fatalf("initial finding lacks match or provider provenance: %+v", finding)
	}
	if ok, err := f.findings.Ignore(ctx, finding.ID, finding.ComparisonFingerprint); err != nil || !ok {
		t.Fatalf("ignore title: %v %v", ok, err)
	}
	if _, err := f.database.ExecContext(ctx, `UPDATE editions SET title = ? WHERE id = ?`,
		"  PROVIDER  EDITION TITLE! ", f.edition.ID); err != nil {
		t.Fatal(err)
	}
	if audit, err := f.svc.Audit(ctx); err != nil || audit.Updated != 1 {
		t.Fatalf("formatting-only change should refresh evidence without reopening: %+v %v", audit, err)
	}
	if audit, err := f.svc.Audit(ctx); err != nil || audit.Updated != 0 {
		t.Fatalf("unchanged recheck must not rewrite ignored result: %+v %v", audit, err)
	}
	ignored := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, "")
	if ignored.State != models.CalibreAuditIgnored || ignored.ComparisonFingerprint != finding.ComparisonFingerprint {
		t.Fatalf("equivalent evidence reopened ignore: %+v", ignored)
	}
	// A material authoritative change reopens exactly the old finding.
	updateFixtureCalibreTitle(t, f.root, "Changed Calibre Title")
	before = calibreDBContents(t, f.root)
	res, err = f.svc.Reconcile(ctx)
	if err != nil || res.Audit == nil || res.Audit.ComparedBooks != 1 {
		t.Fatalf("reconciliation did not recheck changed Calibre metadata: %+v %v", res, err)
	}
	if !bytes.Equal(before, calibreDBContents(t, f.root)) {
		t.Fatal("audit wrote to Calibre after a Calibre edit")
	}
	reopened := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, "")
	if reopened.State != models.CalibreAuditUnresolved || reopened.ID != ignored.ID ||
		reopened.ComparisonFingerprint == ignored.ComparisonFingerprint {
		t.Fatalf("Calibre edit did not reopen existing ignored finding: %+v", reopened)
	}
	if ok, err := f.findings.Ignore(ctx, reopened.ID, reopened.ComparisonFingerprint); err != nil || !ok {
		t.Fatalf("ignore updated title: %v %v", ok, err)
	}
	beforeProvider := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, "")
	// An external edition update also reopens the ignore for the same field.
	if _, err := f.database.ExecContext(ctx, `UPDATE editions SET title = ? WHERE id = ?`, "Different Provider Title", f.edition.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Audit(ctx); err != nil {
		t.Fatal(err)
	}
	reopened = auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, "")
	if reopened.State != models.CalibreAuditUnresolved || reopened.ComparisonFingerprint == beforeProvider.ComparisonFingerprint ||
		reopened.BinderyEvidence[0].Value != "Different Provider Title" {
		t.Fatalf("provider edit did not reopen ignored finding: %+v", reopened)
	}
	if _, err := f.database.ExecContext(ctx, `UPDATE editions SET title = ? WHERE id = ?`, "Changed Calibre Title", f.edition.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Audit(ctx); err != nil {
		t.Fatal(err)
	}
	resolved := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, "")
	if resolved.State != models.CalibreAuditResolved || resolved.ID != finding.ID {
		t.Fatalf("equivalent current values did not resolve finding: %+v", resolved)
	}
	// A stale reference is not a clean result. The last comparison is kept
	// for inspection, but marked unmatched until a confident match returns.
	ref.Status = models.CalibreMatchStatusStale
	if err := f.crossRef.UpsertCrossReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if res, err := f.svc.Audit(ctx); err != nil || res.ComparedBooks != 0 {
		t.Fatalf("audit should skip stale cross-reference: %+v %v", res, err)
	}
	unmatched := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, "")
	if unmatched.State != models.CalibreAuditUnmatched {
		t.Fatalf("stale reference did not become unmatched: %+v", unmatched)
	}
	ref.Status = models.CalibreMatchStatusMatched
	if err := f.crossRef.UpsertCrossReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Audit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, ""); got.State != models.CalibreAuditResolved {
		t.Fatalf("recovered match did not resolve cleaned evidence: %+v", got)
	}
	if err := f.settings.Set(ctx, "calibre.authoritative_library_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Audit(ctx); !errors.Is(err, ErrAuthoritativeDisabled) {
		t.Fatalf("disabled audit returned %v", err)
	}
	if got := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, ""); got.State != models.CalibreAuditResolved {
		t.Fatalf("disabled mode mutated finding: %+v", got)
	}
}

func TestCalibreAudit_SkipsUnconfidentAndAbsentProviderData(t *testing.T) {
	f := newAuditTestFixture(t)
	ctx := context.Background()
	// Stored cross-reference is not enough to audit an unconfident match.
	ref := &models.CalibreWorkCrossReference{BookID: f.book.ID, CalibreID: 1,
		MatchMethod: "identifier:ambiguous", Confidence: models.CalibreMatchConfidenceAmbiguous,
		Status: models.CalibreMatchStatusAmbiguous}
	if err := f.crossRef.UpsertCrossReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if res, err := f.svc.Audit(ctx); err != nil || res.ComparedBooks != 0 || res.Updated != 0 {
		t.Fatalf("ambiguous link produced findings: %+v %v", res, err)
	}
	ref.Status = models.CalibreMatchStatusMatched
	ref.Confidence = models.CalibreMatchConfidenceExact
	if err := f.crossRef.UpsertCrossReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	// Without an independent external identity the book and edition cannot
	// be used as provider evidence even though an ISBN could still match.
	if _, err := f.database.ExecContext(ctx, `UPDATE books SET foreign_id = 'manual:fixture', metadata_provider = 'openlibrary' WHERE id = ?`, f.book.ID); err != nil {
		t.Fatal(err)
	}
	if res, err := f.svc.Audit(ctx); err != nil || res.ComparedBooks != 0 || res.Updated != 0 {
		t.Fatalf("synthetic provider identity produced findings: %+v %v", res, err)
	}
}

func TestCalibreAudit_SelfSourcedEditionCannotValidateMatch(t *testing.T) {
	f := newAuditTestFixture(t)
	ctx := context.Background()
	if _, err := f.database.ExecContext(ctx, `UPDATE editions SET foreign_id = 'calibre:1:EPUB' WHERE id = ?`, f.edition.ID); err != nil {
		t.Fatal(err)
	}
	ref := &models.CalibreWorkCrossReference{BookID: f.book.ID, CalibreID: 1,
		MatchMethod: "identifier:isbn", Confidence: models.CalibreMatchConfidenceExact,
		Status: models.CalibreMatchStatusMatched}
	if err := f.crossRef.UpsertCrossReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	result, err := f.svc.Audit(ctx)
	if err != nil || result.ComparedBooks != 0 || result.Updated != 0 {
		t.Fatalf("a Calibre-imported edition ISBN must not validate independent evidence: %+v %v", result, err)
	}
}

func TestCalibreAudit_InvalidISBNCannotValidateMatch(t *testing.T) {
	f := newAuditTestFixture(t)
	ctx := context.Background()
	bad := "9781234567890" // checksum-invalid, still valid as a human-review finding value
	updateFixtureCalibreISBN(t, f.root, bad)
	if _, err := f.database.ExecContext(ctx, `UPDATE editions SET isbn_13 = ? WHERE id = ?`, bad, f.edition.ID); err != nil {
		t.Fatal(err)
	}
	ref := &models.CalibreWorkCrossReference{BookID: f.book.ID, CalibreID: 1,
		MatchMethod: "identifier:isbn", Confidence: models.CalibreMatchConfidenceExact,
		Status: models.CalibreMatchStatusMatched}
	if err := f.crossRef.UpsertCrossReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	result, err := f.svc.Audit(ctx)
	if err != nil || result.ComparedBooks != 0 || result.Updated != 0 {
		t.Fatalf("malformed ISBN alone must not validate a provider comparison: %+v %v", result, err)
	}
}

func TestCalibreAudit_IgnoreSurvivesTemporaryUnmatch(t *testing.T) {
	f := newAuditTestFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	initial := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, "")
	if ok, err := f.findings.Ignore(ctx, initial.ID, initial.ComparisonFingerprint); err != nil || !ok {
		t.Fatalf("ignore title: ok=%t err=%v", ok, err)
	}
	ref, err := f.crossRef.GetByBookID(ctx, f.book.ID)
	if err != nil || ref == nil {
		t.Fatalf("read cross-reference: %+v %v", ref, err)
	}
	ref.Status = models.CalibreMatchStatusStale
	if err := f.crossRef.UpsertCrossReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Audit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, ""); got.State != models.CalibreAuditUnmatched {
		t.Fatalf("temporarily unmatched finding not recorded: %+v", got)
	}
	ref.Status = models.CalibreMatchStatusMatched
	if err := f.crossRef.UpsertCrossReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Audit(ctx); err != nil {
		t.Fatal(err)
	}
	got := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, "")
	if got.State != models.CalibreAuditIgnored || got.ComparisonFingerprint != initial.ComparisonFingerprint {
		t.Fatalf("identical comparison should retain ignore after temporary unmatch: %+v", got)
	}
}

func TestCalibreAudit_MatchDowngradeReopensIgnore(t *testing.T) {
	f := newAuditTestFixture(t)
	ctx := context.Background()
	// Title/author fallback stays valid after the exact-match ISBN disappears.
	if _, err := f.database.ExecContext(ctx, `UPDATE books SET title = 'Book One' WHERE id = ?`, f.book.ID); err != nil {
		t.Fatal(err)
	}
	series := &models.Series{ForeignID: "ol-series:provider", Title: "Provider Saga"}
	seriesRepo := db.NewSeriesRepo(f.database)
	if err := seriesRepo.CreateOrGet(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertBookLink(ctx, series.ID, f.book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	initial := auditFindingFor(t, f.findings, models.CalibreAuditFieldSeries, "")
	if initial.MatchConfidence != models.CalibreMatchConfidenceExact {
		t.Fatalf("unexpected starting match: %+v", initial)
	}
	if ok, err := f.findings.Ignore(ctx, initial.ID, initial.ComparisonFingerprint); err != nil || !ok {
		t.Fatalf("ignore title: ok=%t err=%v", ok, err)
	}
	if _, err := f.database.ExecContext(ctx, `UPDATE editions SET isbn_13 = NULL WHERE id = ?`, f.edition.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Audit(ctx); err != nil {
		t.Fatal(err)
	}
	got := auditFindingFor(t, f.findings, models.CalibreAuditFieldSeries, "")
	if got.State != models.CalibreAuditUnresolved || got.MatchMethod != "fallback_title_author" ||
		got.MatchConfidence != models.CalibreMatchConfidenceMedium {
		t.Fatalf("changed matching evidence must reopen old ignore: %+v", got)
	}
}

func TestCalibreAudit_ConcurrentPassesAreSerialized(t *testing.T) {
	f := newAuditTestFixture(t)
	ctx := context.Background()
	ref := &models.CalibreWorkCrossReference{BookID: f.book.ID, CalibreID: 1,
		MatchMethod: "identifier:isbn", Confidence: models.CalibreMatchConfidenceExact,
		Status: models.CalibreMatchStatusMatched}
	if err := f.crossRef.UpsertCrossReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	const passes = 6
	started := make(chan struct{})
	results := make(chan struct {
		audit *AuditResult
		err   error
	}, passes)
	for range passes {
		go func() {
			<-started
			audit, err := f.svc.Audit(ctx)
			results <- struct {
				audit *AuditResult
				err   error
			}{audit, err}
		}()
	}
	close(started)
	written := 0
	for range passes {
		result := <-results
		if result.err != nil || result.audit == nil || result.audit.ComparedBooks != 1 {
			t.Errorf("concurrent audit failed: %+v %v", result.audit, result.err)
			continue
		}
		written += result.audit.Updated
	}
	findings, err := f.findings.List(ctx)
	if err != nil || written != len(findings) {
		t.Fatalf("overlapping snapshots wrote findings more than once: written=%d rows=%d err=%v", written, len(findings), err)
	}
}

func TestCalibreAudit_ManyDistinctCalibreBooks(t *testing.T) {
	f := newAuditTestFixture(t)
	ctx := context.Background()
	conn, err := sql.Open("sqlite", filepath.Join(f.root, metadataDB))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	const count = 130 // three write batches, with a distinct Calibre ID per work
	for i := range count {
		calibreID := int64(1000 + i)
		workID := fmt.Sprintf("OL%dW", calibreID)
		if _, err := tx.ExecContext(ctx, `INSERT INTO books (id, title, path) VALUES (?, ?, '')`,
			calibreID, fmt.Sprintf("Owned Book %d", i)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO books_authors_link (book, author) VALUES (?, 1)`, calibreID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO identifiers (book, type, val) VALUES (?, 'openlibrary', ?)`,
			calibreID, workID); err != nil {
			t.Fatal(err)
		}
		book := &models.Book{AuthorID: f.book.AuthorID, ForeignID: workID, MetadataProvider: "openlibrary",
			Title: fmt.Sprintf("Provider Book %d", i), MediaType: models.MediaTypeEbook}
		if err := f.books.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	before := calibreDBContents(t, f.root)
	res, err := f.svc.Reconcile(ctx)
	if err != nil || res.Audit == nil || res.Audit.ComparedBooks != count+1 || res.Audit.Updated < count {
		t.Fatalf("distinct-library reconciliation missed audits: %+v %v", res, err)
	}
	if !bytes.Equal(before, calibreDBContents(t, f.root)) {
		t.Fatal("reconciliation modified the Calibre library")
	}
	if result, err := f.svc.Audit(ctx); err != nil || result.ComparedBooks != count+1 || result.Updated != 0 {
		t.Fatalf("unchanged distinct-library recheck rewrote findings: %+v %v", result, err)
	}
}

func TestCalibreAudit_BulkRecheck(t *testing.T) {
	f := newAuditTestFixture(t)
	ctx := context.Background()
	// The small Calibre fixture suffices: each external work is a distinct
	// match to the same owned ISBN, exercising the pass's bulk-load and batched
	// persistence shape without constructing a huge on-disk Calibre library.
	const additional = 160
	for i := range additional {
		book := &models.Book{AuthorID: f.book.AuthorID, ForeignID: fmt.Sprintf("OL%dW", i+1000),
			MetadataProvider: "openlibrary", Title: fmt.Sprintf("Provider Title %d", i),
			MediaType: models.MediaTypeEbook}
		if err := f.books.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		isbn := "9780306406157"
		ed := &models.Edition{BookID: book.ID, ForeignID: fmt.Sprintf("OL%dM", i+1000),
			Title: book.Title, ISBN13: &isbn, IsEbook: true}
		if err := f.editions.Upsert(ctx, ed); err != nil {
			t.Fatal(err)
		}
		ref := &models.CalibreWorkCrossReference{BookID: book.ID, CalibreID: 1,
			MatchMethod: "identifier:isbn", Confidence: models.CalibreMatchConfidenceExact,
			Status: models.CalibreMatchStatusMatched}
		if err := f.crossRef.UpsertCrossReference(ctx, ref); err != nil {
			t.Fatal(err)
		}
	}
	result, err := f.svc.Audit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.ComparedBooks != additional || result.Findings < additional || result.Updated < additional {
		t.Fatalf("bulk audit missed matched books: %+v", result)
	}
	list, err := f.findings.List(ctx)
	if err != nil || len(list) != result.Updated {
		t.Fatalf("bulk finding persistence count = %d, want %d, err %v", len(list), result.Updated, err)
	}
	result, err = f.svc.Audit(ctx)
	if err != nil || result.Updated != 0 {
		t.Fatalf("unchanged large-library pass rewrote findings: %+v err %v", result, err)
	}
}
