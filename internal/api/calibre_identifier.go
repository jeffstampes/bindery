package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
)

func identifierFindingID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid identifier finding id")
	}
	return id, nil
}

// IdentifierProposals returns only currently eligible, evidence-backed adds.
// It never accepts a browser's proposal as evidence or writes to Calibre.
func (h *CalibreAuditHandler) IdentifierProposals(w http.ResponseWriter, r *http.Request) {
	if !h.available(w, r) {
		return
	}
	id, err := identifierFindingID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	service, ok := h.service.(interface {
		IdentifierProposals(context.Context, int64) ([]calibre.CalibreIdentifierProposal, error)
	})
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "identifier reconciliation unavailable"})
		return
	}
	items, err := service.IdentifierProposals(r.Context(), id)
	if errors.Is(err, calibre.ErrIdentifierWriteDisabled) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "identifier writes require their separate opt-in"})
		return
	}
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if items == nil {
		items = []calibre.CalibreIdentifierProposal{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// IdentifierAdd is an individual, explicit approval of a server-generated
// proposal. Only the current comparison fingerprint and proposed value are
// echoed by the browser; the server selects type/book/evidence afresh.
func (h *CalibreAuditHandler) IdentifierAdd(w http.ResponseWriter, r *http.Request) {
	if !h.available(w, r) {
		return
	}
	id, err := identifierFindingID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var body struct {
		ComparisonFingerprint string `json:"comparisonFingerprint"`
		ProposedValue         string `json:"proposedValue"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.ComparisonFingerprint == "" || body.ProposedValue == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "comparisonFingerprint and proposedValue are required"})
		return
	}
	service, ok := h.service.(interface {
		AddIdentifier(context.Context, int64, int64, string, string) (*db.CalibreIdentifierAttempt, string, error)
	})
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "identifier reconciliation unavailable"})
		return
	}
	attempt, reauditError, err := service.AddIdentifier(r.Context(), id, auth.UserIDFromContext(r.Context()), body.ComparisonFingerprint, body.ProposedValue)
	if errors.Is(err, calibre.ErrIdentifierWriteDisabled) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "identifier writes require their separate opt-in"})
		return
	}
	if errors.Is(err, calibre.ErrIdentifierProposalStale) || errors.Is(err, db.ErrCalibreIdentifierConflict) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "proposal is stale or ineligible; refresh the review queue", "attemptId": attemptID(attempt)})
		return
	}
	if err != nil {
		// An external writer or the Bindery attempt log may be unavailable.
		// A pending attempt is not proof that Calibre committed or rolled back.
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "identifier write could not be confirmed; inspect the attempt and Calibre book before retrying", "attemptId": attemptID(attempt)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attemptId": attempt.ID, "outcome": attempt.Outcome, "reauditError": reauditError})
}

func attemptID(attempt *db.CalibreIdentifierAttempt) int64 {
	if attempt == nil {
		return 0
	}
	return attempt.ID
}

func (h *CalibreAuditHandler) IdentifierAttempts(w http.ResponseWriter, r *http.Request) {
	if !h.available(w, r) {
		return
	}
	id, err := identifierFindingID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	service, ok := h.service.(interface {
		IdentifierAttempts(context.Context, int64) ([]db.CalibreIdentifierAttempt, error)
	})
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "identifier history unavailable"})
		return
	}
	items, err := service.IdentifierAttempts(r.Context(), id)
	if errors.Is(err, calibre.ErrIdentifierWriteDisabled) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "identifier history unavailable"})
		return
	}
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if items == nil {
		items = []db.CalibreIdentifierAttempt{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
