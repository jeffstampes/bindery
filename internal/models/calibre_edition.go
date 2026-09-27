package models

// CalibreEditionResolution is advisory and never changes ownership or canonical
// work identity. Only exact/high with a single selected edition may guide audit.
type CalibreEditionResolution struct {
	Confidence string                    `json:"confidence"` // exact, high, ambiguous, unresolved
	EditionID  string                    `json:"editionId,omitempty"`
	Provider   string                    `json:"provider,omitempty"`
	Reason     string                    `json:"reason"`
	Candidates []CalibreEditionCandidate `json:"candidates"`
}

// CalibreEditionCandidate is an edition within the established canonical work.
// Reasons describe matching *sources*, not a count of correlated fields.
type CalibreEditionCandidate struct {
	EditionID string   `json:"editionId"`
	Provider  string   `json:"provider"`
	Reasons   []string `json:"reasons"`
}
