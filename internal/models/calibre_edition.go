package models

// Presentation reason codes describe the branch that actually resolved (or
// blocked) an edition. Diagnostic Reason remains unchanged for support.
const (
	CalibreEditionReasonNoEvidence       = "no_edition_evidence"
	CalibreEditionReasonNoWork           = "ownership_unavailable"
	CalibreEditionReasonNoWorkLookup     = "work_lookup_unavailable"
	CalibreEditionReasonNoEbookEditions  = "no_ebook_editions"
	CalibreEditionReasonIncomplete       = "lookup_incomplete"
	CalibreEditionReasonConflicting      = "conflicting_evidence"
	CalibreEditionReasonOriginalFile     = "independent_file_isbn"
	CalibreEditionReasonHistoricFile     = "historical_file_isbn"
	CalibreEditionReasonCalibreIDs       = "calibre_identifiers"
	CalibreEditionReasonCalibreFields    = "calibre_metadata"
	CalibreEditionReasonMultiple         = "multiple_editions"
	CalibreEditionWarningPossiblyDerived = "possibly_calibre_derived"
)

// CalibreEditionResolution is advisory and never changes ownership or canonical
// work identity. Only exact/high with a single selected edition may guide audit.
type CalibreEditionResolution struct {
	Confidence      string                    `json:"confidence"` // exact, high, ambiguous, unresolved
	EditionID       string                    `json:"editionId,omitempty"`
	Provider        string                    `json:"provider,omitempty"`
	Reason          string                    `json:"reason"`
	ReasonCode      string                    `json:"reasonCode"`
	ArtifactWarning string                    `json:"artifactWarning,omitempty"`
	Candidates      []CalibreEditionCandidate `json:"candidates"`
}

// CalibreEditionClaim identifies a typed CWA value embedded in a candidate
// reason, so the UI need not infer identifier types from explanatory prose.
type CalibreEditionClaim struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// CalibreEditionCandidate is an edition within the established canonical work.
// Reasons describe matching *sources*, not a count of correlated fields.
type CalibreEditionCandidate struct {
	EditionID string                `json:"editionId"`
	Provider  string                `json:"provider"`
	Reasons   []string              `json:"reasons"`
	Claims    []CalibreEditionClaim `json:"claims,omitempty"`
}
