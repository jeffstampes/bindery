package calibre

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func TestCalibreAudit_StoredSeriesMembershipAndPosition(t *testing.T) {
	f := newAuditTestFixture(t)
	ctx := context.Background()
	seriesRepo := db.NewSeriesRepo(f.database)
	series := &models.Series{ForeignID: "ol-series:fixture", Title: "Example Saga"}
	if err := seriesRepo.CreateOrGet(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertBookLink(ctx, series.ID, f.book.ID, "2", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	position := auditFindingFor(t, f.findings, models.CalibreAuditFieldPosition, series.ForeignID)
	if position.State != models.CalibreAuditUnresolved || position.BinderyEvidence[0].Source != "series_books.position_in_series" ||
		position.BinderyEvidence[0].ForeignID != series.ForeignID {
		t.Fatalf("bulk-joined provider series evidence missing: %+v", position)
	}
	if err := seriesRepo.UpsertBookLink(ctx, series.ID, f.book.ID, "1.0", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Audit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := auditFindingFor(t, f.findings, models.CalibreAuditFieldPosition, series.ForeignID); got.State != models.CalibreAuditResolved {
		t.Fatalf("numeric-equivalent series position failed to resolve: %+v", got)
	}
	if _, err := f.database.ExecContext(ctx, `UPDATE series SET title = ? WHERE id = ?`, "Another Saga", series.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Audit(ctx); err != nil {
		t.Fatal(err)
	}
	membership := auditFindingFor(t, f.findings, models.CalibreAuditFieldSeries, "")
	if membership.State != models.CalibreAuditUnresolved || membership.BinderyEvidence[0].Value != "Another Saga" {
		t.Fatalf("current series metadata was not compared: %+v", membership)
	}
	if got := auditFindingFor(t, f.findings, models.CalibreAuditFieldPosition, series.ForeignID); got.State != models.CalibreAuditUnmatched {
		t.Fatalf("series position without a common membership is not comparable: %+v", got)
	}
}

func TestCalibreAudit_UsesCurrentMatchProvenance(t *testing.T) {
	f := newAuditTestFixture(t)
	ctx := context.Background()
	ref := &models.CalibreWorkCrossReference{BookID: f.book.ID, CalibreID: 1,
		MatchMethod: "fallback_title_author", Confidence: models.CalibreMatchConfidenceMedium,
		Status: models.CalibreMatchStatusMatched}
	if err := f.crossRef.UpsertCrossReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Audit(ctx); err != nil {
		t.Fatal(err)
	}
	finding := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, "")
	if finding.MatchMethod != "identifier:isbn" || finding.MatchConfidence != models.CalibreMatchConfidenceExact {
		t.Fatalf("audit should report the current match evidence: %+v", finding)
	}
	stored, err := f.crossRef.GetByBookID(ctx, f.book.ID)
	if err != nil || stored == nil || stored.MatchMethod != "fallback_title_author" {
		t.Fatalf("audit must not mutate ownership matching: %+v %v", stored, err)
	}
}
