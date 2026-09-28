package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
)

type coverAuditSpy struct {
	enabled bool
	path    string
	calls   int
}

func (s *coverAuditSpy) IsEnabled(context.Context) bool { return s.enabled }
func (s *coverAuditSpy) Audit(context.Context) (*calibre.AuditResult, error) {
	return nil, nil
}
func (s *coverAuditSpy) OpenOwnedCover(_ context.Context, id int64) (*os.File, error) {
	s.calls++
	if id != 7 || s.path == "" {
		return nil, nil
	}
	return os.Open(s.path)
}

func TestCalibreOwnedCoverRoute(t *testing.T) {
	root := t.TempDir()
	cover := filepath.Join(root, "cover.jpg")
	// The handler serves only JPEG bytes even if the source file ends in .jpg.
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0, 1}
	if err := os.WriteFile(cover, jpeg, 0o600); err != nil {
		t.Fatal(err)
	}
	spy := &coverAuditSpy{enabled: true, path: cover}
	h := NewCalibreAuditHandler(spy, &db.CalibreAuditRepo{})
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/calibre/identity/{bookID}/cover", h.OwnedCover)
	})
	request := func(role, path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(rec, req.WithContext(auth.WithUserRole(req.Context(), role)))
		return rec
	}
	if rec := request("user", "/calibre/identity/7/cover"); rec.Code != http.StatusForbidden || spy.calls != 0 {
		t.Fatalf("non-admin cover access: %d, %d opens", rec.Code, spy.calls)
	}
	spy.enabled = false
	if rec := request("admin", "/calibre/identity/7/cover"); rec.Code != http.StatusNotFound || spy.calls != 0 {
		t.Fatalf("disabled cover access: %d, %d opens", rec.Code, spy.calls)
	}
	spy.enabled = true
	if rec := request("admin", "/calibre/identity/invalid/cover"); rec.Code != http.StatusBadRequest || spy.calls != 0 {
		t.Fatalf("bad book ID: %d, %d opens", rec.Code, spy.calls)
	}
	if rec := request("admin", "/calibre/identity/8/cover"); rec.Code != http.StatusNotFound {
		t.Fatalf("unmatched cover: %d", rec.Code)
	}
	if rec := request("admin", "/calibre/identity/7/cover"); rec.Code != http.StatusOK ||
		rec.Header().Get("Content-Type") != "image/jpeg" || rec.Header().Get("Cache-Control") != "private, max-age=60" ||
		!bytes.Equal(rec.Body.Bytes(), jpeg) {
		t.Fatalf("owned jpeg: %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if err := os.WriteFile(cover, []byte("<script>alert(1)</script>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := request("admin", "/calibre/identity/7/cover"); rec.Code != http.StatusNotFound || bytes.Contains(rec.Body.Bytes(), []byte("script")) {
		t.Fatalf("non-JPEG served: %d %s", rec.Code, rec.Body.String())
	}
}
