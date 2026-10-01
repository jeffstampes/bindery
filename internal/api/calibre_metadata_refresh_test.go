package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
)

type refreshRouteService struct {
	enabled        bool
	refreshEnabled bool
	fingerprint    string
	actor          int64
	fail           error
}

func (s *refreshRouteService) IsEnabled(context.Context) bool                      { return s.enabled }
func (s *refreshRouteService) Audit(context.Context) (*calibre.AuditResult, error) { return nil, nil }
func (s *refreshRouteService) PreviewMetadataRefresh(_ context.Context, bookID int64) (*calibre.MetadataRefreshProposal, error) {
	if !s.refreshEnabled {
		return nil, calibre.ErrMetadataRefreshDisabled
	}
	return &calibre.MetadataRefreshProposal{ID: 11, BookID: bookID, CalibreID: 42, Status: "ready", Fingerprint: "frozen", LookupISBN: "9780306406157"}, nil
}
func (s *refreshRouteService) ApplyMetadataRefresh(_ context.Context, id, actor int64, fingerprint string) (*db.CalibreMetadataRefreshAttempt, error) {
	s.fingerprint, s.actor = fingerprint, actor
	if !s.refreshEnabled {
		return nil, calibre.ErrMetadataRefreshDisabled
	}
	if s.fail != nil {
		return &db.CalibreMetadataRefreshAttempt{ID: 3, ProposalID: id, Outcome: "partial"}, s.fail
	}
	return &db.CalibreMetadataRefreshAttempt{ID: 3, ProposalID: id, Outcome: "applied"}, nil
}
func (s *refreshRouteService) MetadataRefreshAttempts(context.Context, int64) ([]db.CalibreMetadataRefreshAttempt, error) {
	if !s.refreshEnabled {
		return nil, calibre.ErrMetadataRefreshDisabled
	}
	return []db.CalibreMetadataRefreshAttempt{{ID: 3, Outcome: "partial"}}, nil
}

func TestMetadataRefreshRoutesGatedAndFrozenApproval(t *testing.T) {
	s := &refreshRouteService{enabled: true}
	h := NewCalibreAuditHandler(s, &db.CalibreAuditRepo{})
	router := chi.NewRouter()
	router.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Post("/calibre/metadata-refresh/{bookID}/preview", h.RefreshPreview)
		r.Post("/calibre/metadata-refresh/proposals/{proposalID}/apply", h.RefreshApply)
		r.Get("/calibre/metadata-refresh/{bookID}/attempts", h.RefreshAttempts)
	})
	req := func(role, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r = r.WithContext(auth.WithUserRole(auth.WithUserID(r.Context(), 7), role))
		router.ServeHTTP(w, r)
		return w
	}
	preview := "/calibre/metadata-refresh/5/preview"
	apply := "/calibre/metadata-refresh/proposals/11/apply"
	if got := req("user", http.MethodPost, preview, ""); got.Code != 403 {
		t.Fatalf("user preview %d", got.Code)
	}
	if got := req("user", http.MethodPost, apply, `{"fingerprint":"frozen"}`); got.Code != 403 {
		t.Fatalf("user apply %d", got.Code)
	}
	if got := req("admin", http.MethodPost, preview, ""); got.Code != 403 {
		t.Fatalf("separate opt-in bypassed: %d", got.Code)
	}
	s.refreshEnabled = true
	if got := req("admin", http.MethodPost, preview, ""); got.Code != 200 || !bytes.Contains(got.Body.Bytes(), []byte(`"lookupIsbn":"9780306406157"`)) {
		t.Fatalf("preview %d %s", got.Code, got.Body.String())
	}
	if got := req("admin", http.MethodPost, apply, `{"fingerprint":"frozen","field":"identifiers"}`); got.Code != 400 {
		t.Fatalf("client-selected field accepted: %d", got.Code)
	}
	if got := req("admin", http.MethodPost, apply, `{"fingerprint":"frozen"}`); got.Code != 200 || s.actor != 7 || s.fingerprint != "frozen" {
		t.Fatalf("approval %d %s", got.Code, got.Body.String())
	}
	s.fail = calibre.ErrMetadataRefreshStale
	if got := req("admin", http.MethodPost, apply, `{"fingerprint":"frozen"}`); got.Code != 409 {
		t.Fatalf("stale %d", got.Code)
	}
	s.fail = errors.New("partial command failure")
	if got := req("admin", http.MethodPost, apply, `{"fingerprint":"frozen"}`); got.Code != 503 || !bytes.Contains(got.Body.Bytes(), []byte(`"outcome":"partial"`)) {
		t.Fatalf("partial outcome %d %s", got.Code, got.Body.String())
	}
	if got := req("admin", http.MethodGet, "/calibre/metadata-refresh/5/attempts", ""); got.Code != 200 || !bytes.Contains(got.Body.Bytes(), []byte(`"partial"`)) {
		t.Fatalf("attempts %d %s", got.Code, got.Body.String())
	}
	s.enabled = false
	if got := req("admin", http.MethodPost, apply, `{"fingerprint":"frozen"}`); got.Code != 404 {
		t.Fatalf("mode off %d", got.Code)
	}
}
