package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
)

type controlledReconciliation struct {
	enabled bool
	started chan context.Context
	release chan struct{}
	result  *calibre.ReconcileResult
	err     error
}

func (s *controlledReconciliation) IsEnabled(context.Context) bool { return s.enabled }
func (s *controlledReconciliation) Audit(context.Context) (*calibre.AuditResult, error) {
	return &calibre.AuditResult{}, nil
}
func (s *controlledReconciliation) ReconcileReadOnlyWithProgress(ctx context.Context, stage func(string)) (*calibre.ReconcileResult, error) {
	stage("ownership")
	stage("identity")
	s.started <- ctx
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	stage("audit")
	return s.result, s.err
}

type reconciliationRunService func(context.Context, func(string)) (*calibre.ReconcileResult, error)

func (run reconciliationRunService) ReconcileReadOnlyWithProgress(ctx context.Context, stage func(string)) (*calibre.ReconcileResult, error) {
	return run(ctx, stage)
}

func TestCalibreReconciliationCompletesActualStages(t *testing.T) {
	for _, test := range []struct {
		name      string
		active    string
		callbacks []string
		want      []string
	}{
		{name: "active stage without a later callback", active: "prepare", want: []string{"prepare"}},
		{name: "non-audit final stage", active: "ownership", callbacks: []string{"ownership", "identity", "finalize"}, want: []string{"ownership", "identity", "finalize"}},
		{name: "repeated and revisited callbacks", active: "ownership", callbacks: []string{"ownership", "ownership", "identity", "identity", "ownership", "finalize", "finalize"}, want: []string{"ownership", "identity", "finalize"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &CalibreAuditHandler{reconcile: CalibreReconciliationStatus{State: "running", Stage: test.active, CompletedStages: []string{}}}
			service := reconciliationRunService(func(_ context.Context, stage func(string)) (*calibre.ReconcileResult, error) {
				for _, name := range test.callbacks {
					stage(name)
				}
				return &calibre.ReconcileResult{}, nil
			})
			h.runReconciliation(context.Background(), service)
			h.mu.Lock()
			status := h.reconciliationStatus()
			h.mu.Unlock()
			if status.State != "completed" || status.Stage != "" || status.FinishedAt == nil || !slices.Equal(status.CompletedStages, test.want) {
				t.Fatalf("stage bookkeeping: %+v, want %v", status, test.want)
			}
		})
	}
}

