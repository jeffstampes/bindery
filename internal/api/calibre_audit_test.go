package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/models"
)

func TestCalibreAuditReviewRoutes(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	settings := db.NewSettingsRepo(database)
	repo := db.NewCalibreAuditRepo(database)
	service := calibre.NewAuthoritativeService(settings, nil, nil)
	h := NewCalibreAuditHandler(service, repo)
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/calibre/audit", h.List)
		r.Post("/calibre/audit/recheck", h.Recheck)
		r.Get("/calibre/audit/recheck/status", h.RecheckStatus)
		r.Post("/calibre/audit/{id}/ignore", h.Ignore)
	})
	request := func(role, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req = req.WithContext(auth.WithUserRole(req.Context(), role))
		r.ServeHTTP(rec, req)
		return rec
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/calibre/audit"},
		{http.MethodPost, "/calibre/audit/recheck"},
		{http.MethodGet, "/calibre/audit/recheck/status"},
		{http.MethodPost, "/calibre/audit/1/ignore"},
	} {
		if rec := request("user", route.method, route.path, ""); rec.Code != http.StatusForbidden {
			t.Fatalf("non-admin %s %s: %d", route.method, route.path, rec.Code)
		}
		if rec := request("admin", route.method, route.path, ""); rec.Code != http.StatusNotFound {
			t.Fatalf("disabled %s %s: %d", route.method, route.path, rec.Code)
		}
	}
	if err := settings.Set(ctx, "calibre.authoritative_library_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, "calibre.library_path", "/not-a-real-library"); err != nil {
		t.Fatal(err)
	}

	author := &models.Author{Name: "Provider Author"}
	if err := db.NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "Provider Title", ForeignID: "OL1W"}
	if err := db.NewBookRepo(database).Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	finding := models.CalibreAuditFinding{
		BookID: book.ID, CalibreID: 17, Field: models.CalibreAuditFieldTitle,
		FindingType: models.CalibreAuditTitleDifference, Assessment: models.CalibreAuditAmbiguous,
		CalibreEvidence: []models.CalibreAuditEvidence{{Value: "Owned title", Source: "calibre.books.title"}},
		BinderyEvidence: []models.CalibreAuditEvidence{{Value: "Provider Title", Source: "books.title", Provider: "openlibrary", ForeignID: "OL1W"}},
		Reason:          "Different editions may have different titles.", ComparisonFingerprint: "comparison-1", State: models.CalibreAuditUnresolved,
	}
	if _, err := repo.Apply(ctx, []models.CalibreAuditFinding{finding}); err != nil {
		t.Fatal(err)
	}
	if rec := request("admin", http.MethodGet, "/calibre/audit?findingType=invalid", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad filter: %d", rec.Code)
	}
	rec := request("admin", http.MethodGet, "/calibre/audit?state=unresolved&assessment=ambiguous&limit=1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Items        []models.CalibreAuditFinding `json:"items"`
		Total, Limit int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Limit != 1 || len(page.Items) != 1 || page.Items[0].BookTitle != "Provider Title" ||
		page.Items[0].BinderyEvidence[0].ForeignID != "OL1W" || page.Items[0].CalibreEvidence[0].Value != "Owned title" {
		t.Fatalf("missing review context: %+v", page)
	}
	id := page.Items[0].ID
	ignorePath := "/calibre/audit/" + strconv.FormatInt(id, 10) + "/ignore"
	if rec := request("admin", http.MethodPost, ignorePath, `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing fingerprint: %d", rec.Code)
	}
	if rec := request("admin", http.MethodPost, ignorePath, `{"comparisonFingerprint":"stale"}`); rec.Code != http.StatusConflict {
		t.Fatalf("stale fingerprint: %d", rec.Code)
	}
	if rec := request("admin", http.MethodPost, ignorePath, `{"comparisonFingerprint":"comparison-1"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("ignore: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("admin", http.MethodPost, ignorePath, `{"comparisonFingerprint":"comparison-1"}`); rec.Code != http.StatusConflict {
		t.Fatalf("repeat ignore: %d", rec.Code)
	}
	if rec := request("admin", http.MethodGet, "/calibre/audit?state=ignored", ""); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"state":"ignored"`)) {
		t.Fatalf("ignored state: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("admin", http.MethodGet, "/calibre/audit/recheck/status", ""); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"running":false`)) {
		t.Fatalf("idle recheck status: %d %s", rec.Code, rec.Body.String())
	}
}

