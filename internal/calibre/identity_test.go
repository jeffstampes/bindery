package calibre

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"

	_ "modernc.org/sqlite"
)

type identityStub struct {
	calls           []string
	diagnosticCalls []string
	result          metadata.RawBookDiscovery
}

func (s *identityStub) GetBookFromProvider(_ context.Context, provider, id string) (*models.Book, error) {
	s.diagnosticCalls = append(s.diagnosticCalls, provider+":"+id)
	if provider == "hardcover" && id == "hc:wrong" {
		return &models.Book{ForeignID: id, Title: "A different book", ProviderISBNs: []string{"9780140328721"}}, nil
	}
	return nil, metadata.ErrProviderNotConfigured
}

func (s *identityStub) DiscoverRawBookEvidence(_ context.Context, provider, id string) metadata.RawBookDiscovery {
	s.calls = append(s.calls, provider+":"+id)
	return s.result
}

func identityTestDiscovery() metadata.RawBookDiscovery {
	root := &models.Book{ForeignID: "OL100W", Title: "Provider Work Title",
		Author: &models.Author{Name: "Alice Author"}, ProviderISBNs: []string{"9780306406157"}}
	isbn := "9780306406157"
	return metadata.RawBookDiscovery{
		CanonicalProvider: "openlibrary", CanonicalForeignID: "OL100W",
		ISBNSeeds: []metadata.RawISBNSeed{{ISBN: isbn, Sources: []string{"root_book"}}},
		Observations: []metadata.RawBookObservation{
			{Provider: "openlibrary", Method: metadata.RawMethodExactBook, Seed: "OL100W", Outcome: metadata.RawOutcomeFound, Book: root},
			{Provider: "openlibrary", Method: metadata.RawMethodExactEditions, Seed: "OL100W", Outcome: metadata.RawOutcomeFound,
				Editions: []models.Edition{{ForeignID: "OL40M", ISBN13: &isbn, Format: "EPUB", IsEbook: true}}},
			{Provider: "hardcover", Method: metadata.RawMethodISBN, Seed: isbn, Outcome: metadata.RawOutcomeFound,
				Book: &models.Book{ForeignID: "hc:right", Title: "Provider Work Title", Author: &models.Author{Name: "Alice Author"}}},
			{Provider: "googlebooks", Method: metadata.RawMethodTitleAuthor, Seed: "Provider Work Title Alice Author",
				Outcome: metadata.RawOutcomeFound, Candidates: []models.Book{
					{ForeignID: "gb:first", Title: "Provider Work Title", Author: &models.Author{Name: "Alice Author"}},
					{ForeignID: "gb:second", Title: "Provider Work Title", Author: &models.Author{Name: "Alice Author"}},
				}},
			{Provider: "dnb", Method: metadata.RawMethodISBN, Seed: isbn, Outcome: metadata.RawOutcomeFailed,
				Err: errors.New("provider temporarily unavailable")},
		},
	}
}

func identityFixture(t *testing.T) (auditTestFixture, *db.CalibreIdentityRepo, *identityStub) {
	t.Helper()
	f := newAuditTestFixture(t)
	repo := db.NewCalibreIdentityRepo(f.database)
	stub := &identityStub{result: identityTestDiscovery()}
	f.svc.WithIdentityEvidence(repo, stub)
	return f, repo, stub
}

