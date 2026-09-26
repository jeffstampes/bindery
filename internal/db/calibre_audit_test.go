package db

import (
	"context"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestCalibreAuditRepo_IgnoreAndRecheck(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	author := &models.Author{Name: "External Author"}
	if err := NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "External Title", ForeignID: "OL45W"}
	books := NewBookRepo(database)
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	repo := NewCalibreAuditRepo(database)
	finding := models.CalibreAuditFinding{
		BookID: book.ID, CalibreID: 42, Field: models.CalibreAuditFieldTitle,
		FindingType: models.CalibreAuditTitleDifference, Assessment: models.CalibreAuditAmbiguous,
		CalibreEvidence: []models.CalibreAuditEvidence{{Value: "Calibre Title", Source: "calibre.books.title"}},
		BinderyEvidence: []models.CalibreAuditEvidence{{Value: "External Title", Source: "books.title", Provider: "openlibrary", ForeignID: "OL45W"}},
		MatchMethod:     "identifier:isbn", MatchConfidence: models.CalibreMatchConfidenceExact,
		Reason: "Different titles; editions may differ.", ComparisonFingerprint: "fp-1", State: models.CalibreAuditUnresolved,
	}
	if _, err := repo.Apply(ctx, []models.CalibreAuditFinding{finding}); err != nil {
		t.Fatal(err)
	}
	list, err := repo.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List: findings=%v err=%v", list, err)
	}
	initial := list[0]
	if initial.ID == 0 || initial.CreatedAt.IsZero() || initial.UpdatedAt.IsZero() || initial.Assessment != models.CalibreAuditAmbiguous ||
		len(initial.CalibreEvidence) != 1 || len(initial.BinderyEvidence) != 1 || initial.BinderyEvidence[0].ForeignID != "OL45W" {
		t.Fatalf("finding lost provenance or timestamps: %+v", initial)
	}
	if ok, err := repo.Ignore(ctx, initial.ID, "stale-fingerprint"); err != nil || ok {
		t.Fatalf("stale ignore: ok=%v err=%v", ok, err)
	}
	if ok, err := repo.Ignore(ctx, initial.ID, initial.ComparisonFingerprint); err != nil || !ok {
		t.Fatalf("ignore: ok=%v err=%v", ok, err)
	}
	if _, err := repo.Apply(ctx, []models.CalibreAuditFinding{finding}); err != nil {
		t.Fatal(err)
	}
	list, err = repo.List(ctx)
	if err != nil || len(list) != 1 || list[0].State != models.CalibreAuditIgnored {
		t.Fatalf("equivalent recheck did not preserve ignore: %v %v", list, err)
	}
	if ok, err := repo.Ignore(ctx, initial.ID, initial.ComparisonFingerprint); err != nil || ok {
		t.Fatalf("re-ignoring an already ignored row: ok=%v err=%v", ok, err)
	}
	finding.State = models.CalibreAuditUnmatched
	if _, err := repo.Apply(ctx, []models.CalibreAuditFinding{finding}); err != nil {
		t.Fatal(err)
	}
	list, err = repo.List(ctx)
	if err != nil || list[0].State != models.CalibreAuditUnmatched || list[0].IgnoredFingerprint != initial.ComparisonFingerprint {
		t.Fatalf("temporary unmatch lost the reviewer decision: %+v %v", list, err)
	}
	finding.State = models.CalibreAuditUnresolved
	if _, err := repo.Apply(ctx, []models.CalibreAuditFinding{finding}); err != nil {
		t.Fatal(err)
	}
	list, err = repo.List(ctx)
	if err != nil || list[0].State != models.CalibreAuditIgnored {
		t.Fatalf("unchanged comparison did not restore ignore: %+v %v", list, err)
	}
	finding.ComparisonFingerprint = "fp-2"
	finding.BinderyEvidence[0].Value = "Another External Title"
	if _, err := repo.Apply(ctx, []models.CalibreAuditFinding{finding}); err != nil {
		t.Fatal(err)
	}
	list, err = repo.List(ctx)
	if err != nil || len(list) != 1 || list[0].State != models.CalibreAuditUnresolved || list[0].ComparisonFingerprint != "fp-2" ||
		list[0].IgnoredFingerprint != "" || list[0].ID != initial.ID {
		t.Fatalf("changed evidence did not reopen finding in place: %v %v", list, err)
	}
	finding.State = models.CalibreAuditResolved
	finding.Reason = "Values now equivalent."
	if _, err := repo.Apply(ctx, []models.CalibreAuditFinding{finding}); err != nil {
		t.Fatal(err)
	}
	list, err = repo.List(ctx)
	if err != nil || list[0].State != models.CalibreAuditResolved {
		t.Fatalf("resolved recheck: %v %v", list, err)
	}
	finding.State = models.CalibreAuditUnmatched
	if _, err := repo.Apply(ctx, []models.CalibreAuditFinding{finding}); err != nil {
		t.Fatal(err)
	}
	list, err = repo.List(ctx)
	if err != nil || list[0].State != models.CalibreAuditUnmatched {
		t.Fatalf("unmatched recheck: %v %v", list, err)
	}
	if err := books.Delete(ctx, book.ID); err != nil {
		t.Fatal(err)
	}
	list, err = repo.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("book delete should cascade findings: %v %v", list, err)
	}
}

