package db

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func TestCalibreArtifactRepoWritebackHistoryAndOwnership(t *testing.T) {
	identity, _, books, done := identityTestBooks(t, 2)
	defer done()
	ctx := context.Background()
	repo := NewCalibreArtifactRepo(identity.db)
	before := time.Now().UTC().Add(-3 * time.Hour)
	scan := models.CalibreArtifactScan{BookID: books[0].ID, CalibreID: 11, Format: "EPUB", FileName: "book",
		FilePath: "Book (11)/book.epub", SHA256: "old", SizeBytes: 50,
		Method: "bindery_epub_v1", Outcome: "scanned", ModifiedAt: before, ScannedAt: before,
		Identifiers: []models.CalibreArtifactIdentifier{{NormalizedValue: "9780306406157"}}}
	if err := repo.ReplaceForBook(ctx, scan.BookID, 11, []models.CalibreArtifactScan{scan}); err != nil {
		t.Fatal(err)
	}
	event := models.CalibreArtifactWriteback{BookID: scan.BookID, CalibreID: 11, Source: "polish_books", WrittenAt: before.Add(time.Hour)}
	if err := repo.RecordWriteback(ctx, event); err != nil {
		t.Fatal(err)
	}
	scan.SHA256, scan.ScannedAt = "after", before.Add(2*time.Hour)
	if err := repo.ReplaceForBook(ctx, scan.BookID, 11, []models.CalibreArtifactScan{scan}); err != nil {
		t.Fatal(err)
	}
	history, err := repo.ListHistoryByBookID(ctx, scan.BookID, 11)
	if err != nil || len(history) != 1 || !history[0].Historical || history[0].SHA256 != "old" ||
		!history[0].ScannedAt.Equal(before) || history[0].Identifiers[0].NormalizedValue != "9780306406157" {
		t.Fatalf("old scan lineage was overwritten: %+v %v", history, err)
	}
	current, err := repo.ListByBookID(ctx, scan.BookID, 11)
	if err != nil || len(current) != 1 || current[0].SHA256 != "after" {
		t.Fatalf("new scan missing: %+v %v", current, err)
	}
	writebacks, err := repo.ListWritebacks(ctx, scan.BookID, 11)
	if err != nil || len(writebacks) != 1 || !writebacks[0].WrittenAt.Equal(event.WrittenAt) {
		t.Fatalf("write-back event missing: %+v %v", writebacks, err)
	}
	if previous, err := repo.ListHistoryByBookID(ctx, scan.BookID, 12); err != nil || len(previous) != 0 {
		t.Fatalf("older ownership leaked into new link: %+v %v", previous, err)
	}
	if previous, err := repo.ListWritebacks(ctx, scan.BookID, 12); err != nil || len(previous) != 0 {
		t.Fatalf("older writeback leaked into new link: %+v %v", previous, err)
	}
	if err := repo.RecordWriteback(ctx, models.CalibreArtifactWriteback{BookID: scan.BookID, CalibreID: 11,
		Source: "polish_books", WrittenAt: time.Now().UTC().Add(time.Hour)}); err == nil {
		t.Fatal("future report accepted")
	}
	all, err := repo.ListCurrent(ctx)
	if err != nil || len(all[scan.BookID]) != 1 {
		t.Fatalf("bulk scan inventory: %+v %v", all, err)
	}
}

func TestCalibreArtifactRepoIndependentOfProviderRefresh(t *testing.T) {
	identity, _, books, done := identityTestBooks(t, 2)
	defer done()
	ctx := context.Background()
	var version int
	if err := identity.db.QueryRowContext(ctx, `SELECT version FROM schema_migrations WHERE version = 93`).Scan(&version); err != nil || version != 93 {
		t.Fatalf("artifact migration: %d %v", version, err)
	}
	repo := NewCalibreArtifactRepo(identity.db)
	now := time.Now().UTC()
	scan := models.CalibreArtifactScan{BookID: books[0].ID, CalibreID: 11, Format: "EPUB", FileName: "book",
		FilePath: "Book (11)/book.epub", SHA256: "digest", CorrelationGroup: "sha256:digest", SizeBytes: 50,
		Method: "bindery_epub_v1", Outcome: "scanned", ModifiedAt: now, ScannedAt: now,
		Identifiers: []models.CalibreArtifactIdentifier{{ObservedValue: "978-0-306-40615-7", NormalizedValue: "9780306406157", Source: "epub_content", Location: "text.xhtml"}}}
	if err := repo.ReplaceForBook(ctx, scan.BookID, 11, []models.CalibreArtifactScan{scan}); err != nil {
		t.Fatal(err)
	}
	if err := identity.ReplaceBatch(ctx, []models.CalibreIdentitySnapshot{{BookID: scan.BookID, CalibreID: 11, RootKey: "openlibrary:OL1W"}}); err != nil {
		t.Fatal(err)
	}
	if err := identity.ReplaceBatch(ctx, []models.CalibreIdentitySnapshot{{BookID: scan.BookID, CalibreID: 11}}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListByBookID(ctx, scan.BookID, 11)
	if err != nil || len(got) != 1 || got[0].Identifiers[0].ObservedValue != scan.Identifiers[0].ObservedValue ||
		got[0].SHA256 != scan.SHA256 || !got[0].ScannedAt.Equal(now) || got[0].FilePath != scan.FilePath {
		t.Fatalf("artifact lost on provider refresh: %+v %v", got, err)
	}
	if older, err := repo.ListByBookID(ctx, scan.BookID, 12); err != nil || len(older) != 0 {
		t.Fatalf("old Calibre link leaked: %+v %v", older, err)
	}
	if err := repo.ReplaceForBook(ctx, scan.BookID, 12, nil); err != nil {
		t.Fatal(err)
	}
	if gone, err := repo.ListByBookID(ctx, scan.BookID, 11); err != nil || len(gone) != 0 {
		t.Fatalf("removed format retained: %+v %v", gone, err)
	}
	if err := repo.ReplaceForBook(ctx, books[1].ID, 2, []models.CalibreArtifactScan{{BookID: books[1].ID, CalibreID: 2,
		Format: "PDF", FileName: "other", Method: "bindery_epub_v1", Outcome: "unsupported"}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceForBook(ctx, books[1].ID, 3, []models.CalibreArtifactScan{{BookID: books[0].ID}}); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	if kept, err := repo.ListByBookID(ctx, books[1].ID, 2); err != nil || len(kept) != 1 {
		t.Fatalf("failed replacement was not atomic: %+v %v", kept, err)
	}
	if err := NewBookRepo(identity.db).Delete(ctx, books[1].ID); err != nil {
		t.Fatal(err)
	}
	if gone, err := repo.ListByBookID(ctx, books[1].ID, 2); err != nil || len(gone) != 0 {
		t.Fatalf("book cascade failed: %+v %v", gone, err)
	}
}
