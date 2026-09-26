package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func TestCalibreAuditTagSettingIsIndependentOptIn(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	setting, err := repo.Get(ctx, SettingCalibreAuditTagWriteEnabled)
	if err != nil || setting != nil {
		t.Fatalf("tag write must be unset: %+v %v", setting, err)
	}
	d, ok := LookupSettingDescriptor(SettingCalibreAuditTagWriteEnabled)
	if !ok || d.Type != SettingTypeBool || d.Default != "false" || d.State != SettingStateActive || !d.Writable {
		t.Fatalf("missing off-by-default descriptor: %+v %v", d, ok)
	}
	put := func(value string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := withKey(httptest.NewRequest(http.MethodPut,
			"/api/v1/setting/"+SettingCalibreAuditTagWriteEnabled,
			strings.NewReader(`{"value":"`+value+`"}`)), SettingCalibreAuditTagWriteEnabled)
		h.Set(rec, req)
		return rec
	}
	if rec := put("true"); rec.Code != http.StatusBadRequest {
		t.Fatalf("enabled without authoritative mode: %d %s", rec.Code, rec.Body.String())
	}
	if rec := put("TRUE"); rec.Code != http.StatusBadRequest {
		t.Fatalf("case-folded opt-in bypassed dependency: %d %s", rec.Code, rec.Body.String())
	}
	if err := repo.Set(ctx, SettingCalibreLibraryPath, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingCalibreAuthoritativeLibraryEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	if rec := put("yes"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad tag value: %d %s", rec.Code, rec.Body.String())
	}
	if s, err := repo.Get(ctx, SettingCalibreAuditTagWriteEnabled); err != nil || s != nil {
		t.Fatalf("authoritative mode silently enabled tag writes: %+v %v", s, err)
	}
	if rec := put("true"); rec.Code != http.StatusOK {
		t.Fatalf("independent opt-in failed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := put("false"); rec.Code != http.StatusOK {
		t.Fatalf("disabling tag writes failed: %d %s", rec.Code, rec.Body.String())
	}
}

type auditIgnoreTagSpy struct {
	repo    *db.CalibreAuditRepo
	id      int64
	called  int
	ignored bool
}

func (s *auditIgnoreTagSpy) IsEnabled(context.Context) bool                      { return true }
func (s *auditIgnoreTagSpy) Audit(context.Context) (*calibre.AuditResult, error) { return nil, nil }
func (s *auditIgnoreTagSpy) ReconcileAuditTags(ctx context.Context) error {
	s.called++
	findings, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	for _, f := range findings {
		if f.ID == s.id && f.State == models.CalibreAuditIgnored {
			s.ignored = true
		}
	}
	return context.Canceled // external write failure must not undo Ignore
}

func TestCalibreAuditIgnoreTagFailurePreservesDecision(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	author := &models.Author{Name: "Provider"}
	if err := db.NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "Title", ForeignID: "OL10W"}
	if err := db.NewBookRepo(database).Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	repo := db.NewCalibreAuditRepo(database)
	if _, err := repo.Apply(ctx, []models.CalibreAuditFinding{{BookID: book.ID, CalibreID: 1,
		Field: models.CalibreAuditFieldTitle, FindingType: models.CalibreAuditTitleDifference,
		Assessment: models.CalibreAuditNeedsReview, ComparisonFingerprint: "seen", State: models.CalibreAuditUnresolved}}); err != nil {
		t.Fatal(err)
	}
	findings, err := repo.List(ctx)
	if err != nil || len(findings) != 1 {
		t.Fatalf("findings: %v %v", findings, err)
	}
	spy := &auditIgnoreTagSpy{repo: repo, id: findings[0].ID}
	h := NewCalibreAuditHandler(spy, repo)
	request := func(fingerprint string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		r := chi.NewRouter()
		r.Post("/calibre/audit/{id}/ignore", h.Ignore)
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
			"/calibre/audit/"+strconv.FormatInt(spy.id, 10)+"/ignore",
			strings.NewReader(`{"comparisonFingerprint":"`+fingerprint+`"}`)))
		return rec
	}
	if rec := request("stale"); rec.Code != http.StatusConflict || spy.called != 0 {
		t.Fatalf("stale decision triggered write: %d calls=%d", rec.Code, spy.called)
	}
	if rec := request("seen"); rec.Code != http.StatusNoContent || spy.called != 1 || !spy.ignored {
		t.Fatalf("failed tag write affected review: %d calls=%d committed=%t", rec.Code, spy.called, spy.ignored)
	}
}
