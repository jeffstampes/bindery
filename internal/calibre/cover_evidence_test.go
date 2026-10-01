package calibre

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func TestOwnedCoverUsesOnlyActiveCalibreBookAndConfinesFile(t *testing.T) {
	f, _, _ := identityFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.svc.IdentitySnapshot(ctx, f.book.ID)
	if err != nil || snapshot == nil || !snapshot.HasOwnedCover {
		t.Fatalf("current cover not advertised: %+v %v", snapshot, err)
	}
	cover, err := f.svc.OpenOwnedCover(ctx, f.book.ID)
	if err != nil || cover == nil {
		t.Fatalf("current cover not opened: %v", err)
	}
	got, readErr := io.ReadAll(cover)
	_ = cover.Close()
	if readErr != nil || string(got) != "cover" {
		t.Fatalf("owned cover bytes: %q %v", got, readErr)
	}

	path := filepath.Join(f.root, "Alice Author", "Book One (1)", "cover.jpg")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if cover, err := f.svc.OpenOwnedCover(ctx, f.book.ID); err != nil || cover != nil {
		t.Fatalf("missing cover served: %v %v", cover, err)
	}
	snapshot, err = f.svc.IdentitySnapshot(ctx, f.book.ID)
	if err != nil || snapshot == nil || snapshot.HasOwnedCover {
		t.Fatalf("missing cover advertised: %+v %v", snapshot, err)
	}

	outside := filepath.Join(t.TempDir(), "secret.jpg")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if cover, err := f.svc.OpenOwnedCover(ctx, f.book.ID); err != nil || cover != nil {
		t.Fatalf("escaped symlink served: %v %v", cover, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.root, "Alice Author", "Book Two (2)", "cover.jpg"), path); err != nil {
		t.Fatal(err)
	}
	if cover, err := f.svc.OpenOwnedCover(ctx, f.book.ID); err != nil || cover != nil {
		t.Fatalf("another book's cover served: %v %v", cover, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, (8<<20)+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if cover, err := f.svc.OpenOwnedCover(ctx, f.book.ID); err != nil || cover != nil {
		t.Fatalf("oversized cover served: %v %v", cover, err)
	}
	if err := f.crossRef.UpsertCrossReference(ctx, &models.CalibreWorkCrossReference{
		BookID: f.book.ID, CalibreID: 2, Confidence: models.CalibreMatchConfidenceExact,
	}); err != nil {
		t.Fatal(err)
	}
	if cover, err := f.svc.OpenOwnedCover(ctx, f.book.ID); err != nil || cover != nil {
		t.Fatalf("stale ownership cover served: %v %v", cover, err)
	}
	if err := f.settings.Set(ctx, "calibre.authoritative_library_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if cover, err := f.svc.OpenOwnedCover(ctx, f.book.ID); cover != nil || err == nil {
		t.Fatalf("disabled mode served cover: %v %v", cover, err)
	}
}

func TestEditionImageURLRejectsUnsafeValues(t *testing.T) {
	for _, raw := range []string{"javascript:alert(1)", "data:image/jpeg;base64,xxx", "http://example.org/cover.jpg",
		"https://user:pass@example.org/cover.jpg", "https://example.org/cover.jpg#fragment",
		" https://example.org/cover.jpg", "https://example.org:bad/cover.jpg"} {
		if got := editionImageURL(raw); got != "" {
			t.Errorf("unsafe cover %q persisted as %q", raw, got)
		}
	}
	const valid = "https://covers.openlibrary.org/b/id/40-L.jpg"
	if got := editionImageURL(valid); got != valid {
		t.Fatalf("valid provider cover lost: %q", got)
	}
}

func TestExactProviderEditionArtworkIsAdvisoryAndPersistsWithItsRecord(t *testing.T) {
	f := newAuditTestFixture(t)
	ctx := context.Background()
	first, second := "9780306406157", "9781861972712"
	book := &models.Book{ID: f.book.ID, ForeignID: "OL100W", MetadataProvider: "openlibrary"}
	cb := &CalibreBook{CalibreID: 1, Identifiers: map[string]string{}}
	raw := identityTestDiscovery()
	raw.Observations[1].Editions = nil
	raw.Observations[1].Editions = append(raw.Observations[1].Editions, []models.Edition{
		{ForeignID: "OL40M", ISBN13: &first, Format: "EPUB", IsEbook: true, ImageURL: "https://covers.openlibrary.org/b/id/40-L.jpg"},
		{ForeignID: "OL41M", ISBN13: &second, Format: "EPUB", IsEbook: true, ImageURL: "https://covers.openlibrary.org/b/id/41-L.jpg"},
	}...)
	snapshot := buildIdentitySnapshot(book, cb, raw)
	repo := db.NewCalibreIdentityRepo(f.database)
	if err := repo.ReplaceBatch(ctx, []models.CalibreIdentitySnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.ListByBookID(ctx, book.ID)
	if err != nil || stored == nil {
		t.Fatalf("load edition artwork: %v", err)
	}
	covers := map[string]string{}
	for _, e := range stored.Evidence {
		if e.EditionID != "" {
			covers[e.EditionID], _ = e.ProviderMetadata["imageUrl"].(string)
		}
	}
	if covers["OL40M"] != "https://covers.openlibrary.org/b/id/40-L.jpg" ||
		covers["OL41M"] != "https://covers.openlibrary.org/b/id/41-L.jpg" {
		t.Fatalf("edition cover provenance lost: %v", covers)
	}

	for _, scenario := range []struct {
		name   string
		change func(*models.CalibreIdentitySnapshot, *CalibreBook)
	}{
		{"ambiguous", func(*models.CalibreIdentitySnapshot, *CalibreBook) {}},
		{"high", func(_ *models.CalibreIdentitySnapshot, c *CalibreBook) { c.Identifiers["isbn"] = first }},
		{"exact", func(s *models.CalibreIdentitySnapshot, _ *CalibreBook) {
			s.Artifacts = []models.CalibreArtifactScan{observedISBN(first)}
			s.Artifacts[0].CalibreID = 1
			s.Artifacts[0].BookID = book.ID
		}},
		{"unresolved", func(s *models.CalibreIdentitySnapshot, _ *CalibreBook) { s.RootKey = "" }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			copySnapshot, err := repo.ListByBookID(ctx, book.ID)
			if err != nil || copySnapshot == nil {
				t.Fatalf("load independent evidence: %v", err)
			}
			owned := &CalibreBook{CalibreID: 1, Identifiers: map[string]string{}}
			scenario.change(copySnapshot, owned)
			withCovers := ResolveOwnedEdition(*copySnapshot, owned)
			for i := range copySnapshot.Evidence {
				delete(copySnapshot.Evidence[i].ProviderMetadata, "imageUrl")
			}
			withoutCovers := ResolveOwnedEdition(*copySnapshot, owned)
			if !reflect.DeepEqual(withCovers, withoutCovers) {
				t.Fatalf("artwork changed edition confidence, candidates or eligibility: %+v vs %+v", withCovers, withoutCovers)
			}
		})
	}
}
