package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func (s *artifactAPISpy) ScanArtifactsAttested(ctx context.Context, id int64, original bool) (*models.CalibreIdentitySnapshot, error) {
	if !original {
		return nil, calibre.ErrArtifactNotReady
	}
	result, err := s.ScanArtifacts(ctx, id)
	if result != nil {
		result.Artifacts[0].AttestedOriginal = true
	}
	return result, err
}

func (s *artifactAPISpy) RecordArtifactWriteback(_ context.Context, id int64, at time.Time, source string) (*models.CalibreIdentitySnapshot, error) {
	s.calls++
	if id == 6 {
		return nil, calibre.ErrArtifactNotReady
	}
	if id != 7 || at.IsZero() || source != "polish_books" {
		return nil, nil
	}
	return &models.CalibreIdentitySnapshot{BookID: 7, CalibreID: 9,
		Writebacks: []models.CalibreArtifactWriteback{{BookID: 7, CalibreID: 9, WrittenAt: at, Source: source}}}, nil
}

func TestCalibreArtifactWritebackAPI(t *testing.T) {
	spy := &artifactAPISpy{}
	h := NewCalibreAuditHandler(spy, &db.CalibreAuditRepo{})
	router := chi.NewRouter()
	router.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Post("/calibre/identity/{bookID}/writeback", h.ReportArtifactWriteback)
	})
	when := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	request := func(role, id, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/calibre/identity/"+id+"/writeback", strings.NewReader(body))
		router.ServeHTTP(rec, req.WithContext(auth.WithUserRole(req.Context(), role)))
		return rec
	}
	valid := `{"writtenAt":"` + when + `","source":"polish_books"}`
	if rec := request("user", "7", valid); rec.Code != http.StatusForbidden || spy.calls != 0 {
		t.Fatalf("non-admin report: %d calls=%d", rec.Code, spy.calls)
	}
	if rec := request("admin", "7", valid); rec.Code != http.StatusNotFound || spy.calls != 0 {
		t.Fatalf("disabled report: %d calls=%d", rec.Code, spy.calls)
	}
	spy.enabled = true
	for _, bad := range []string{`{}`, `{"writtenAt":"broken","source":"polish_books"}`,
		`{"writtenAt":"` + time.Now().Add(time.Hour).Format(time.RFC3339Nano) + `","source":"polish_books"}`,
		`{"writtenAt":"` + when + `","source":"other"}`, valid + valid} {
		if rec := request("admin", "7", bad); rec.Code != http.StatusBadRequest || spy.calls != 0 {
			t.Fatalf("invalid event accepted: status=%d calls=%d body=%s", rec.Code, spy.calls, bad)
		}
	}
	if rec := request("admin", "6", valid); rec.Code != http.StatusConflict {
		t.Fatalf("stale ownership: %d", rec.Code)
	}
	if rec := request("admin", "8", valid); rec.Code != http.StatusNotFound {
		t.Fatalf("missing link: %d", rec.Code)
	}
	if rec := request("admin", "7", valid); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"source":"polish_books"`) {
		t.Fatalf("reported event missing from readback: %d %s", rec.Code, rec.Body.String())
	}
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
	attested := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/calibre/identity/7/scan", strings.NewReader(`{"attestOriginal":true}`))
	router.ServeHTTP(attested, req.WithContext(auth.WithUserRole(req.Context(), "admin")))
	if attested.Code != http.StatusOK || !strings.Contains(attested.Body.String(), `"attestedOriginal":true`) {
		t.Fatalf("explicit scan attestation missing: %d %s", attested.Code, attested.Body.String())
	}
}
