package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
)

func refreshBookID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "bookID"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid Bindery book ID")
	}
	return id, nil
}

// RefreshEligibility exposes only the persisted ownership policy for the UI.
// It never fetches metadata and cannot authorize a preview or apply.
func (h *CalibreAuditHandler) RefreshEligibility(w http.ResponseWriter, r *http.Request) {
	if !h.available(w, r) {
		return
	}
	id, err := refreshBookID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	service, ok := h.service.(interface {
		MetadataRefreshEligibility(context.Context, int64) (calibre.MetadataRefreshEligibility, error)
	})
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "metadata refresh unavailable"})
		return
	}
	eligibility, err := service.MetadataRefreshEligibility(r.Context(), id)
	if errors.Is(err, calibre.ErrMetadataRefreshDisabled) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eligibility)
}

// RefreshPreview starts one frozen lookup for the matched book. It does not
// apply fetched OPF content and is independently gated by the refresh setting.
func (h *CalibreAuditHandler) RefreshPreview(w http.ResponseWriter, r *http.Request) {
	if !h.available(w, r) {
		return
	}
	id, err := refreshBookID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	service, ok := h.service.(interface {
		PreviewMetadataRefresh(context.Context, int64) (*calibre.MetadataRefreshProposal, error)
	})
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "metadata refresh unavailable"})
		return
	}
	p, err := service.PreviewMetadataRefresh(r.Context(), id)
	if errors.Is(err, calibre.ErrMetadataRefreshDisabled) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	if errors.Is(err, calibre.ErrMetadataRefreshIneligible) {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "ownership_ineligible", "error": err.Error()})
		return
	}
	if errors.Is(err, calibre.ErrMetadataRefreshStale) {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "stale", "error": err.Error()})
		return
	}
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// RefreshApply accepts only the immutable proposal ID and its fingerprint;
// client-supplied fields, target IDs, and OPF bytes are never honored.
func (h *CalibreAuditHandler) RefreshApply(w http.ResponseWriter, r *http.Request) {
	if !h.available(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "proposalID"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid proposal ID"})
		return
	}
	var input struct {
		Fingerprint string `json:"fingerprint"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Fingerprint == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "fingerprint required"})
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "one approval object required"})
		return
	}
	service, ok := h.service.(interface {
		ApplyMetadataRefresh(context.Context, int64, int64, string) (*db.CalibreMetadataRefreshAttempt, error)
	})
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "metadata refresh unavailable"})
		return
	}
	attempt, err := service.ApplyMetadataRefresh(r.Context(), id, auth.UserIDFromContext(r.Context()), input.Fingerprint)
	if errors.Is(err, calibre.ErrMetadataRefreshDisabled) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	if errors.Is(err, calibre.ErrMetadataRefreshStale) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "attempt": attempt})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Calibre write or verification failed; inspect the attempt before retrying", "attempt": attempt})
		return
	}
	writeJSON(w, http.StatusOK, attempt)
}

func (h *CalibreAuditHandler) RefreshAttempts(w http.ResponseWriter, r *http.Request) {
	if !h.available(w, r) {
		return
	}
	id, err := refreshBookID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	service, ok := h.service.(interface {
		MetadataRefreshAttempts(context.Context, int64) ([]db.CalibreMetadataRefreshAttempt, error)
	})
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "metadata refresh unavailable"})
		return
	}
	items, err := service.MetadataRefreshAttempts(r.Context(), id)
	if errors.Is(err, calibre.ErrMetadataRefreshDisabled) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if items == nil {
		items = []db.CalibreMetadataRefreshAttempt{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