type controlledAudit struct {
	started chan context.Context
	release chan struct{}
	calls   atomic.Int32
	err     error
}

func (s *controlledAudit) IsEnabled(context.Context) bool { return true }
func (s *controlledAudit) Audit(ctx context.Context) (*calibre.AuditResult, error) {
	s.calls.Add(1)
	s.started <- ctx
	<-s.release
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.err != nil {
		return nil, s.err
	}
	return &calibre.AuditResult{ComparedBooks: 42, Findings: 2}, nil
}

func TestCalibreAuditRecheckLifecycle(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{"completed", nil},
		{"failed", errors.New("reader unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			group := jobs.NewGroup(context.Background())
			defer group.Shutdown(time.Second)
			service := &controlledAudit{started: make(chan context.Context, 1), release: make(chan struct{}), err: test.err}
			h := NewCalibreAuditHandler(service, &db.CalibreAuditRepo{}).WithJobs(group)
			request := func(method string) *httptest.ResponseRecorder {
				t.Helper()
				rec := httptest.NewRecorder()
				path := "/calibre/audit/recheck"
				if method == http.MethodGet {
					path += "/status"
				}
				req := httptest.NewRequest(method, path, nil)
				if method == http.MethodGet {
					h.RecheckStatus(rec, req)
				} else {
					h.Recheck(rec, req)
				}
				return rec
			}
			startCtx, disconnect := context.WithCancel(context.Background())
			startReq := httptest.NewRequest(http.MethodPost, "/calibre/audit/recheck", nil).WithContext(startCtx)
			start := httptest.NewRecorder()
			h.Recheck(start, startReq)
			if start.Code != http.StatusAccepted || !bytes.Contains(start.Body.Bytes(), []byte(`"running":true`)) {
				disconnect()
				t.Fatalf("start: %d %s", start.Code, start.Body.String())
			}
			// Client disconnects after acceptance, before the worker finishes.
			disconnect()
			jobCtx := <-service.started
			if err := jobCtx.Err(); err != nil {
				t.Fatalf("accepted audit inherited cancelled request: %v", err)
			}
			if rec := request(http.MethodPost); rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte(`"running":true`)) {
				t.Fatalf("duplicate: %d %s", rec.Code, rec.Body.String())
			}
			if rec := request(http.MethodGet); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"running":true`)) {
				t.Fatalf("running status: %d %s", rec.Code, rec.Body.String())
			}
			if calls := service.calls.Load(); calls != 1 {
				t.Fatalf("duplicate started %d audits", calls)
			}
			close(service.release)
			deadline := time.Now().Add(time.Second)
			for {
				rec := request(http.MethodGet)
				var status CalibreAuditRecheckStatus
				if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
					t.Fatal(err)
				}
				if !status.Running {
					if status.StartedAt == nil || status.FinishedAt == nil {
						t.Fatalf("missing run times: %+v", status)
					}
					if test.err != nil {
						if status.Error != test.err.Error() || status.Result != nil {
							t.Fatalf("error presented as success: %+v", status)
						}
					} else if status.Error != "" || status.Result == nil || status.Result.ComparedBooks != 42 {
						t.Fatalf("missing successful result: %+v", status)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("audit did not finish")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestCalibreAuditRecheckRejectsShutdown(t *testing.T) {
	group := jobs.NewGroup(context.Background())
	group.Shutdown(time.Second)
	h := NewCalibreAuditHandler(&controlledAudit{}, &db.CalibreAuditRepo{}).WithJobs(group)
	rec := httptest.NewRecorder()
	h.Recheck(rec, httptest.NewRequest(http.MethodPost, "/calibre/audit/recheck", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("shutdown start: %d %s", rec.Code, rec.Body.String())
	}
	status := httptest.NewRecorder()
	h.RecheckStatus(status, httptest.NewRequest(http.MethodGet, "/calibre/audit/recheck/status", nil))
	if !bytes.Contains(status.Body.Bytes(), []byte(`"running":false`)) {
		t.Fatalf("dropped launch left running: %s", status.Body.String())
	}
}
