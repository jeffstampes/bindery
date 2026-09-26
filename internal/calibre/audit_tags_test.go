package calibre

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func setupAuditTags(t *testing.T) (auditTestFixture, *sql.DB) {
	t.Helper()
	f := newAuditTestFixture(t)
	conn, err := sql.Open("sqlite", f.root+"/metadata.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, query := range []string{
		`PRAGMA application_id = 0x63616c69`,
		`CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE, link TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE books_tags_link (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, tag INTEGER NOT NULL, UNIQUE(book, tag))`,
		`INSERT INTO tags (id, name) VALUES (1, 'Favorites'), (2, 'To Read')`,
		`INSERT INTO books_tags_link (book, tag) VALUES (1, 1), (1, 2)`,
	} {
		if _, err := conn.ExecContext(context.Background(), query); err != nil {
			t.Fatal(err)
		}
	}
	return f, conn
}

func auditTagsFor(t *testing.T, conn *sql.DB) []string {
	t.Helper()
	rows, err := conn.QueryContext(context.Background(), `SELECT t.name FROM tags t JOIN books_tags_link l ON l.tag = t.id WHERE l.book = 1 ORDER BY t.name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

func requireAuditTags(t *testing.T, conn *sql.DB, withMismatch bool) {
	t.Helper()
	want := []string{"Favorites", "To Read"}
	if withMismatch {
		want = []string{db.CalibreMismatchTag, "Favorites", "To Read"}
	}
	got := auditTagsFor(t, conn)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Calibre tags = %v, want %v", got, want)
	}
}

func ignoreActionableAuditFindings(t *testing.T, f auditTestFixture, exceptField string) {
	t.Helper()
	findings, err := f.findings.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.State != models.CalibreAuditUnresolved || finding.Assessment != models.CalibreAuditNeedsReview || finding.Field == exceptField {
			continue
		}
		if ok, err := f.findings.Ignore(context.Background(), finding.ID, finding.ComparisonFingerprint); err != nil || !ok {
			t.Fatalf("ignore %d: %t %v", finding.ID, ok, err)
		}
	}
}

func TestAuditTags_OptInAndReviewLifecycle(t *testing.T) {
	f, conn := setupAuditTags(t)
	ctx := context.Background()
	result, err := f.svc.Reconcile(ctx)
	if err != nil || result.Audit == nil || result.Audit.TagUpdated != 0 {
		t.Fatalf("default-off reconciliation: %+v %v", result, err)
	}
	requireAuditTags(t, conn, false)
	if err := f.settings.Set(ctx, auditTagSetting, "true"); err != nil {
		t.Fatal(err)
	}
	resultAudit, err := f.svc.Audit(ctx)
	if err != nil || resultAudit.TagError != "" || resultAudit.TagUpdated != 1 || resultAudit.Updated != 0 {
		t.Fatalf("multiple findings should add once: %+v %v", resultAudit, err)
	}
	requireAuditTags(t, conn, true)
	if repeat, err := f.svc.Audit(ctx); err != nil || repeat.TagUpdated != 0 {
		t.Fatalf("idempotent audit: %+v %v", repeat, err)
	}
	ignoreActionableAuditFindings(t, f, models.CalibreAuditFieldTitle)
	if err := f.svc.ReconcileAuditTags(ctx); err != nil {
		t.Fatal(err)
	}
	requireAuditTags(t, conn, true) // remaining actionable title finding
	ignoreActionableAuditFindings(t, f, "")
	if err := f.svc.ReconcileAuditTags(ctx); err != nil {
		t.Fatal(err)
	}
	requireAuditTags(t, conn, false) // ambiguous findings do not keep the tag
	if err := f.settings.Set(ctx, auditTagSetting, "false"); err != nil {
		t.Fatal(err)
	}
	// While off, even a stray BinderyMismatch tag is left completely alone.
	if _, err := db.NewCalibreTagWriter(f.root).ReconcileMismatch(ctx, map[int64]bool{1: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Audit(ctx); err != nil {
		t.Fatal(err)
	}
	requireAuditTags(t, conn, true)
	if err := f.settings.Set(ctx, auditTagSetting, "true"); err != nil {
		t.Fatal(err)
	}
	if err := f.settings.Set(ctx, "calibre.authoritative_library_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReconcileAuditTags(ctx); err != nil {
		t.Fatal(err)
	}
	requireAuditTags(t, conn, true) // disabling the core mode also blocks writes
}

func TestAuditTags_ResolvedLastFindingRemovesTag(t *testing.T) {
	f, conn := setupAuditTags(t)
	ctx := context.Background()
	if err := f.settings.Set(ctx, auditTagSetting, "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	ignoreActionableAuditFindings(t, f, models.CalibreAuditFieldTitle)
	if err := f.svc.ReconcileAuditTags(ctx); err != nil {
		t.Fatal(err)
	}
	requireAuditTags(t, conn, true)
	// A provider correction makes the final actionable comparison agree.
	if _, err := f.database.ExecContext(ctx, `UPDATE editions SET title = 'Book One' WHERE id = ?`, f.edition.ID); err != nil {
		t.Fatal(err)
	}
	result, err := f.svc.Audit(ctx)
	if err != nil || result.TagError != "" || result.TagUpdated != 1 {
		t.Fatalf("resolve last finding: %+v %v", result, err)
	}
	requireAuditTags(t, conn, false)
}

func TestAuditTags_UnmatchedAndEmptyCatalogueRemoveTag(t *testing.T) {
	f, conn := setupAuditTags(t)
	ctx := context.Background()
	if err := f.settings.Set(ctx, auditTagSetting, "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	requireAuditTags(t, conn, true)
	ref, err := f.crossRef.GetByBookID(ctx, f.book.ID)
	if err != nil || ref == nil {
		t.Fatalf("cross-reference: %+v %v", ref, err)
	}
	ref.Status = models.CalibreMatchStatusStale
	if err := f.crossRef.UpsertCrossReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if result, err := f.svc.Audit(ctx); err != nil || result.TagUpdated != 1 {
		t.Fatalf("unmatched recheck: %+v %v", result, err)
	}
	requireAuditTags(t, conn, false)
	if finding := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, ""); finding.State != models.CalibreAuditUnmatched {
		t.Fatalf("unmatched finding was misrepresented as clean: %+v", finding)
	}
	// A previously tagged, now-deleted Bindery work leaves no findings.
	// Scheduled Reconcile must still run the tag projection with zero works.
	if _, err := db.NewCalibreTagWriter(f.root).ReconcileMismatch(ctx, map[int64]bool{1: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.ExecContext(ctx, `DELETE FROM books WHERE id = ?`, f.book.ID); err != nil {
		t.Fatal(err)
	}
	if result, err := f.svc.Reconcile(ctx); err != nil || result.Audit == nil || result.Audit.TagUpdated != 1 {
		t.Fatalf("empty catalogue failed to remove tag: %+v %v", result, err)
	}
	requireAuditTags(t, conn, false)
}

func TestAuditTags_ConcurrentPassesWriteOnce(t *testing.T) {
	f, conn := setupAuditTags(t)
	ctx := context.Background()
	if _, err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.settings.Set(ctx, auditTagSetting, "true"); err != nil {
		t.Fatal(err)
	}
	const passes = 5
	start := make(chan struct{})
	results := make(chan struct {
		result *AuditResult
		err    error
	}, passes)
	for range passes {
		go func() {
			<-start
			result, err := f.svc.Audit(ctx)
			results <- struct {
				result *AuditResult
				err    error
			}{result, err}
		}()
	}
	close(start)
	changed := 0
	for range passes {
		r := <-results
		if r.err != nil || r.result == nil || r.result.TagError != "" {
			t.Fatalf("concurrent audit: %+v %v", r.result, r.err)
		}
		changed += r.result.TagUpdated
	}
	if changed != 1 {
		t.Fatalf("concurrent passes changed tag %d times", changed)
	}
	requireAuditTags(t, conn, true)
}

func TestAuditTags_FailedAddAndRemoveRetryWithoutRollingBackFindings(t *testing.T) {
	f, conn := setupAuditTags(t)
	ctx := context.Background()
	if err := f.settings.Set(ctx, auditTagSetting, "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `CREATE TRIGGER fail_add BEFORE INSERT ON books_tags_link BEGIN SELECT RAISE(ABORT, 'read only test'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := f.svc.Reconcile(ctx)
	if err != nil || result.Audit == nil || result.Audit.TagError == "" {
		t.Fatalf("failed add should not fail audit: %+v %v", result, err)
	}
	if finding := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, ""); finding.State != models.CalibreAuditUnresolved {
		t.Fatalf("failed add corrupted finding: %+v", finding)
	}
	requireAuditTags(t, conn, false)
	var orphan int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM tags WHERE name = ?`, db.CalibreMismatchTag).Scan(&orphan); err != nil || orphan != 0 {
		t.Fatalf("failed add left an unused tag row: %d %v", orphan, err)
	}
	if _, err := conn.ExecContext(ctx, `DROP TRIGGER fail_add`); err != nil {
		t.Fatal(err)
	}
	if retry, err := f.svc.Audit(ctx); err != nil || retry.Updated != 0 || retry.TagUpdated != 1 || retry.TagError != "" {
		t.Fatalf("retry after add failed: %+v %v", retry, err)
	}
	requireAuditTags(t, conn, true)
	if _, err := conn.ExecContext(ctx, `CREATE TRIGGER fail_remove BEFORE DELETE ON books_tags_link BEGIN SELECT RAISE(ABORT, 'read only test'); END`); err != nil {
		t.Fatal(err)
	}
	ignoreActionableAuditFindings(t, f, "")
	if err := f.svc.ReconcileAuditTags(ctx); err == nil {
		t.Fatal("failed removal not surfaced")
	}
	requireAuditTags(t, conn, true)
	if finding := auditFindingFor(t, f.findings, models.CalibreAuditFieldTitle, ""); finding.State != models.CalibreAuditIgnored {
		t.Fatalf("failed removal rolled back ignore: %+v", finding)
	}
	if _, err := conn.ExecContext(ctx, `DROP TRIGGER fail_remove`); err != nil {
		t.Fatal(err)
	}
	if retry, err := f.svc.Audit(ctx); err != nil || retry.TagUpdated != 1 || retry.TagError != "" {
		t.Fatalf("retry after remove failed: %+v %v", retry, err)
	}
	requireAuditTags(t, conn, false)
}
