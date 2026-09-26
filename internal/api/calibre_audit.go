package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/models"
)

// calibreAuditService is the existing audit and availability surface.
// Optional tag projection is implemented by a separate capability.
type calibreAuditService interface {
	IsEnabled(context.Context) bool
	Audit(context.Context) (*calibre.AuditResult, error)
}

// CalibreAuditRecheckStatus is the current or most recent manual audit in this process.
// Scheduled reconciliation still uses the service's own serialization lock.
type CalibreAuditRecheckStatus struct {
	Running    bool                 `json:"running"`
	StartedAt  *time.Time           `json:"startedAt,omitempty"`
	FinishedAt *time.Time           `json:"finishedAt,omitempty"`
	Result     *calibre.AuditResult `json:"result,omitempty"`
	Error      string               `json:"error,omitempty"`
}

// CalibreAuditHandler exposes advisory findings and a tracked manual audit to admins.
// It never edits curated Calibre metadata; an opted-in service may project
// review state through only the Bindery-owned mismatch tag.
type CalibreAuditHandler struct {
	service  calibreAuditService
	findings *db.CalibreAuditRepo
	jobs     *jobs.Group

	mu      sync.Mutex
	recheck CalibreAuditRecheckStatus
}

func NewCalibreAuditHandler(service calibreAuditService, findings *db.CalibreAuditRepo) *CalibreAuditHandler {
	return &CalibreAuditHandler{service: service, findings: findings}
}

// WithJobs attaches the process shutdown-scoped job group. Without it rechecks
// are refused rather than starting an untracked audit against a closing DB.
func (h *CalibreAuditHandler) WithJobs(group *jobs.Group) *CalibreAuditHandler {
	h.jobs = group
	return h
}

func (h *CalibreAuditHandler) available(w http.ResponseWriter, r *http.Request) bool {
	if h.service == nil || h.findings == nil || !h.service.IsEnabled(r.Context()) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "authoritative Calibre audit is not enabled"})
		return false
	}
	return true
}

// Identity returns the latest rooted discovery snapshot for a currently
// matched work. It is admin-only at the router; CWA claims in the response are
// comparisons, never evidence used to select a canonical work or edition.
func (h *CalibreAuditHandler) Identity(w http.ResponseWriter, r *http.Request) {
	if !h.available(w, r) {
		return
	}
	bookID, err := strconv.ParseInt(chi.URLParam(r, "bookID"), 10, 64)
	if err != nil || bookID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid book id"})
		return
	}
	reader, ok := h.service.(interface {
		IdentitySnapshot(context.Context, int64) (*models.CalibreIdentitySnapshot, error)
	})
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "identity evidence unavailable"})
		return
	}
	snapshot, err := reader.IdentitySnapshot(r.Context(), bookID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if snapshot == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "identity evidence unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

// List returns a filtered, bounded page of findings and the matching count.
func (h *CalibreAuditHandler) List(w http.ResponseWriter, r *http.Request) {
	if !h.available(w, r) {
		return
	}
	q := r.URL.Query()
	state, kind, assessment := q.Get("state"), q.Get("findingType"), q.Get("assessment")
	if !auditChoice(state, "", models.CalibreAuditUnresolved, models.CalibreAuditIgnored, models.CalibreAuditResolved, models.CalibreAuditUnmatched) ||
		!auditChoice(kind, "", models.CalibreAuditIdentifierMissing, models.CalibreAuditIdentifierConflict,
			models.CalibreAuditTitleDifference, models.CalibreAuditAuthorDifference, models.CalibreAuditSeriesDifference,
			models.CalibreAuditPositionDifference, models.CalibreAuditLanguageDifference, models.CalibreAuditPubDateDifference) ||
		!auditChoice(assessment, "", models.CalibreAuditNeedsReview, models.CalibreAuditAmbiguous) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid audit filter"})
		return
	}
	limit, offset := parseLimitOffset(r, 50, 250)
	items, total, err := h.findings.ListPage(r.Context(), db.CalibreAuditListOpts{
		State: state, FindingType: kind, Assessment: assessment, Limit: limit, Offset: offset,
	})
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items  []models.CalibreAuditFinding `json:"items"`
		Total  int                          `json:"total"`
		Limit  int                          `json:"limit"`
		Offset int                          `json:"offset"`
	}{items, total, limit, offset})
}

func auditChoice(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

// Ignore records a human decision only if the finding still has the displayed
// comparison fingerprint and is unresolved. A stale view returns 409.
func (h *CalibreAuditHandler) Ignore(w http.ResponseWriter, r *http.Request) {
	if !h.available(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid finding id"})
		return
	}
	var body struct {
		ComparisonFingerprint string `json:"comparisonFingerprint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ComparisonFingerprint == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "comparisonFingerprint is required"})
		return
	}
	ok, err := h.findings.Ignore(r.Context(), id, body.ComparisonFingerprint)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "finding changed or is no longer unresolved; refresh the review queue"})
		return
	}
	// Ignore is already committed. A tag failure cannot undo the reviewer's
	// decision; the next recheck retries against committed finding state.
	if reconciler, ok := h.service.(interface{ ReconcileAuditTags(context.Context) error }); ok {
		if err := reconciler.ReconcileAuditTags(r.Context()); err != nil {
			slog.Warn("calibre audit ignore: tag reconciliation failed; decision retained", "finding_id", id, "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// Recheck accepts a full audit for background execution. Its Calibre read
// snapshot remains read-only; optional tag projection runs only after the
// findings commit. The handler's gate rejects duplicate manual starts; the
// shared service's passMu continues to serialize this with scheduled passes.
func (h *CalibreAuditHandler) Recheck(w http.ResponseWriter, r *http.Request) {
	if !h.available(w, r) {
		return
	}
	if h.jobs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "audit worker is unavailable"})
		return
	}
	h.mu.Lock()
	if h.recheck.Running {
		status := h.recheck
		h.mu.Unlock()
		writeJSON(w, http.StatusConflict, status)
		return
	}
	now := time.Now().UTC()
	h.recheck = CalibreAuditRecheckStatus{Running: true, StartedAt: &now}
	if !h.jobs.Go("calibre-manual-audit", h.runRecheck) {
		h.recheck = CalibreAuditRecheckStatus{}
		h.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "audit worker is shutting down"})
		return
	}
	status := h.recheck
	h.mu.Unlock()
	writeJSON(w, http.StatusAccepted, status)
}

func (h *CalibreAuditHandler) runRecheck(ctx context.Context) {
	// The job group supplies shutdown-scoped context, never the HTTP request's
	// context. Even a disconnected client cannot interrupt an accepted pass.
	var result *calibre.AuditResult
	var err error
	defer func() {
		finished := time.Now().UTC()
		h.mu.Lock()
		h.recheck.Running = false
		h.recheck.FinishedAt = &finished
		if rec := recover(); rec != nil {
			h.recheck.Error = "audit failed unexpectedly"
			slog.Error("calibre manual audit panicked", "panic", rec)
		} else if err != nil {
			h.recheck.Error = err.Error()
			slog.Error("calibre manual audit failed", "error", err)
		} else {
			h.recheck.Result = result
		}
		h.mu.Unlock()
	}()
	result, err = h.service.Audit(ctx)
}

// RecheckStatus returns the in-process run state, including completion or
// failure. It is intentionally polled separately from the paginated queue.
func (h *CalibreAuditHandler) RecheckStatus(w http.ResponseWriter, r *http.Request) {
	if !h.available(w, r) {
		return
	}
	h.mu.Lock()
	status := h.recheck
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, status)
}
