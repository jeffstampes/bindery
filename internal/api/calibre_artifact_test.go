package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type artifactAPISpy struct {
	enabled bool
	calls   int
}

func (s *artifactAPISpy) IsEnabled(context.Context) bool                      { return s.enabled }
func (s *artifactAPISpy) Audit(context.Context) (*calibre.AuditResult, error) { return nil, nil }
func (s *artifactAPISpy) ScanArtifacts(_ context.Context, id int64) (*models.CalibreIdentitySnapshot, error) {
	s.calls++
	if id == 6 {
		return nil, calibre.ErrArtifactNotReady
	}
	if id != 7 {
		return nil, nil
	}
	return &models.CalibreIdentitySnapshot{BookID: 7, CalibreID: 9, RootKey: "openlibrary:OL7W",
		Artifacts: []models.CalibreArtifactScan{{BookID: 7, CalibreID: 9, Format: "EPUB", FileName: "book", FilePath: "Book (9)/book.epub",
			Method: "bindery_epub_v1", Outcome: "scanned", CorrelationGroup: "sha256:test", Identifiers: []models.CalibreArtifactIdentifier{{ObservedValue: "9780306406157", Source: "epub_content", Status: "matches_work", EditionConfidence: "candidate"}}}},
	}, nil
}

func TestCalibreArtifactScanAPIAdminAndProvenance(t *testing.T) {
	spy := &artifactAPISpy{}
	h := NewCalibreAuditHandler(spy, &db.CalibreAuditRepo{})
	router := chi.NewRouter()
	router.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Post("/calibre/identity/{bookID}/scan", h.ScanArtifacts)
	})
	request := func(role, path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		router.ServeHTTP(rec, req.WithContext(auth.WithUserRole(req.Context(), role)))
		return rec
	}
	if rec := request("user", "/calibre/identity/7/scan"); rec.Code != http.StatusForbidden || spy.calls != 0 {
		t.Fatalf("non-admin scan: %d calls %d", rec.Code, spy.calls)
	}
	if rec := request("admin", "/calibre/identity/7/scan"); rec.Code != http.StatusNotFound || spy.calls != 0 {
		t.Fatalf("disabled scan: %d calls %d", rec.Code, spy.calls)
	}
	spy.enabled = true
	if rec := request("admin", "/calibre/identity/no/scan"); rec.Code != http.StatusBadRequest || spy.calls != 0 {
		t.Fatalf("invalid id: %d calls %d", rec.Code, spy.calls)
	}
	if rec := request("admin", "/calibre/identity/8/scan"); rec.Code != http.StatusNotFound {
		t.Fatalf("no active match: %d", rec.Code)
	}
	if rec := request("admin", "/calibre/identity/6/scan"); rec.Code != http.StatusConflict {
		t.Fatalf("unresolved canonical root: %d", rec.Code)
	}
	rec := request("admin", "/calibre/identity/7/scan")
	if rec.Code != http.StatusOK || spy.calls != 3 || !strings.Contains(rec.Body.String(), `"filePath":"Book (9)/book.epub"`) ||
		!strings.Contains(rec.Body.String(), `"status":"matches_work"`) || !strings.Contains(rec.Body.String(), `"editionConfidence":"candidate"`) {
		t.Fatalf("artifact API lost provenance: %d calls %d body %s", rec.Code, spy.calls, rec.Body.String())
	}
}
