package models

import "time"

// CalibreIdentitySnapshot is advisory evidence: it never replaces the owned book's curated
// Calibre/CWA metadata. The root key is the canonical identity selected for a
// Bindery work, or empty when the discovery pass did not establish one.
type CalibreIdentitySnapshot struct {
	BookID    int64                     `json:"bookId"`
	CalibreID int64                     `json:"calibreId"`
	RootKey   string                    `json:"rootKey"`
	Evidence  []CalibreIdentityEvidence `json:"evidence"`
	Lookups   []CalibreIdentityLookup   `json:"lookups"`
	Claims    []CalibreIdentityClaim    `json:"claims"`
	CheckedAt time.Time                 `json:"checkedAt"`
}

const (
	CalibreIdentityRoot         = "root"
	CalibreIdentityCorroborated = "corroborated"
	CalibreIdentityCandidate    = "candidate"
	CalibreIdentityConflict     = "conflict"
)

// CalibreIdentityEvidence has a stable key within one work that links it to a CWA claim.
// ProviderMetadata is provider-sourced identity context, NOT a copy of the
// Calibre book. NormalizedIdentifiers uses identifier types as keys.
type CalibreIdentityEvidence struct {
	Key                   string              `json:"key"`
	BookID                int64               `json:"bookId"`
	CalibreID             int64               `json:"calibreId"`
	CanonicalIdentity     string              `json:"canonicalIdentity"`
	Provider              string              `json:"provider"`
	ForeignID             string              `json:"foreignId"`
	EditionID             string              `json:"editionId,omitempty"`
	Method                string              `json:"method"`
	Seed                  string              `json:"seed"`
	ProvenanceGroup       string              `json:"provenanceGroup"`
	Status                string              `json:"status"`
	WorkConfidence        string              `json:"workConfidence"`
	EditionConfidence     string              `json:"editionConfidence"`
	NormalizedIdentifiers map[string][]string `json:"normalizedIdentifiers"`
	ProviderMetadata      map[string]any      `json:"providerMetadata"`
	CheckedAt             time.Time           `json:"checkedAt"`
}

const (
	CalibreIdentityLookupAnswered     = "answered"
	CalibreIdentityLookupEmpty        = "empty"
	CalibreIdentityLookupFailed       = "failed"
	CalibreIdentityLookupUnconfigured = "unconfigured"
	CalibreIdentityLookupTruncated    = "truncated"
	CalibreIdentityLookupNotAttempted = "not_attempted"
)

type CalibreIdentityLookup struct {
	BookID    int64     `json:"bookId"`
	CalibreID int64     `json:"calibreId"`
	Provider  string    `json:"provider"`
	Method    string    `json:"method"`
	Seed      string    `json:"seed"`
	Outcome   string    `json:"outcome"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

const (
	CalibreIdentityClaimAgrees     = "agrees"
	CalibreIdentityClaimConflicts  = "conflicts"
	CalibreIdentityClaimUnverified = "unverified"
)

// CalibreIdentityClaim is CWA's asserted identifier, kept only as the
// minimal comparison claim. EvidenceKey is empty when no provider evidence
// was linked; otherwise it refers to evidence within the same snapshot.
type CalibreIdentityClaim struct {
	BookID          int64     `json:"bookId"`
	CalibreID       int64     `json:"calibreId"`
	IdentifierType  string    `json:"identifierType"`
	IdentifierValue string    `json:"identifierValue"`
	Status          string    `json:"status"`
	EvidenceKey     string    `json:"evidenceKey,omitempty"`
	CheckedAt       time.Time `json:"checkedAt"`
}
