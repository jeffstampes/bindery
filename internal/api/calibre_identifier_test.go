package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
)

type identifierReviewService struct {
	enabled               bool
	writeEnabled          bool
	addErr                error
	fingerprint, proposed string
	actor                 int64
}

func (s *identifierReviewService) IsEnabled(context.Context) bool { return s.enabled }
func (s *identifierReviewService) Audit(context.Context) (*calibre.AuditResult, error) {
	return &calibre.AuditResult{}, nil
}
func (s *identifierReviewService) IdentifierProposals(_ context.Context, id int64) ([]calibre.CalibreIdentifierProposal, error) {
	if !s.writeEnabled {
		return nil, calibre.ErrIdentifierWriteDisabled
	}
	return []calibre.CalibreIdentifierProposal{{FindingID: id, IdentifierType: "openlibrary", ProposedValue: "OL100W", Action: "add"}}, nil
}
func (s *identifierReviewService) AddIdentifier(_ context.Context, id, actor int64, fingerprint, proposed string) (*db.CalibreIdentifierAttempt, string, error) {
	s.fingerprint, s.proposed, s.actor = fingerprint, proposed, actor
	if !s.writeEnabled {
		return nil, "", calibre.ErrIdentifierWriteDisabled
	}
	if s.addErr != nil {
		return &db.CalibreIdentifierAttempt{ID: 13}, "", s.addErr
	}
	return &db.CalibreIdentifierAttempt{ID: 13, FindingID: id, Outcome: "applied"}, "", nil
}
func (s *identifierReviewService) IdentifierAttempts(context.Context, int64) ([]db.CalibreIdentifierAttempt, error) {
	return []db.CalibreIdentifierAttempt{{ID: 13, Outcome: "applied"}}, nil
}

func TestCalibreIdentifierWriteSettingIndependentOptIn(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	setting, err := repo.Get(ctx, SettingCalibreIdentifierWriteEnabled)
	if err != nil || setting != nil {
		t.Fatalf("identifier writer must start unset: %+v %v", setting, err)
	}
	desc, ok := LookupSettingDescriptor(SettingCalibreIdentifierWriteEnabled)
	if !ok || desc.Default != "false" || desc.Type != SettingTypeBool || !desc.Writable {
		t.Fatalf("identifier opt-in descriptor: %+v %v", desc, ok)
	}
	put := func(value string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := withKey(httptest.NewRequest(http.MethodPut,
			"/api/v1/setting/"+SettingCalibreIdentifierWriteEnabled,
			bytes.NewBufferString(`{"value":"`+value+`"}`)), SettingCalibreIdentifierWriteEnabled)
		h.Set(rec, req)
		return rec
	}
	if rec := put("true"); rec.Code != http.StatusBadRequest {
		t.Fatalf("identifier opt-in without authoritative mode: %d", rec.Code)
	}
	if err := repo.Set(ctx, SettingCalibreLibraryPath, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingCalibreAuthoritativeLibraryEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	if rec := put("yes"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid identifier opt-in accepted: %d", rec.Code)
	}
	if rec := put("true"); rec.Code != http.StatusOK {
		t.Fatalf("independent identifier opt-in: %d %s", rec.Code, rec.Body.String())
	}
	if rec := put("false"); rec.Code != http.StatusOK {
		t.Fatalf("disable identifier writes: %d", rec.Code)
	}
}

func TestCalibreIdentifierReviewRoutes(t *testing.T) {
	s := &identifierReviewService{enabled: true}
	h := NewCalibreAuditHandler(s, &db.CalibreAuditRepo{})
	router := chi.NewRouter()
	router.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/calibre/audit/{id}/identifier-proposals", h.IdentifierProposals)
		r.Post("/calibre/audit/{id}/identifier-add", h.IdentifierAdd)
		r.Get("/calibre/audit/{id}/identifier-attempts", h.IdentifierAttempts)
	})
	request := func(role, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req = req.WithContext(auth.WithUserRole(auth.WithUserID(req.Context(), 7), role))
		router.ServeHTTP(rec, req)
		return rec
	}
	path := "/calibre/audit/42/identifier-"
	if rec := request("user", http.MethodPost, path+"add", `{"comparisonFingerprint":"fp","proposedValue":"OL100W"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin write: %d", rec.Code)
	}
	if rec := request("user", http.MethodGet, path+"proposals", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin preview: %d", rec.Code)
	}
	s.enabled = false
	if rec := request("admin", http.MethodPost, path+"add", `{"comparisonFingerprint":"fp","proposedValue":"OL100W"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled authoritative mode: %d", rec.Code)
	}
	s.enabled = true
	if rec := request("admin", http.MethodGet, path+"proposals", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("separate write opt-in not enforced on preview: %d", rec.Code)
	}
	if rec := request("admin", http.MethodPost, path+"add", `{"comparisonFingerprint":"fp","proposedValue":"OL100W"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("separate write opt-in not enforced on apply: %d", rec.Code)
	}
	s.writeEnabled = true
	if rec := request("admin", http.MethodGet, "/calibre/audit/nope/identifier-proposals", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid finding ID: %d", rec.Code)
	}
	if rec := request("admin", http.MethodGet, path+"proposals", ""); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"proposedValue":"OL100W"`)) {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("admin", http.MethodPost, path+"add", `{"comparisonFingerprint":"fp"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("incomplete approval: %d", rec.Code)
	}
	s.addErr = calibre.ErrIdentifierWriteDisabled
	if rec := request("admin", http.MethodPost, path+"add", `{"comparisonFingerprint":"fp","proposedValue":"OL100W"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("identifier-write opt-in not enforced: %d", rec.Code)
	}
	s.addErr = calibre.ErrIdentifierProposalStale
	if rec := request("admin", http.MethodPost, path+"add", `{"comparisonFingerprint":"fp","proposedValue":"OL100W"}`); rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte(`"attemptId":13`)) {
		t.Fatalf("stale approval: %d %s", rec.Code, rec.Body.String())
	}
	s.addErr = errors.New("Calibre database is locked")
	if rec := request("admin", http.MethodPost, path+"add", `{"comparisonFingerprint":"fp","proposedValue":"OL100W"}`); rec.Code != http.StatusServiceUnavailable || !bytes.Contains(rec.Body.Bytes(), []byte(`"attemptId":13`)) {
		t.Fatalf("retryable failure: %d %s", rec.Code, rec.Body.String())
	}
	s.addErr = nil
	rec := request("admin", http.MethodPost, path+"add", `{"comparisonFingerprint":"fp","proposedValue":"OL100W","identifierType":"isbn"}`)
	if rec.Code != http.StatusOK || s.actor != 7 || s.fingerprint != "fp" || s.proposed != "OL100W" {
		t.Fatalf("approved identifier: %d %s, service=%+v", rec.Code, rec.Body.String(), s)
	}
	var result struct {
		AttemptID int64  `json:"attemptId"`
		Outcome   string `json:"outcome"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.AttemptID != 13 || result.Outcome != "applied" {
		t.Fatalf("write result: %+v %v", result, err)
	}
	if rec := request("admin", http.MethodGet, path+"attempts", ""); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"outcome":"applied"`)) {
		t.Fatalf("attempt history: %d %s", rec.Code, rec.Body.String())
	}
}