func TestCalibreAuditRepo_SkipsBookDeletedBeforeApply(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	author := &models.Author{Name: "External Author"}
	if err := NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	books := NewBookRepo(database)
	repo := NewCalibreAuditRepo(database)
	makeBook := func(title, id string) *models.Book {
		t.Helper()
		book := &models.Book{AuthorID: author.ID, Title: title, ForeignID: id}
		if err := books.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		return book
	}
	live := makeBook("Live", "OL42W")
	deleted := makeBook("Deleted", "OL43W")
	if err := books.Delete(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}
	finding := func(bookID int64) models.CalibreAuditFinding {
		return models.CalibreAuditFinding{BookID: bookID, CalibreID: 1, Field: models.CalibreAuditFieldTitle,
			FindingType: models.CalibreAuditTitleDifference, Assessment: models.CalibreAuditNeedsReview,
			ComparisonFingerprint: "fp-1", State: models.CalibreAuditUnresolved}
	}
	written, err := repo.Apply(ctx, []models.CalibreAuditFinding{finding(live.ID), finding(deleted.ID)})
	if err != nil || written != 1 {
		t.Fatalf("one deleted book should not abort the whole audit pass: written=%d err=%v", written, err)
	}
	got, err := repo.List(ctx)
	if err != nil || len(got) != 1 || got[0].BookID != live.ID {
		t.Fatalf("live finding missing after deletion: %+v %v", got, err)
	}
}

func TestCalibreAuditRepo_BatchedApplyIsAtomic(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	author := &models.Author{Name: "Author"}
	if err := NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	books := NewBookRepo(database)
	repo := NewCalibreAuditRepo(database)
	var batch []models.CalibreAuditFinding
	for i := range 90 { // spans the fixed-size bulk statement boundary
		b := &models.Book{AuthorID: author.ID, Title: fmt.Sprintf("Book %d", i), ForeignID: fmt.Sprintf("OL%dW", i+1)}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		batch = append(batch, models.CalibreAuditFinding{BookID: b.ID, CalibreID: int64(i + 1),
			Field: models.CalibreAuditFieldLanguage, FindingType: models.CalibreAuditLanguageDifference,
			Assessment: models.CalibreAuditNeedsReview, ComparisonFingerprint: fmt.Sprintf("fp-%d", i), State: models.CalibreAuditUnresolved})
	}
	if _, err := repo.Apply(ctx, batch); err != nil {
		t.Fatal(err)
	}
	list, err := repo.List(ctx)
	if err != nil || len(list) != len(batch) {
		t.Fatalf("batched finding count = %d, error %v", len(list), err)
	}
	batch[0].Reason = "changed"
	batch[len(batch)-1].State = models.CalibreAuditIgnored // human decisions must use Ignore
	if _, err := repo.Apply(ctx, batch); err == nil {
		t.Fatal("expected invalid second batch to abort the transaction")
	}
	list, err = repo.List(ctx)
	if err != nil || len(list) != len(batch) || list[0].Reason == "changed" {
		t.Fatalf("failed batch partially committed: %v / %d", err, len(list))
	}
}
