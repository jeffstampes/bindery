package calibre

import (
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

func identifierReviewFixture(t *testing.T) (auditTestFixture, *sql.DB, *db.CalibreIdentifierAttemptRepo, models.CalibreAuditFinding) {
	t.Helper()
	f, conn := setupAuditTags(t)
	ctx := context.Background()
	if _, err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot := models.CalibreIdentitySnapshot{
		BookID: f.book.ID, CalibreID: 1, RootKey: "openlibrary:OL100W", CheckedAt: time.Now().UTC(),
		Evidence: []models.CalibreIdentityEvidence{{
			Key: "root", CanonicalIdentity: "openlibrary:OL100W", Provider: "openlibrary", ForeignID: "OL100W",
			Method: metadata.RawMethodExactBook, Seed: "OL100W", ProvenanceGroup: "openlibrary:OL100W",
			Status: models.CalibreIdentityRoot, WorkConfidence: "exact", EditionConfidence: "unresolved",
			NormalizedIdentifiers: map[string][]string{"openlibrary": {"OL100W"}},
		}},
	}
	identity := db.NewCalibreIdentityRepo(f.database)
	if err := identity.ReplaceBatch(ctx, []models.CalibreIdentitySnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	attempts := db.NewCalibreIdentifierAttemptRepo(f.database)
	f.svc.WithIdentityEvidence(identity, nil).WithIdentifierAttempts(attempts)
	if _, err := f.svc.Audit(ctx); err != nil {
		t.Fatal(err)
	}
	finding := auditFindingFor(t, f.findings, models.CalibreAuditFieldIdentifiers, "openlibrary")
	if finding.FindingType != models.CalibreAuditIdentifierMissing || finding.State != models.CalibreAuditUnresolved {
		t.Fatalf("expected missing work ID: %+v", finding)
	}
	return f, conn, attempts, finding
}

func TestIdentifierReconciliationRequiresSeparateOptInAndRootedEvidence(t *testing.T) {
	f, _, _, finding := identifierReviewFixture(t)
	ctx := context.Background()
	if _, err := f.svc.IdentifierProposals(ctx, finding.ID); !errors.Is(err, ErrIdentifierWriteDisabled) {
		t.Fatalf("authoritative alone authorized identifier proposals: %v", err)
	}
	if _, _, err := f.svc.AddIdentifier(ctx, finding.ID, 3, finding.ComparisonFingerprint, "OL100W"); !errors.Is(err, ErrIdentifierWriteDisabled) {
		t.Fatalf("authoritative alone authorized writes: %v", err)
	}
	if err := f.settings.Set(ctx, identifierWriteSetting, "true"); err != nil {
		t.Fatal(err)
	}
	proposals, err := f.svc.IdentifierProposals(ctx, finding.ID)
	if err != nil || len(proposals) != 1 || proposals[0].IdentifierType != "openlibrary" ||
		proposals[0].ProposedValue != "OL100W" || proposals[0].CurrentValue != "" ||
		len(proposals[0].EvidenceKeys) != 1 || proposals[0].EvidenceKeys[0] != "root" || proposals[0].Action != "add" {
		t.Fatalf("rooted work proposal: %+v %v", proposals, err)
	}
	// A client cannot swap in an unrelated work, even with a valid fingerprint.
	if _, _, err := f.svc.AddIdentifier(ctx, finding.ID, 3, finding.ComparisonFingerprint, "OL999W"); !errors.Is(err, ErrIdentifierProposalStale) {
		t.Fatalf("browser-controlled identifier accepted: %v", err)
	}
	log, err := f.svc.IdentifierAttempts(ctx, finding.ID)
	if err != nil || len(log) != 1 || log[0].Outcome != "rejected" || log[0].ActorUserID != 3 {
		t.Fatalf("rejected attempt not retained: %+v %v", log, err)
	}
	if err := f.settings.Set(ctx, identifierWriteSetting, "false"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.IdentifierProposals(ctx, finding.ID); !errors.Is(err, ErrIdentifierWriteDisabled) {
		t.Fatalf("disabling identifier writes had no effect: %v", err)
	}
}

func TestIdentifierReconciliationProposalReadFailureRecordsFailedAttempt(t *testing.T) {
	f, conn, attempts, finding := identifierReviewFixture(t)
	ctx := context.Background()
	if err := f.settings.Set(ctx, identifierWriteSetting, "true"); err != nil {
		t.Fatal(err)
	}
	// The review was valid, but Calibre becomes unavailable before apply.
	if err := f.settings.Set(ctx, "calibre.library_path", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	attempt, _, err := f.svc.AddIdentifier(ctx, finding.ID, 7, finding.ComparisonFingerprint, "OL100W")
	if !errors.Is(err, ErrMissingMetadataDB) || attempt == nil {
		t.Fatalf("missing Calibre reader: attempt=%+v err=%v", attempt, err)
	}
	log, err := attempts.ListByFinding(ctx, finding.ID)
	if err != nil || len(log) != 1 || log[0].Outcome != "failed" ||
		!strings.Contains(log[0].Error, ErrMissingMetadataDB.Error()) {
		t.Fatalf("reader failure misclassified or lost its detail: %+v err=%v", log, err)
	}
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM identifiers WHERE book = 1 AND type = 'openlibrary'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("read failure wrote Calibre: count=%d err=%v", count, err)
	}
}

func TestIdentifierReconciliationAddResolvesByOrdinaryAudit(t *testing.T) {
	f, conn, attempts, finding := identifierReviewFixture(t)
	ctx := context.Background()
	if err := f.settings.Set(ctx, identifierWriteSetting, "true"); err != nil {
		t.Fatal(err)
	}
	bookFile := filepath.Join(f.root, "Alice Author/Book One (1)/bookone.epub")
	before, err := os.ReadFile(bookFile)
	if err != nil {
		t.Fatal(err)
	}
	attempt, reauditErr, err := f.svc.AddIdentifier(ctx, finding.ID, 7, finding.ComparisonFingerprint, "OL100W")
	if err != nil || reauditErr != "" || attempt == nil || attempt.Outcome != "applied" {
		t.Fatalf("add identifier: %+v audit=%q err=%v", attempt, reauditErr, err)
	}
	var typ, val, title string
	if err := conn.QueryRowContext(ctx, `SELECT type, val FROM identifiers WHERE book = 1 AND type = 'openlibrary'`).Scan(&typ, &val); err != nil || typ != "openlibrary" || val != "OL100W" {
		t.Fatalf("Calibre identifier not applied: %q=%q %v", typ, val, err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT title FROM books WHERE id = 1`).Scan(&title); err != nil || title != "Book One" {
		t.Fatalf("curated title changed: %q %v", title, err)
	}
	after, err := os.ReadFile(bookFile)
	if err != nil || string(after) != string(before) {
		t.Fatalf("owned ebook changed: %v", err)
	}
	latest := auditFindingFor(t, f.findings, models.CalibreAuditFieldIdentifiers, "openlibrary")
	if latest.State != models.CalibreAuditResolved {
		t.Fatalf("ordinary audit failed to resolve identifier: %+v", latest)
	}
	log, err := attempts.ListByFinding(ctx, finding.ID)
	if err != nil || len(log) != 1 || log[0].Outcome != "applied" || log[0].ActorUserID != 7 ||
		len(log[0].EvidenceKeys) != 1 || log[0].EvidenceKeys[0] != "root" {
		t.Fatalf("successful provenance attempt: %+v %v", log, err)
	}
	if _, _, err := f.svc.AddIdentifier(ctx, finding.ID, 7, finding.ComparisonFingerprint, "OL100W"); !errors.Is(err, ErrIdentifierProposalStale) {
		t.Fatalf("stale repeated add: %v", err)
	}
	// A later audit-retention cleanup must not erase the operator's history.
	if _, err := f.database.ExecContext(ctx, `DELETE FROM calibre_metadata_audit_findings WHERE id = ?`, finding.ID); err != nil {
		t.Fatal(err)
	}
	if kept, err := attempts.ListByFinding(ctx, finding.ID); err != nil || len(kept) != 2 || kept[1].Outcome != "applied" {
		t.Fatalf("deleted finding erased write history: %+v %v", kept, err)
	}
}

func TestIdentifierReconciliationRejectsStaleAndAmbiguousWithoutTouchingDecisions(t *testing.T) {
	f, conn, attempts, finding := identifierReviewFixture(t)
	ctx := context.Background()
	if err := f.settings.Set(ctx, identifierWriteSetting, "true"); err != nil {
		t.Fatal(err)
	}
	// Changed Calibre state between preview and apply is not permission.
	if _, err := conn.ExecContext(ctx, `INSERT INTO identifiers(book,type,val) VALUES (1,'openlibrary','OL999W')`); err != nil {
		t.Fatal(err)
	}
	if proposals, err := f.svc.IdentifierProposals(ctx, finding.ID); err != nil || len(proposals) != 0 {
		t.Fatalf("stale preview remained actionable: %+v %v", proposals, err)
	}
	if _, _, err := f.svc.AddIdentifier(ctx, finding.ID, 2, finding.ComparisonFingerprint, "OL100W"); !errors.Is(err, ErrIdentifierProposalStale) {
		t.Fatalf("stale apply: %v", err)
	}
	log, err := attempts.ListByFinding(ctx, finding.ID)
	if err != nil || len(log) != 1 || log[0].Outcome != "rejected" || log[0].Error != "stale or ineligible identifier proposal" {
		t.Fatalf("stale attempt not recorded: %+v %v", log, err)
	}
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM identifiers WHERE book = 1 AND type = 'openlibrary' AND val = 'OL100W'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale proposal wrote approved value to Calibre: count=%d err=%v", count, err)
	}
	if finding := auditFindingFor(t, f.findings, models.CalibreAuditFieldIdentifiers, "openlibrary"); finding.State != models.CalibreAuditUnresolved {
		t.Fatalf("failed approval altered finding: %+v", finding)
	}
	// Edition-level ambiguity never becomes an add proposal, even if someone
	// stores a similarly shaped finding in the advisory queue.
	ambiguous := models.CalibreAuditFinding{BookID: f.book.ID, CalibreID: 1, Field: models.CalibreAuditFieldIdentifiers,
		EvidenceKey: "openlibrary_edition", FindingType: models.CalibreAuditIdentifierMissing,
		Assessment: models.CalibreAuditAmbiguous, State: models.CalibreAuditUnresolved,
		CalibreEvidence: []models.CalibreAuditEvidence{{Source: "calibre.identifiers.openlibrary_edition"}},
		BinderyEvidence: []models.CalibreAuditEvidence{{Value: "OL40M", Source: "editions.foreign_id"}}, ComparisonFingerprint: "edition"}
	if _, err := f.findings.Apply(ctx, []models.CalibreAuditFinding{ambiguous}); err != nil {
		t.Fatal(err)
	}
	list, err := f.findings.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		if item.EvidenceKey == "openlibrary_edition" {
			if proposals, err := f.svc.IdentifierProposals(ctx, item.ID); err != nil || len(proposals) != 0 {
				t.Fatalf("ambiguous edition proposed: %+v %v", proposals, err)
			}
		}
	}
}

func TestIdentifierReconciliationRejectsNewCompetingOwnership(t *testing.T) {
	f, conn, attempts, finding := identifierReviewFixture(t)
	ctx := context.Background()
	if err := f.settings.Set(ctx, identifierWriteSetting, "true"); err != nil {
		t.Fatal(err)
	}
	if proposals, err := f.svc.IdentifierProposals(ctx, finding.ID); err != nil || len(proposals) != 1 {
		t.Fatalf("expected initial eligible review: %+v %v", proposals, err)
	}
	// The original book remains byte-for-byte unchanged, but a different
	// Calibre book now claims its exact ISBN. The saved cross-reference is stale.
	if _, err := conn.ExecContext(ctx, `INSERT INTO identifiers (book, type, val) VALUES (2, 'isbn', '9780306406157')`); err != nil {
		t.Fatal(err)
	}
	if proposals, err := f.svc.IdentifierProposals(ctx, finding.ID); err != nil || len(proposals) != 0 {
		t.Fatalf("ambiguous ownership remained eligible: %+v %v", proposals, err)
	}
	if _, _, err := f.svc.AddIdentifier(ctx, finding.ID, 7, finding.ComparisonFingerprint, "OL100W"); !errors.Is(err, ErrIdentifierProposalStale) {
		t.Fatalf("old ownership authorized a write: %v", err)
	}
	if log, err := attempts.ListByFinding(ctx, finding.ID); err != nil || len(log) != 1 || log[0].Outcome != "rejected" {
		t.Fatalf("stale attempt not retained: %+v %v", log, err)
	}
}

func TestIdentifierReconciliationFailedCalibreWriteRetainsEvidenceAndAudit(t *testing.T) {
	f, conn, attempts, finding := identifierReviewFixture(t)
	ctx := context.Background()
	if err := f.settings.Set(ctx, identifierWriteSetting, "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `CREATE TRIGGER block_identifier BEFORE INSERT ON identifiers BEGIN SELECT RAISE(ABORT, 'simulated writer failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.AddIdentifier(ctx, finding.ID, 3, finding.ComparisonFingerprint, "OL100W"); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	log, err := attempts.ListByFinding(ctx, finding.ID)
	if err != nil || len(log) != 1 || log[0].Outcome != "failed" || log[0].Error == "" {
		t.Fatalf("failed attempt not inspectable: %+v %v", log, err)
	}
	if result := auditFindingFor(t, f.findings, models.CalibreAuditFieldIdentifiers, "openlibrary"); result.State != models.CalibreAuditUnresolved {
		t.Fatalf("failed write altered audit: %+v", result)
	}
	snapshot, err := f.svc.IdentitySnapshot(ctx, f.book.ID)
	if err != nil || snapshot == nil || len(snapshot.Evidence) != 1 {
		t.Fatalf("failed write lost independent evidence: %+v %v", snapshot, err)
	}
}
