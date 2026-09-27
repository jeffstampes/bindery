package calibre

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

func TestManualReconciliationReportsPartialAndPreservesReviewWithoutCalibreWrites(t *testing.T) {
	f, repo, stub := identityFixture(t)
	f.svc.WithArtifactEvidence(db.NewCalibreArtifactRepo(f.database))
	if err := f.settings.Set(context.Background(), "calibre.audit_tag_write_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	before := calibreDBContents(t, f.root)
	var stages []string
	run := func() *ReconcileResult {
		t.Helper()
		stages = nil
		result, err := f.svc.ReconcileReadOnlyWithProgress(context.Background(), func(stage string) { stages = append(stages, stage) })
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, calibreDBContents(t, f.root)) {
			t.Fatal("manual reconciliation wrote to Calibre despite optional tag write opt-in")
		}
		if len(stages) != 3 || stages[0] != "ownership" || stages[1] != "identity" || stages[2] != "audit" {
			t.Fatalf("stage sequence = %v", stages)
		}
		return result
	}
	first := run()
	if first.Matched != 1 || first.TotalBinderyBooks != 1 || first.Audit == nil || first.Audit.Findings == 0 ||
		first.Identity == nil || first.Identity.RefreshedWorks != 1 || first.Identity.EvidenceRecords == 0 ||
		first.Identity.FailedLookups != 1 || first.Identity.DeferredWorks != 0 ||
		first.Transitions == nil || first.Transitions.NewUnresolved == 0 {
		t.Fatalf("initial run summary: %+v", first)
	}
	finding := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, "")
	if ok, err := f.findings.Ignore(context.Background(), finding.ID, finding.ComparisonFingerprint); err != nil || !ok {
		t.Fatalf("ignore: %v %v", ok, err)
	}
	linkBefore, err := f.crossRef.GetByBookID(context.Background(), f.book.ID)
	if err != nil || linkBefore == nil {
		t.Fatalf("load ownership link: %+v %v", linkBefore, err)
	}
	second := run()
	linkAfter, err := f.crossRef.GetByBookID(context.Background(), f.book.ID)
	if err != nil || linkAfter == nil || !linkAfter.UpdatedAt.Equal(linkBefore.UpdatedAt) {
		t.Fatalf("unchanged ownership link was rewritten: before=%+v after=%+v err=%v", linkBefore, linkAfter, err)
	}
	if second.Revalidated != 1 || second.Matched != 0 || second.Audit.Updated != 0 ||
		second.Identity.RefreshedWorks != 0 || second.Identity.EvidenceRecords != 0 ||
		second.Identity.FailedLookups != 1 || second.Transitions.NewUnresolved != 0 ||
		second.Transitions.Resolved != 0 || second.Transitions.IgnoredPreserved == 0 {
		t.Fatalf("unchanged second run manufactured changes: %+v", second)
	}
	if got := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, ""); got.ID != finding.ID || got.State != models.CalibreAuditIgnored {
		t.Fatalf("ignored finding reopened or duplicated: %+v", got)
	}
	page, _, err := f.findings.ListPage(context.Background(), db.CalibreAuditListOpts{State: models.CalibreAuditIgnored, Limit: 50})
	if err != nil || len(page) == 0 || len(page[0].Decisions) != 1 {
		t.Fatalf("manual run manufactured review decisions: %+v %v", page, err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("unchanged snapshot re-fetched providers: %v", stub.calls)
	}
	artifacts, err := db.NewCalibreArtifactRepo(f.database).ListByBookID(context.Background(), f.book.ID, 1)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("full reconciliation scanned owned files: %+v %v", artifacts, err)
	}
	stored, err := repo.ListByBookID(context.Background(), f.book.ID)
	if err != nil || stored == nil || len(stored.Evidence) != first.Identity.EvidenceRecords {
		t.Fatalf("duplicate or missing persisted evidence: %+v %v", stored, err)
	}
}

func TestManualReconciliationReportsCachedArtifactsWithoutRescanning(t *testing.T) {
	f, artifacts, _ := artifactFixture(t, "ISBN: 978-0-306-40615-7")
	ctx := context.Background()
	if _, err := f.svc.ScanArtifacts(ctx, f.book.ID); err != nil {
		t.Fatal(err)
	}
	before, err := artifacts.ListByBookID(ctx, f.book.ID, 1)
	if err != nil || len(before) != 1 {
		t.Fatalf("explicit scan missing: %+v %v", before, err)
	}
	calibreBefore := calibreDBContents(t, f.root)
	result, err := f.svc.ReconcileReadOnlyWithProgress(ctx, nil)
	if err != nil || result.ArtifactScansCached != 1 {
		t.Fatalf("cached scan report: %+v %v", result, err)
	}
	after, err := artifacts.ListByBookID(ctx, f.book.ID, 1)
	if err != nil || len(after) != 1 || !before[0].ScannedAt.Equal(after[0].ScannedAt) || !bytes.Equal(calibreBefore, calibreDBContents(t, f.root)) {
		t.Fatalf("reconciliation rescanned or modified owned files: before=%+v after=%+v err=%v", before, after, err)
	}
	ref, err := f.crossRef.GetByBookID(ctx, f.book.ID)
	if err != nil || ref == nil {
		t.Fatalf("read ownership link: %+v %v", ref, err)
	}
	ref.Status = models.CalibreMatchStatusStale
	if err := f.crossRef.UpsertCrossReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if count, err := artifacts.CountCurrent(ctx); err != nil || count != 0 {
		t.Fatalf("stale link counted as current artifact evidence: %d %v", count, err)
	}
}

func TestManualReconciliationReportsFailedRootAsPartial(t *testing.T) {
	f, _, stub := identityFixture(t)
	stub.result.Observations = []metadata.RawBookObservation{{
		Provider: "openlibrary", Method: metadata.RawMethodExactBook, Seed: "OL100W", Outcome: metadata.RawOutcomeFailed,
		Err: errors.New("provider timeout"),
	}}
	result, err := f.svc.ReconcileReadOnlyWithProgress(context.Background(), nil)
	if err != nil || result.Identity == nil || result.Identity.UnresolvedRoots != 1 || result.Identity.FailedLookups != 1 || result.Identity.DeferredWorks != 0 {
		t.Fatalf("failed root looked like an empty successful discovery: %+v %v", result, err)
	}
}

func TestManualReconciliationRequiresAuthoritativeMode(t *testing.T) {
	f := newAuditTestFixture(t)
	if err := f.settings.Set(context.Background(), "calibre.authoritative_library_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if result, err := f.svc.ReconcileReadOnlyWithProgress(context.Background(), nil); result != nil || !errors.Is(err, ErrAuthoritativeDisabled) {
		t.Fatalf("disabled mode: %+v %v", result, err)
	}
}

func TestManualReconciliationResolvesAfterExternalCorrection(t *testing.T) {
	f := newAuditTestFixture(t)
	first, err := f.svc.ReconcileReadOnlyWithProgress(context.Background(), nil)
	if err != nil || first.Identity == nil || !first.Identity.DiscoveryUnavailable {
		t.Fatalf("unconfigured discovery reported as complete: %+v %v", first, err)
	}
	updateFixtureCalibreTitle(t, f.root, "Provider Edition Title")
	result, err := f.svc.ReconcileReadOnlyWithProgress(context.Background(), nil)
	if err != nil || result.Transitions == nil || result.Transitions.Resolved == 0 {
		t.Fatalf("resolved transition: %+v %v", result, err)
	}
	if got := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, ""); got.State != models.CalibreAuditResolved {
		t.Fatalf("finding not resolved: %+v", got)
	}
}