func TestCalibreReconciliationRoutesAndLifecycle(t *testing.T) {
	for _, test := range []struct {
		name   string
		result *calibre.ReconcileResult
		err    error
		state  string
	}{
		{"complete", &calibre.ReconcileResult{Matched: 2, Audit: &calibre.AuditResult{ComparedBooks: 2}, Identity: &calibre.IdentityRefreshResult{EvidenceRecords: 4}}, nil, "completed"},
		{"provider partial", &calibre.ReconcileResult{Matched: 2, Identity: &calibre.IdentityRefreshResult{FailedLookups: 1, UnconfiguredLookups: 1, DeferredWorks: 3}}, nil, "partial"},
		{"discovery unavailable", &calibre.ReconcileResult{Identity: &calibre.IdentityRefreshResult{DiscoveryUnavailable: true, UnresolvedRoots: 1}}, nil, "partial"},
		{"failure with partial counts", &calibre.ReconcileResult{Matched: 1}, errors.New("audit unavailable"), "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			group := jobs.NewGroup(context.Background())
			defer group.Shutdown(time.Second)
			service := &controlledReconciliation{started: make(chan context.Context, 1), release: make(chan struct{}), result: test.result, err: test.err}
			h := NewCalibreAuditHandler(service, &db.CalibreAuditRepo{}).WithJobs(group)
			router := chi.NewRouter()
			router.Group(func(r chi.Router) {
				r.Use(auth.RequireAdmin)
				r.Post("/calibre/reconciliation", h.Reconcile)
				r.Get("/calibre/reconciliation/status", h.ReconcileStatus)
			})
			request := func(role, method, path string) *httptest.ResponseRecorder {
				t.Helper()
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(method, path, nil)
				req = req.WithContext(auth.WithUserRole(req.Context(), role))
				router.ServeHTTP(rec, req)
				return rec
			}
			for _, path := range []struct{ method, url string }{{http.MethodPost, "/calibre/reconciliation"}, {http.MethodGet, "/calibre/reconciliation/status"}} {
				if rec := request("user", path.method, path.url); rec.Code != http.StatusForbidden {
					t.Fatalf("non-admin: %d", rec.Code)
				}
				if rec := request("admin", path.method, path.url); rec.Code != http.StatusNotFound {
					t.Fatalf("disabled mode: %d", rec.Code)
				}
			}
			service.enabled = true
			if rec := request("admin", http.MethodGet, "/calibre/reconciliation/status"); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"state":"idle"`)) {
				t.Fatalf("idle: %d %s", rec.Code, rec.Body.String())
			}
			startCtx, disconnect := context.WithCancel(context.Background())
			start := httptest.NewRecorder()
			h.Reconcile(start, httptest.NewRequest(http.MethodPost, "/calibre/reconciliation", nil).WithContext(startCtx))
			disconnect()
			if start.Code != http.StatusAccepted || !bytes.Contains(start.Body.Bytes(), []byte(`"state":"running"`)) {
				t.Fatalf("invocation: %d %s", start.Code, start.Body.String())
			}
			if jobCtx := <-service.started; jobCtx.Err() != nil {
				t.Fatalf("accepted job inherited disconnected request: %v", jobCtx.Err())
			}
			if rec := request("admin", http.MethodPost, "/calibre/reconciliation"); rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte(`"stage":"identity"`)) {
				t.Fatalf("duplicate should return current stage: %d %s", rec.Code, rec.Body.String())
			}
			if rec := request("admin", http.MethodGet, "/calibre/reconciliation/status"); !bytes.Contains(rec.Body.Bytes(), []byte(`"completedStages":["ownership"]`)) {
				t.Fatalf("running stage history: %s", rec.Body.String())
			}
			close(service.release)
			deadline := time.Now().Add(time.Second)
			for {
				rec := request("admin", http.MethodGet, "/calibre/reconciliation/status")
				var status CalibreReconciliationStatus
				if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
					t.Fatal(err)
				}
				if status.State != "running" {
					if status.State != test.state || status.StartedAt == nil || status.FinishedAt == nil || status.Result == nil || status.Result.Matched != test.result.Matched {
						t.Fatalf("final run status: %+v", status)
					}
					if test.err != nil {
						if status.Error != test.err.Error() || len(status.CompletedStages) != 2 {
							t.Fatalf("failure presented as success: %+v", status)
						}
					} else if status.Error != "" || len(status.CompletedStages) != 3 {
						t.Fatalf("completed stages: %+v", status)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("reconciliation did not finish")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestCalibreReconciliationRejectsShutdownAndConcurrentAudit(t *testing.T) {
	service := &controlledReconciliation{enabled: true, started: make(chan context.Context, 1), release: make(chan struct{})}
	closed := jobs.NewGroup(context.Background())
	closed.Shutdown(time.Second)
	h := NewCalibreAuditHandler(service, &db.CalibreAuditRepo{}).WithJobs(closed)
	rec := httptest.NewRecorder()
	h.Reconcile(rec, httptest.NewRequest(http.MethodPost, "/calibre/reconciliation", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("shutdown: %d %s", rec.Code, rec.Body.String())
	}
	status := httptest.NewRecorder()
	h.ReconcileStatus(status, httptest.NewRequest(http.MethodGet, "/calibre/reconciliation/status", nil))
	if !bytes.Contains(status.Body.Bytes(), []byte(`"state":"idle"`)) {
		t.Fatalf("shutdown left running: %s", status.Body.String())
	}
	group := jobs.NewGroup(context.Background())
	defer group.Shutdown(time.Second)
	h = NewCalibreAuditHandler(service, &db.CalibreAuditRepo{}).WithJobs(group)
	h.mu.Lock()
	h.recheck.Running = true
	h.mu.Unlock()
	rec = httptest.NewRecorder()
	h.Reconcile(rec, httptest.NewRequest(http.MethodPost, "/calibre/reconciliation", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("manual audit should exclude reconciliation: %d", rec.Code)
	}
	h.mu.Lock()
	h.recheck.Running = false
	h.reconcile.State = "running"
	h.mu.Unlock()
	rec = httptest.NewRecorder()
	h.Recheck(rec, httptest.NewRequest(http.MethodPost, "/calibre/audit/recheck", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("reconciliation should exclude manual audit: %d", rec.Code)
	}
}
