package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/vavallee/bindery/internal/calibre"
)

// CalibreReconciliationStatus describes the current or last explicit run in this
// process. Restarts reset it; scheduled passes are serialized by the service but
// are not represented as explicit operator runs.
type CalibreReconciliationStatus struct {
	State           string                   `json:"state"`
	Stage           string                   `json:"stage,omitempty"`
	CompletedStages []string                 `json:"completedStages"`
	StartedAt       *time.Time               `json:"startedAt,omitempty"`
	FinishedAt      *time.Time               `json:"finishedAt,omitempty"`
	Result          *calibre.ReconcileResult `json:"result,omitempty"`
	Error           string                   `json:"error,omitempty"`
}

// Reconcile starts the ownership -> bounded identity -> audit workflow. It
// never runs the optional Calibre mismatch-tag writer or scans ebook files.
func (h *CalibreAuditHandler) Reconcile(w http.ResponseWriter, r *http.Request) {
	if !h.available(w, r) {
		return
	}
	if h.jobs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reconciliation worker is unavailable"})
		return
	}
	service, ok := h.service.(interface {
		ReconcileReadOnlyWithProgress(context.Context, func(string)) (*calibre.ReconcileResult, error)
	})
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reconciliation is unavailable"})
		return
	}
	h.mu.Lock()
	if h.reconcile.State == "running" || h.recheck.Running {
		status := h.reconciliationStatus()
		h.mu.Unlock()
		writeJSON(w, http.StatusConflict, status)
		return
	}
	now := time.Now().UTC()
	h.reconcile = CalibreReconciliationStatus{State: "running", Stage: "ownership", CompletedStages: []string{}, StartedAt: &now}
	if !h.jobs.Go("calibre-manual-reconciliation", func(ctx context.Context) { h.runReconciliation(ctx, service) }) {
		h.reconcile = CalibreReconciliationStatus{}
		h.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reconciliation worker is shutting down"})
		return
	}
	status := h.reconciliationStatus()
	h.mu.Unlock()
	writeJSON(w, http.StatusAccepted, status)
}

// reconciliationStatus must be called with h.mu held.
func (h *CalibreAuditHandler) reconciliationStatus() CalibreReconciliationStatus {
	status := h.reconcile
	if status.State == "" {
		status.State = "idle"
	}
	status.CompletedStages = append([]string{}, status.CompletedStages...)
	return status
}

// completeReconciliationStage must be called with h.mu held.
func (h *CalibreAuditHandler) completeReconciliationStage() {
	stage := h.reconcile.Stage
	if stage == "" {
		return
	}
	for _, completed := range h.reconcile.CompletedStages {
		if completed == stage {
			return
		}
	}
	h.reconcile.CompletedStages = append(h.reconcile.CompletedStages, stage)
}

func (h *CalibreAuditHandler) runReconciliation(ctx context.Context, service interface {
	ReconcileReadOnlyWithProgress(context.Context, func(string)) (*calibre.ReconcileResult, error)
}) {
	var result *calibre.ReconcileResult
	var err error
	defer func() {
		finished := time.Now().UTC()
		h.mu.Lock()
		defer h.mu.Unlock()
		h.reconcile.FinishedAt = &finished
		h.reconcile.Result = result // partial ownership counts remain visible on failure
		if rec := recover(); rec != nil {
			h.reconcile.State, h.reconcile.Error = "failed", "reconciliation failed unexpectedly"
			slog.Error("calibre manual reconciliation panicked", "panic", rec)
		} else if err != nil {
			h.reconcile.State, h.reconcile.Error = "failed", err.Error()
			slog.Error("calibre manual reconciliation failed", "error", err)
		} else {
			h.completeReconciliationStage()
			h.reconcile.State = "completed"
			if result != nil && result.Identity != nil && (result.Identity.FailedLookups > 0 ||
				result.Identity.TruncatedLookups > 0 || result.Identity.UnconfiguredLookups > 0 ||
				result.Identity.NotAttemptedLookups > 0 || result.Identity.DeferredWorks > 0 ||
				result.Identity.UnresolvedRoots > 0 || result.Identity.DiscoveryUnavailable) {
				h.reconcile.State = "partial"
			}
		}
		h.reconcile.Stage = ""
	}()
	result, err = service.ReconcileReadOnlyWithProgress(ctx, func(stage string) {
		h.mu.Lock()
		if h.reconcile.Stage != stage {
			h.completeReconciliationStage()
		}
		h.reconcile.Stage = stage
		h.mu.Unlock()
	})
}

// ReconcileStatus returns the process-local explicit run state, including
// completed stage boundaries and any partial/provider outcomes.
func (h *CalibreAuditHandler) ReconcileStatus(w http.ResponseWriter, r *http.Request) {
	if !h.available(w, r) {
		return
	}
	h.mu.Lock()
	status := h.reconciliationStatus()
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, status)
}
