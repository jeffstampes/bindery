package calibre

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

func writeArtifactEPUB(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, content := range files {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func artifactFixture(t *testing.T, content string) (auditTestFixture, *db.CalibreArtifactRepo, *identityStub) {
	t.Helper()
	f, identity, source := identityFixture(t)
	artifacts := db.NewCalibreArtifactRepo(f.database)
	f.svc.WithArtifactEvidence(artifacts)
	writeArtifactEPUB(t, filepath.Join(f.root, "Alice Author", "Book One (1)", "bookone.epub"), map[string]string{
		"OEBPS/content.opf": `<?xml version="1.0"?><package xmlns:dc="http://purl.org/dc/elements/1.1/"><metadata><dc:identifier>urn:isbn:9780306406157</dc:identifier></metadata></package>`,
		"OEBPS/intro.xhtml": `<html><body>` + content + `</body></html>`,
	})
	if _, err := f.svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := identity.ListByBookID(context.Background(), f.book.ID); err != nil || got == nil {
		t.Fatalf("identity fixture missing: %+v %v", got, err)
	}
	return f, artifacts, source
}

func TestArtifactScanMatchingAndPersistence(t *testing.T) {
	f, repo, _ := artifactFixture(t, `ISBN: 978-0-306-40615-7`)
	before := calibreDBContents(t, f.root)
	got, err := f.svc.ScanArtifacts(context.Background(), f.book.ID)
	if err != nil || got == nil || len(got.Artifacts) != 1 {
		t.Fatalf("artifact scan: %+v %v", got, err)
	}
	if !bytes.Equal(before, calibreDBContents(t, f.root)) {
		t.Fatal("artifact scan modified Calibre metadata.db")
	}
	s := got.Artifacts[0]
	if s.Outcome != "scanned" || s.Format != "EPUB" || s.Method != "bindery_epub_v1" || s.SHA256 == "" || s.CorrelationGroup != "sha256:"+s.SHA256 ||
		len(s.Identifiers) != 2 || s.ScannedAt.IsZero() || s.ModifiedAt.IsZero() || s.FilePath == "" {
		t.Fatalf("lost extraction provenance: %+v", s)
	}
	for _, id := range s.Identifiers {
		if id.NormalizedValue != "9780306406157" || id.Selected || id.Status != "matches_work" ||
			id.EditionConfidence != "candidate" || len(id.EditionIDs) != 1 || id.EditionIDs[0] != "OL40M" {
			t.Fatalf("one validated ISBN only proposes an edition: %+v", id)
		}
	}
	stored, err := repo.ListByBookID(context.Background(), f.book.ID, 1)
	if err != nil || len(stored) != 1 || stored[0].Identifiers[0].ObservedValue == "" {
		t.Fatalf("artifact not persisted: %+v %v", stored, err)
	}
	if err := f.svc.RefreshIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	again, err := f.svc.IdentitySnapshot(context.Background(), f.book.ID)
	if err != nil || len(again.Artifacts) != 1 || len(again.Artifacts[0].Identifiers) != 2 {
		t.Fatalf("provider refresh erased artifact: %+v %v", again, err)
	}
}

func TestArtifactScanMultipleCandidatesAndWrongWork(t *testing.T) {
	f, _, source := artifactFixture(t, `ISBN: 9780306406157 <p>ISBN: 9780140328721</p>`)
	// This rejected provider record is diagnostic evidence of another work.
	// It may classify a conflict, never replace the canonical work identifier.
	source.result.Observations = append(source.result.Observations, metadata.RawBookObservation{
		Provider: "googlebooks", Method: metadata.RawMethodTitleAuthor, Seed: "wrong", Outcome: metadata.RawOutcomeFound,
		RejectedCandidates: []models.Book{{ForeignID: "gb:other", Title: "Other Work", ProviderISBNs: []string{"9780140328721"}}},
	})
	// Force the provider snapshot to refresh with the diagnostic conflict.
	old, err := f.svc.identity.ListByBookID(context.Background(), f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	old.CheckedAt = old.CheckedAt.Add(-8 * 24 * time.Hour)
	if err := f.svc.identity.ReplaceBatch(context.Background(), []models.CalibreIdentitySnapshot{*old}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RefreshIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.ScanArtifacts(context.Background(), f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RootKey != "openlibrary:OL100W" || len(got.Artifacts) != 1 {
		t.Fatalf("wrong work redirected identity: %+v", got)
	}
	var matched, conflicting int
	for _, id := range got.Artifacts[0].Identifiers {
		switch id.NormalizedValue {
		case "9780306406157":
			if id.Status != "matches_work" || id.EditionConfidence != "unresolved" {
				t.Fatalf("mixed-work file must not select an edition: %+v", id)
			}
			matched++
		case "9780140328721":
			if id.Status != "conflict" || id.EditionConfidence != "unresolved" {
				t.Fatalf("wrong-work ISBN gained edition confidence: %+v", id)
			}
			conflicting++
		}
	}
	if matched != 2 || conflicting != 1 {
		t.Fatalf("multiple correlated candidates lost: %+v", got.Artifacts)
	}
}

func TestArtifactScanUnresolvedEditionMultipleFormatsAndStale(t *testing.T) {
	f, _, _ := artifactFixture(t, `ISBN: 9780140328721`)
	// No independently rooted edition answers this second ISBN, even when
	// metadata.db already contains a different, correctly rooted ISBN.
	addFixtureFormat(t, f.root, 1, "PDF", "second")
	got, err := f.svc.ScanArtifacts(context.Background(), f.book.ID)
	if err != nil || len(got.Artifacts) != 2 {
		t.Fatalf("multiple formats: %+v %v", got, err)
	}
	for _, scan := range got.Artifacts {
		if scan.Format == "PDF" && (scan.Outcome != "unsupported" || len(scan.Identifiers) != 0) {
			t.Fatalf("PDF metadata fabricated file observations: %+v", scan)
		}
		if scan.Format == "EPUB" {
			for _, id := range scan.Identifiers {
				if id.NormalizedValue == "9780140328721" && (id.Status != "unverified" || id.EditionConfidence != "unresolved") {
					t.Fatalf("metadata agreement manufactured provenance: %+v", id)
				}
			}
		}
	}
	epub := filepath.Join(f.root, "Alice Author", "Book One (1)", "bookone.epub")
	if err := os.WriteFile(epub, []byte("replaced file"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale, err := f.svc.IdentitySnapshot(context.Background(), f.book.ID)
	if err != nil || !stale.Artifacts[0].Stale && !stale.Artifacts[1].Stale {
		t.Fatalf("replacement not marked stale: %+v %v", stale, err)
	}
	for _, scan := range stale.Artifacts {
		if scan.Stale {
			for _, id := range scan.Identifiers {
				if id.Status != "unverified" || id.EditionConfidence != "unresolved" {
					t.Fatalf("stale file still corroborates an edition: %+v", id)
				}
			}
		}
	}
}

func TestArtifactScanDisabledAndStaleOwnership(t *testing.T) {
	f, repo, _ := artifactFixture(t, `ISBN: 9780306406157`)
	ctx := context.Background()
	if err := f.settings.Set(ctx, "calibre.authoritative_library_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ScanArtifacts(ctx, f.book.ID); !errors.Is(err, ErrAuthoritativeDisabled) {
		t.Fatalf("disabled artifact scan: %v", err)
	}
	if rows, err := repo.ListByBookID(ctx, f.book.ID, 1); err != nil || len(rows) != 0 {
		t.Fatalf("disabled scan persisted rows: %+v %v", rows, err)
	}
	if err := f.settings.Set(ctx, "calibre.authoritative_library_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ScanArtifacts(ctx, f.book.ID); err != nil {
		t.Fatal(err)
	}
	ref, err := f.crossRef.GetByBookID(ctx, f.book.ID)
	if err != nil || ref == nil {
		t.Fatalf("ownership: %+v %v", ref, err)
	}
	ref.Status = models.CalibreMatchStatusStale
	if err := f.crossRef.UpsertCrossReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := f.svc.IdentitySnapshot(ctx, f.book.ID); err != nil || snapshot != nil {
		t.Fatalf("stale ownership exposed artifact evidence: %+v %v", snapshot, err)
	}
	if scan, err := f.svc.ScanArtifacts(ctx, f.book.ID); err != nil || scan != nil {
		t.Fatalf("stale ownership rescanned artifact: %+v %v", scan, err)
	}
}

func TestArtifactScanRejectsEscapedOwnedPath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "other.epub")
	writeArtifactEPUB(t, outside, map[string]string{"content.opf": `<package><metadata><identifier>9780306406157</identifier></metadata></package>`})
	link := filepath.Join(root, "outside.epub")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	_, err := scanOwnedArtifact(context.Background(), root, &CalibreBook{CalibreID: 1}, CalibreFormat{Format: "EPUB", FileName: "outside", AbsolutePath: link})
	if err == nil {
		t.Fatal("symlink outside library accepted as owned file")
	}
	_, err = scanOwnedArtifact(context.Background(), root, &CalibreBook{CalibreID: 1}, CalibreFormat{Format: "EPUB", FileName: "outside", AbsolutePath: outside})
	if err == nil {
		t.Fatal("out-of-library metadata.db path accepted")
	}
}

func TestArtifactScanUnsupportedLargeFormat(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "oversized.pdf")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(artifactMaxFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	scan, err := scanOwnedArtifact(context.Background(), root, &CalibreBook{CalibreID: 1},
		CalibreFormat{Format: "PDF", FileName: "oversized", AbsolutePath: path})
	if err != nil || scan.Method != "unsupported_format" || scan.Outcome != "unsupported" ||
		scan.SizeBytes != artifactMaxFileBytes+1 || scan.Error != "" || scan.SHA256 != "" ||
		scan.CorrelationGroup != "" || len(scan.Identifiers) != 0 {
		t.Fatalf("large unsupported format was classified as EPUB partial: %+v %v", scan, err)
	}
}

func TestArtifactScanBoundedPartial(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "oversized.epub")
	writeArtifactEPUB(t, path, map[string]string{
		"content.opf": `<package><metadata><identifier>9780306406157</identifier></metadata></package>`,
		"huge.xhtml":  `<html><body>` + strings.Repeat("A", artifactMaxEntryBytes+1) + `</body></html>`,
	})
	scan, err := scanOwnedArtifact(context.Background(), root, &CalibreBook{CalibreID: 1}, CalibreFormat{Format: "EPUB", FileName: "oversized", AbsolutePath: path})
	if err != nil || scan.Outcome != "partial" || scan.Error == "" {
		t.Fatalf("oversized EPUB member treated as a complete scan: %+v %v", scan, err)
	}
}

func addFixtureFormat(t *testing.T, root string, bookID int64, format, name string) {
	t.Helper()
	path := filepath.Join(root, "Alice Author", "Book One (1)", name+"."+strings.ToLower(format))
	if err := os.WriteFile(path, []byte("ISBN: 9780306406157"), 0o644); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite", filepath.Join(root, metadataDB))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `INSERT INTO data (book, format, name, uncompressed_size) VALUES (?, ?, ?, ?)`, bookID, format, name, 23); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}
