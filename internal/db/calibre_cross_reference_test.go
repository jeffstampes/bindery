package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestCalibreCrossReferenceRepo_CRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	// 1. Setup author and books
	authors := NewAuthorRepo(database)
	author := &models.Author{Name: "Brandon Sanderson"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}

	books := NewBookRepo(database)
	b1 := &models.Book{Title: "The Way of Kings", ForeignID: "OL1W", AuthorID: author.ID}
	if err := books.Create(ctx, b1); err != nil {
		t.Fatalf("create book 1: %v", err)
	}
	b2 := &models.Book{Title: "Words of Radiance", ForeignID: "OL2W", AuthorID: author.ID}
	if err := books.Create(ctx, b2); err != nil {
		t.Fatalf("create book 2: %v", err)
	}

	repo := NewCalibreCrossReferenceRepo(database)

	// 2. Initial GetByBookID is nil
	got, err := repo.GetByBookID(ctx, b1.ID)
	if err != nil {
		t.Fatalf("GetByBookID before create: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil before insert, got %+v", got)
	}

	// 3. Upsert b1 cross-reference
	ref1 := &models.CalibreWorkCrossReference{
		BookID:             b1.ID,
		CalibreID:          101,
		MatchMethod:        "identifier:isbn",
		Confidence:         models.CalibreMatchConfidenceExact,
		Status:             models.CalibreMatchStatusMatched,
		CalibreFingerprint: "fp101",
		MatchDetailsJSON:   `{"matched_isbn":"9780765326355"}`,
	}
	if err := repo.UpsertCrossReference(ctx, ref1); err != nil {
		t.Fatalf("UpsertCrossReference: %v", err)
	}

	// Read back b1
	got1, err := repo.GetByBookID(ctx, b1.ID)
	if err != nil {
		t.Fatalf("GetByBookID after insert: %v", err)
	}
	if got1 == nil || got1.CalibreID != 101 || got1.MatchMethod != "identifier:isbn" {
		t.Fatalf("unexpected cross reference: %+v", got1)
	}

	// 4. Upsert b2 cross-reference
	ref2 := &models.CalibreWorkCrossReference{
		BookID:             b2.ID,
		CalibreID:          102,
		MatchMethod:        "fallback_title_author",
		Confidence:         models.CalibreMatchConfidenceMedium,
		Status:             models.CalibreMatchStatusAmbiguous,
		CalibreFingerprint: "fp102",
		MatchDetailsJSON:   `{"candidates":[102,103]}`,
	}
	if err := repo.UpsertCrossReference(ctx, ref2); err != nil {
		t.Fatalf("UpsertCrossReference 2: %v", err)
	}

	// 5. GetByCalibreID
	byCalibre, err := repo.GetByCalibreID(ctx, 101)
	if err != nil {
		t.Fatalf("GetByCalibreID: %v", err)
	}
	if len(byCalibre) != 1 || byCalibre[0].BookID != b1.ID {
		t.Fatalf("GetByCalibreID(101) got %+v, want book %d", byCalibre, b1.ID)
	}

	// 6. ListByStatus and GetMatchedMap
	matched, err := repo.ListByStatus(ctx, models.CalibreMatchStatusMatched)
	if err != nil {
		t.Fatalf("ListByStatus matched: %v", err)
	}
	if len(matched) != 1 || matched[0].BookID != b1.ID {
		t.Fatalf("ListByStatus(matched) got %+v, want 1 entry for book %d", matched, b1.ID)
	}

	matchedMap, err := repo.GetMatchedMap(ctx)
	if err != nil {
		t.Fatalf("GetMatchedMap: %v", err)
	}
	if len(matchedMap) != 1 || matchedMap[b1.ID].CalibreID != 101 {
		t.Fatalf("GetMatchedMap got %+v, want b1 (ID %d)", matchedMap, b1.ID)
	}

	ambiguous, err := repo.ListByStatus(ctx, models.CalibreMatchStatusAmbiguous)
	if err != nil {
		t.Fatalf("ListByStatus ambiguous: %v", err)
	}
	if len(ambiguous) != 1 || ambiguous[0].BookID != b2.ID {
		t.Fatalf("ListByStatus(ambiguous) got %+v, want 1 entry for book %d", ambiguous, b2.ID)
	}

	// 7. Update b1 cross-reference (upsert conflict handling)
	ref1.Status = models.CalibreMatchStatusStale
	ref1.CalibreFingerprint = "fp101_updated"
	if err := repo.UpsertCrossReference(ctx, ref1); err != nil {
		t.Fatalf("UpsertCrossReference update: %v", err)
	}

	updated1, err := repo.GetByBookID(ctx, b1.ID)
	if err != nil {
		t.Fatalf("GetByBookID after update: %v", err)
	}
	if updated1.Status != models.CalibreMatchStatusStale || updated1.CalibreFingerprint != "fp101_updated" {
		t.Fatalf("update failed, got %+v", updated1)
	}

	// 8. DeleteByBookID
	if err := repo.DeleteByBookID(ctx, b2.ID); err != nil {
		t.Fatalf("DeleteByBookID: %v", err)
	}
	got2, err := repo.GetByBookID(ctx, b2.ID)
	if err != nil {
		t.Fatalf("GetByBookID after delete: %v", err)
	}
	if got2 != nil {
		t.Fatalf("expected nil after delete, got %+v", got2)
	}
}

func TestCalibreCrossReferenceRepo_CascadeOnBookDelete(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	authors := NewAuthorRepo(database)
	author := &models.Author{Name: "Brandon Sanderson"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}

	books := NewBookRepo(database)
	b := &models.Book{Title: "Elantris", ForeignID: "OL3W", AuthorID: author.ID}
	if err := books.Create(ctx, b); err != nil {
		t.Fatalf("create book: %v", err)
	}

	repo := NewCalibreCrossReferenceRepo(database)
	ref := &models.CalibreWorkCrossReference{
		BookID:             b.ID,
		CalibreID:          201,
		MatchMethod:        "identifier:isbn",
		Confidence:         models.CalibreMatchConfidenceExact,
		Status:             models.CalibreMatchStatusMatched,
		CalibreFingerprint: "fp201",
	}
	if err := repo.UpsertCrossReference(ctx, ref); err != nil {
		t.Fatalf("UpsertCrossReference: %v", err)
	}

	// Verify cross reference exists
	got, err := repo.GetByBookID(ctx, b.ID)
	if err != nil || got == nil {
		t.Fatalf("expected ref, got %v, err %v", got, err)
	}

	// Delete the book from books table
	if err := books.Delete(ctx, b.ID); err != nil {
		t.Fatalf("delete book: %v", err)
	}

	// Cross reference must be deleted via CASCADE
	gotAfter, err := repo.GetByBookID(ctx, b.ID)
	if err != nil {
		t.Fatalf("GetByBookID after book delete: %v", err)
	}
	if gotAfter != nil {
		t.Fatalf("expected cross reference to be cascade-deleted, got %+v", gotAfter)
	}
}
