package db

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func identityTestBooks(t *testing.T, count int) (*CalibreIdentityRepo, *models.Author, []*models.Book, func()) {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	author := &models.Author{Name: "Identity Test"}
	if err := NewAuthorRepo(database).Create(ctx, author); err != nil {
		database.Close()
		t.Fatal(err)
	}
	books := make([]*models.Book, 0, count)
	for i := range count {
		b := &models.Book{Title: fmt.Sprintf("Work %d", i), ForeignID: fmt.Sprintf("OL%dW", i+1), AuthorID: author.ID}
		if err := NewBookRepo(database).Create(ctx, b); err != nil {
			database.Close()
			t.Fatal(err)
		}
		books = append(books, b)
	}
	return NewCalibreIdentityRepo(database), author, books, func() { _ = database.Close() }
}

func TestCalibreIdentityRepoRoundTripAndScopedReplacement(t *testing.T) {
	repo, _, books, cleanup := identityTestBooks(t, 2)
	defer cleanup()
	ctx := context.Background()
	checked := time.Date(2026, 9, 26, 12, 34, 56, 123456789, time.UTC)
	first := models.CalibreIdentitySnapshot{
		BookID: books[0].ID, CalibreID: 901, RootKey: "openlibrary:OL1W", CheckedAt: checked,
		Evidence: []models.CalibreIdentityEvidence{{Key: "ol-root", CanonicalIdentity: "openlibrary:OL1W", Provider: "openlibrary",
			ForeignID: "OL1W", EditionID: "OL1M", Method: "isbn", Seed: "9781111111111", ProvenanceGroup: "isbn:9781111111111",
			Status: models.CalibreIdentityRoot, WorkConfidence: "exact", EditionConfidence: "high",
			NormalizedIdentifiers: map[string][]string{"isbn": {"9781111111111", "9782222222222"}},
			ProviderMetadata:      map[string]any{"title": "Provider edition", "languages": []any{"en"}}, CheckedAt: checked},
			{Key: "hc-candidate", CanonicalIdentity: "hardcover:11", Provider: "hardcover", ForeignID: "11",
				Status: models.CalibreIdentityCandidate, WorkConfidence: "medium"}},
		Lookups: []models.CalibreIdentityLookup{{Provider: "openlibrary", Method: "isbn", Seed: "9781111111111", Outcome: models.CalibreIdentityLookupAnswered, CheckedAt: checked},
			{Provider: "hardcover", Method: "isbn", Seed: "invalid", Outcome: models.CalibreIdentityLookupFailed, Error: "timeout"}},
		Claims: []models.CalibreIdentityClaim{{IdentifierType: "isbn", IdentifierValue: "9781111111111", Status: models.CalibreIdentityClaimAgrees, EvidenceKey: "ol-root", CheckedAt: checked},
			{IdentifierType: "asin", IdentifierValue: "B001", Status: models.CalibreIdentityClaimUnverified}},
	}
	second := models.CalibreIdentitySnapshot{BookID: books[1].ID, CalibreID: 902, RootKey: "", CheckedAt: checked,
		Lookups: []models.CalibreIdentityLookup{{Provider: "openlibrary", Method: "title", Seed: "unknown", Outcome: models.CalibreIdentityLookupEmpty}}}
	if err := repo.ReplaceBatch(ctx, []models.CalibreIdentitySnapshot{first, second}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListByBookID(ctx, first.BookID)
	if err != nil || got == nil {
		t.Fatalf("ListByBookID: %+v %v", got, err)
	}
	if got.BookID != first.BookID || got.CalibreID != first.CalibreID || got.RootKey != first.RootKey || !got.CheckedAt.Equal(checked) ||
		len(got.Evidence) != 2 || len(got.Lookups) != 2 || len(got.Claims) != 2 {
		t.Fatalf("snapshot fields/children lost: %+v", got)
	}
	if got.Evidence[1].Key != "ol-root" || got.Evidence[1].CalibreID != 901 || !reflect.DeepEqual(got.Evidence[1].NormalizedIdentifiers, first.Evidence[0].NormalizedIdentifiers) ||
		!reflect.DeepEqual(got.Evidence[1].ProviderMetadata, first.Evidence[0].ProviderMetadata) || !got.Evidence[1].CheckedAt.Equal(checked) {
		t.Fatalf("evidence provenance/metadata lost: %+v", got.Evidence)
	}
	if got.Lookups[0].Outcome != models.CalibreIdentityLookupFailed || got.Lookups[0].Error != "timeout" || got.Lookups[1].CalibreID != 901 ||
		got.Claims[1].EvidenceKey != "ol-root" || got.Claims[1].CalibreID != 901 {
		t.Fatalf("lookup/claim link lost: %+v / %+v", got.Lookups, got.Claims)
	}
	all, err := repo.ListAll(ctx)
	if err != nil || len(all) != 2 || !reflect.DeepEqual(all[first.BookID], *got) || all[second.BookID].Lookups[0].Outcome != models.CalibreIdentityLookupEmpty {
		t.Fatalf("bulk read: %+v %v", all, err)
	}
	// A refresh of one work replaces its stale evidence, claims and lookups,
	// but never deletes the other work or turns a missing provider into a match.
	if err := repo.ReplaceBatch(ctx, []models.CalibreIdentitySnapshot{{BookID: first.BookID, CalibreID: 903, CheckedAt: checked}}); err != nil {
		t.Fatal(err)
	}
	all, err = repo.ListAll(ctx)
	if err != nil || len(all) != 2 || all[first.BookID].CalibreID != 903 || all[second.BookID].CalibreID != 902 ||
		len(all[first.BookID].Evidence) != 0 || len(all[first.BookID].Lookups) != 0 || len(all[first.BookID].Claims) != 0 {
		t.Fatalf("replacement left stale rows or removed unrelated work: %+v %v", all, err)
	}
	if err := repo.ReplaceBatch(ctx, nil); err != nil {
		t.Fatal(err)
	}
	missing, err := repo.ListByBookID(ctx, -1)
	if err != nil || missing != nil {
		t.Fatalf("missing work: %+v %v", missing, err)
	}
	if err := NewBookRepo(repo.db).Delete(ctx, first.BookID); err != nil {
		t.Fatal(err)
	}
	missing, err = repo.ListByBookID(ctx, first.BookID)
	if err != nil || missing != nil {
		t.Fatalf("book FK cascade: %+v %v", missing, err)
	}
	all, err = repo.ListAll(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("cascade removed wrong snapshot: %+v %v", all, err)
	}
}

func TestCalibreIdentityRepoAtomicBatchAndClaimEvidenceFK(t *testing.T) {
	repo, _, books, cleanup := identityTestBooks(t, 102)
	defer cleanup()
	ctx := context.Background()
	initial := models.CalibreIdentitySnapshot{BookID: books[0].ID, CalibreID: 501,
		Evidence: []models.CalibreIdentityEvidence{{Key: "root", Provider: "openlibrary", Status: models.CalibreIdentityRoot}}}
	if err := repo.ReplaceBatch(ctx, []models.CalibreIdentitySnapshot{initial}); err != nil {
		t.Fatal(err)
	}
	batch := make([]models.CalibreIdentitySnapshot, 0, len(books))
	for _, b := range books {
		batch = append(batch, models.CalibreIdentitySnapshot{BookID: b.ID, CalibreID: 999,
			Lookups: []models.CalibreIdentityLookup{{Provider: "hardcover", Outcome: models.CalibreIdentityLookupNotAttempted}}})
	}
	batch[len(batch)-1].Claims = []models.CalibreIdentityClaim{{IdentifierType: "isbn", IdentifierValue: "123", Status: models.CalibreIdentityClaimAgrees, EvidenceKey: "root"}}
	if err := repo.ReplaceBatch(ctx, batch); err == nil {
		t.Fatal("expected invalid claim evidence reference in second work batch")
	}
	all, err := repo.ListAll(ctx)
	if err != nil || len(all) != 1 || all[initial.BookID].CalibreID != 501 || len(all[initial.BookID].Evidence) != 1 {
		t.Fatalf("failed batch partially committed: %+v %v", all, err)
	}
	batch[len(batch)-1].Claims[0].EvidenceKey = ""
	if err := repo.ReplaceBatch(ctx, batch); err != nil {
		t.Fatalf("valid multi-batch replacement: %v", err)
	}
	all, err = repo.ListAll(ctx)
	if err != nil || len(all) != len(batch) || len(all[initial.BookID].Evidence) != 0 ||
		all[books[len(books)-1].ID].Claims[0].EvidenceKey != "" {
		t.Fatalf("multi-batch replacement: count %d err %v", len(all), err)
	}
	if err := repo.ReplaceBatch(ctx, []models.CalibreIdentitySnapshot{{BookID: books[0].ID, CalibreID: 5},
		{BookID: 999999, CalibreID: 6}}); err == nil {
		t.Fatal("expected missing Bindery book to fail")
	}
	got, err := repo.ListByBookID(ctx, books[0].ID)
	if err != nil || got.CalibreID != 999 {
		t.Fatalf("missing-book rollback: %+v %v", got, err)
	}
}

func TestCalibreIdentityMigration092Constraints(t *testing.T) {
	repo, _, books, cleanup := identityTestBooks(t, 1)
	defer cleanup()
	ctx := context.Background()
	var version int
	if err := repo.db.QueryRowContext(ctx, `SELECT version FROM schema_migrations WHERE version = 92`).Scan(&version); err != nil || version != 92 {
		t.Fatalf("migration marker: %d %v", version, err)
	}
	var indexName string
	if err := repo.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='index' AND name='idx_calibre_identity_evidence_canonical'`).Scan(&indexName); err != nil {
		t.Fatalf("canonical identity index missing: %v", err)
	}
	if err := repo.ReplaceBatch(ctx, []models.CalibreIdentitySnapshot{{BookID: books[0].ID, CalibreID: 1,
		Claims: []models.CalibreIdentityClaim{{IdentifierType: "isbn", IdentifierValue: "1", Status: models.CalibreIdentityClaimAgrees, EvidenceKey: "not-there"}}}}); err == nil {
		t.Fatal("orphan claim accepted")
	}
	if err := repo.ReplaceBatch(ctx, []models.CalibreIdentitySnapshot{{BookID: books[0].ID, CalibreID: 1,
		Evidence: []models.CalibreIdentityEvidence{{Key: "root", Provider: "openlibrary", Status: models.CalibreIdentityRoot}},
		Claims:   []models.CalibreIdentityClaim{{IdentifierType: "isbn", IdentifierValue: "1", Status: models.CalibreIdentityClaimAgrees, EvidenceKey: "root"}}}}); err != nil {
		t.Fatal(err)
	}
	// Deleting a root with a referenced child must cascade both rows.
	if _, err := repo.db.ExecContext(ctx, `DELETE FROM calibre_identity_snapshots WHERE book_id = ?`, books[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"calibre_identity_evidence", "calibre_identity_lookups", "calibre_identity_claims"} {
		var n int
		if err := repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("cascade %s: %d %v", table, n, err)
		}
	}
}

func TestCalibreIdentityRepoChildBatchingAndValidation(t *testing.T) {
	repo, _, books, cleanup := identityTestBooks(t, 1)
	defer cleanup()
	ctx := context.Background()
	snapshot := models.CalibreIdentitySnapshot{BookID: books[0].ID, CalibreID: 77}
	for i := range 140 { // cross the 900-bind-variable boundary in each child table
		key := fmt.Sprintf("provider-%03d", i)
		snapshot.Evidence = append(snapshot.Evidence, models.CalibreIdentityEvidence{
			Key: key, Provider: "openlibrary", Status: models.CalibreIdentityCorroborated,
		})
		snapshot.Lookups = append(snapshot.Lookups, models.CalibreIdentityLookup{
			Provider: "openlibrary", Method: "isbn", Seed: key, Outcome: models.CalibreIdentityLookupTruncated,
		})
		snapshot.Claims = append(snapshot.Claims, models.CalibreIdentityClaim{
			IdentifierType: "isbn", IdentifierValue: key, Status: models.CalibreIdentityClaimConflicts, EvidenceKey: key,
		})
	}
	if err := repo.ReplaceBatch(ctx, []models.CalibreIdentitySnapshot{snapshot}); err != nil {
		t.Fatalf("bounded child inserts: %v", err)
	}
	got, err := repo.ListByBookID(ctx, snapshot.BookID)
	if err != nil || got == nil || len(got.Evidence) != 140 || len(got.Lookups) != 140 || len(got.Claims) != 140 {
		t.Fatalf("child rows lost across insert statements: %+v %v", got, err)
	}
	bad := snapshot
	bad.Evidence = []models.CalibreIdentityEvidence{{Key: "bad", BookID: books[0].ID + 1,
		Provider: "openlibrary", Status: models.CalibreIdentityRoot}}
	bad.Lookups = nil
	bad.Claims = nil
	if err := repo.ReplaceBatch(ctx, []models.CalibreIdentitySnapshot{bad}); err == nil {
		t.Fatal("accepted evidence for a different Bindery book")
	}
	got, err = repo.ListByBookID(ctx, snapshot.BookID)
	if err != nil || len(got.Evidence) != 140 {
		t.Fatalf("invalid evidence removed old snapshot: %+v %v", got, err)
	}
}