func addIdentityClaimsToCalibre(t *testing.T, root string) {
	t.Helper()
	conn, err := sql.Open("sqlite", filepath.Join(root, metadataDB))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ typ, value string }{
		{"openlibrary", "OL100W"}, {"hardcover", "hc:wrong"},
		{"google", "gb:unverified"}, {"dnb", "dnb:wrong"},
	} {
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO identifiers (book, type, val) VALUES (1, ?, ?)`, row.typ, row.value); err != nil {
			_ = conn.Close()
			t.Fatal(err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityEvidenceReconcileCanonicalRootClaimsAndAudit(t *testing.T) {
	f, repo, stub := identityFixture(t)
	addIdentityClaimsToCalibre(t, f.root)
	before := calibreDBContents(t, f.root)
	ctx := context.Background()
	res, err := f.svc.Reconcile(ctx)
	if err != nil || res.Audit == nil || res.Audit.ComparedBooks != 1 {
		t.Fatalf("reconcile: %+v %v", res, err)
	}
	if !bytes.Equal(before, calibreDBContents(t, f.root)) {
		t.Fatal("identity discovery wrote CWA metadata")
	}
	if len(stub.calls) != 1 || stub.calls[0] != "openlibrary:OL100W" {
		t.Fatalf("rooted discovery used CWA identifiers instead of Bindery's canonical work: %v", stub.calls)
	}
	got, err := repo.ListByBookID(ctx, f.book.ID)
	if err != nil || got == nil {
		t.Fatalf("persisted graph: %+v %v", got, err)
	}
	if got.CalibreID != 1 || got.RootKey != "openlibrary:OL100W" || len(got.Lookups) != 7 {
		t.Fatalf("snapshot identity/outcomes: %+v", got)
	}
	if len(stub.diagnosticCalls) != 2 || stub.diagnosticCalls[0] != "hardcover:hc:wrong" || stub.diagnosticCalls[1] != "dnb:dnb:wrong" {
		t.Fatalf("CWA IDs must be exact-looked up only after rooted discovery, bounded and never used as ISBN seeds: %v", stub.diagnosticCalls)
	}
	var root, corroborated, candidates int
	for _, e := range got.Evidence {
		if e.EditionConfidence != "unresolved" {
			t.Fatalf("work match manufactured edition certainty: %+v", e)
		}
		switch e.Status {
		case models.CalibreIdentityRoot:
			root++
		case models.CalibreIdentityCorroborated:
			corroborated++
			if e.Provider != "hardcover" || e.WorkConfidence != "high" || e.Method != metadata.RawMethodISBN || e.Seed != "9780306406157" {
				t.Fatalf("cross-provider provenance lost: %+v", e)
			}
		case models.CalibreIdentityCandidate:
			candidates++
		}
	}
	if root != 2 || corroborated != 1 || candidates != 3 {
		t.Fatalf("statuses root=%d corroborated=%d candidates=%d: %+v", root, corroborated, candidates, got.Evidence)
	}
	claims := make(map[string]string)
	for _, c := range got.Claims {
		claims[c.IdentifierType] = c.Status
	}
	if claims["openlibrary"] != models.CalibreIdentityClaimAgrees || claims["isbn"] != models.CalibreIdentityClaimAgrees ||
		claims["hardcover"] != models.CalibreIdentityClaimConflicts || claims["google"] != models.CalibreIdentityClaimUnverified ||
		claims["dnb"] != models.CalibreIdentityClaimUnverified {
		t.Fatalf("CWA claims should not count as discovery corroboration: %+v", got.Claims)
	}
	failure := false
	for _, lookup := range got.Lookups {
		if lookup.Provider == "dnb" && lookup.Outcome == models.CalibreIdentityLookupFailed {
			failure = true
		}
	}
	if !failure {
		t.Fatalf("partial provider failure was lost: %+v", got.Lookups)
	}
	// The audit may use rooted/corroborated identifiers but not the unverified
	// Google title-search candidates as evidence of CWA metadata errors.
	for _, finding := range mustIdentityFindings(t, f.findings) {
		if finding.Field == models.CalibreAuditFieldIdentifiers && finding.EvidenceKey == "google" {
			t.Fatalf("ambiguous search candidate was promoted into audit evidence: %+v", finding)
		}
	}
	if snapshot, err := f.svc.IdentitySnapshot(ctx, f.book.ID); err != nil || snapshot == nil || len(snapshot.Evidence) != len(got.Evidence) {
		t.Fatalf("readback of matched evidence: %+v %v", snapshot, err)
	}
	if _, err := f.svc.Audit(ctx); err != nil {
		t.Fatal(err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("fresh evidence needlessly fanned out: %v", stub.calls)
	}
}

func mustIdentityFindings(t *testing.T, repo *db.CalibreAuditRepo) []models.CalibreAuditFinding {
	t.Helper()
	findings, err := repo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return findings
}

func TestIdentityEvidencePeriodicRefreshAndClaimRecomparison(t *testing.T) {
	f, repo, stub := identityFixture(t)
	addIdentityClaimsToCalibre(t, f.root)
	ctx := context.Background()
	if _, err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite", filepath.Join(f.root, metadataDB))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE identifiers SET val = 'hc:right' WHERE book = 1 AND type = 'hardcover'`); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RefreshIdentity(ctx); err != nil {
		t.Fatal(err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("fresh rooted evidence should recompare CWA claims without provider calls: %v", stub.calls)
	}
	got, err := repo.ListByBookID(ctx, f.book.ID)
	if err != nil || got == nil {
		t.Fatalf("refresh evidence: %+v %v", got, err)
	}
	for _, claim := range got.Claims {
		if claim.IdentifierType == "hardcover" && claim.Status != models.CalibreIdentityClaimAgrees {
			t.Fatalf("CWA claim change not reclassified: %+v", claim)
		}
	}
	got.CheckedAt = time.Now().Add(-7 * time.Hour)
	if err := repo.ReplaceBatch(ctx, []models.CalibreIdentitySnapshot{*got}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RefreshIdentity(ctx); err != nil {
		t.Fatal(err)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("partial provider observation should retry after expiry: %v", stub.calls)
	}
}

type concurrentIdentityStub struct {
	active atomic.Int32
	peak   atomic.Int32
}

func (s *concurrentIdentityStub) DiscoverRawBookEvidence(_ context.Context, provider, id string) metadata.RawBookDiscovery {
	active := s.active.Add(1)
	for peak := s.peak.Load(); active > peak; peak = s.peak.Load() {
		if s.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	defer s.active.Add(-1)
	time.Sleep(25 * time.Millisecond)
	return metadata.RawBookDiscovery{CanonicalProvider: provider, CanonicalForeignID: id,
		Observations: []metadata.RawBookObservation{{Provider: provider, Method: metadata.RawMethodExactBook,
			Seed: id, Outcome: metadata.RawOutcomeFound, Book: &models.Book{ForeignID: id, Title: id}}},
	}
}

func TestIdentityEvidenceBoundedConcurrentWorks(t *testing.T) {
	ctx := context.Background()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	settings := db.NewSettingsRepo(database)
	for key, value := range map[string]string{"calibre.authoritative_library_enabled": "true", "calibre.library_path": "/not-read-here"} {
		if err := settings.Set(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	author := &models.Author{Name: "Provider Author", ForeignID: "OL1A"}
	if err := db.NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	books := make(map[int64]*models.Book)
	var refs []models.CalibreWorkCrossReference
	var calibreBooks []CalibreBook
	for i := 1; i <= 12; i++ {
		foreignID := fmt.Sprintf("OL%dW", i)
		book := &models.Book{Title: fmt.Sprintf("Title %d", i), AuthorID: author.ID,
			ForeignID: foreignID, MetadataProvider: "openlibrary", MediaType: models.MediaTypeEbook}
		if err := db.NewBookRepo(database).Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		books[book.ID] = book
		refs = append(refs, models.CalibreWorkCrossReference{BookID: book.ID, CalibreID: int64(i),
			Status: models.CalibreMatchStatusMatched, Confidence: models.CalibreMatchConfidenceExact})
		calibreBooks = append(calibreBooks, CalibreBook{CalibreID: int64(i), Title: book.Title,
			Identifiers: map[string]string{"openlibrary": foreignID}})
	}
	stub := &concurrentIdentityStub{}
	repo := db.NewCalibreIdentityRepo(database)
	svc := NewAuthoritativeService(settings, nil, nil).WithIdentityEvidence(repo, stub)
	all, err := svc.refreshIdentity(ctx, refs, books, NewLibraryIndex(calibreBooks))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(books) || stub.peak.Load() != 4 || stub.active.Load() != 0 {
		t.Fatalf("bounded parallel evidence: collected %d, peak %d, active %d", len(all), stub.peak.Load(), stub.active.Load())
	}
}

func TestIdentityEvidenceRetiresStaleOwnership(t *testing.T) {
	f, repo, stub := identityFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	ref, err := f.svc.crossRef.GetByBookID(ctx, f.book.ID)
	if err != nil || ref == nil {
		t.Fatalf("ownership reference: %+v %v", ref, err)
	}
	ref.Status = models.CalibreMatchStatusStale
	if err := f.svc.crossRef.UpsertCrossReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RefreshIdentity(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.ListByBookID(ctx, f.book.ID)
	if err != nil || snapshot == nil || snapshot.RootKey != "" || len(snapshot.Evidence) != 0 || len(snapshot.Claims) != 0 {
		t.Fatalf("stale owner retained identity evidence: %+v %v", snapshot, err)
	}
	if visible, err := f.svc.IdentitySnapshot(ctx, f.book.ID); err != nil || visible != nil {
		t.Fatalf("stale ownership exposed identity: %+v %v", visible, err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("stale owner retriggered provider discovery: %v", stub.calls)
	}
}

func TestIdentityEvidenceNoCWAClaimsCanManufactureCorroboration(t *testing.T) {
	f, repo, stub := identityFixture(t)
	addIdentityClaimsToCalibre(t, f.root)
	// Two conflicting Hardcover ISBN answers cannot identify a unique work.
	stub.result.Observations = append(stub.result.Observations, metadata.RawBookObservation{
		Provider: "hardcover", Method: metadata.RawMethodISBN, Seed: "9780140328721", Outcome: metadata.RawOutcomeFound,
		Book: &models.Book{ForeignID: "hc:other", Title: "Provider Work Title", Author: &models.Author{Name: "Alice Author"}},
	})
	if _, err := f.svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListByBookID(context.Background(), f.book.ID)
	if err != nil || got == nil {
		t.Fatalf("evidence: %+v %v", got, err)
	}
	for _, e := range got.Evidence {
		if e.Provider == "hardcover" && e.Status == models.CalibreIdentityCorroborated {
			t.Fatalf("multiple provider candidates became a confirmed identity: %+v", e)
		}
	}
	for _, c := range got.Claims {
		if c.IdentifierType == "hardcover" && c.Status != models.CalibreIdentityClaimUnverified {
			t.Fatalf("correlated wrong CWA IDs counted as independent evidence: %+v", c)
		}
	}
}

func TestIdentityEvidenceDisabledDoesNotDiscoverOrWrite(t *testing.T) {
	f, repo, stub := identityFixture(t)
	if err := f.settings.Set(context.Background(), "calibre.authoritative_library_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Reconcile(context.Background()); !errors.Is(err, ErrAuthoritativeDisabled) {
		t.Fatalf("disabled reconcile: %v", err)
	}
	if _, err := f.svc.Audit(context.Background()); !errors.Is(err, ErrAuthoritativeDisabled) {
		t.Fatalf("disabled audit: %v", err)
	}
	if _, err := f.svc.IdentitySnapshot(context.Background(), f.book.ID); !errors.Is(err, ErrAuthoritativeDisabled) {
		t.Fatalf("disabled read: %v", err)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("disabled discovery called providers: %v", stub.calls)
	}
	all, err := repo.ListAll(context.Background())
	if err != nil || len(all) != 0 {
		t.Fatalf("disabled mode persisted evidence: %+v %v", all, err)
	}
}
