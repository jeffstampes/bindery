package models

import "time"

// Confidence levels for CalibreWorkCrossReference.
const (
	CalibreMatchConfidenceExact     = "exact"
	CalibreMatchConfidenceHigh      = "high"
	CalibreMatchConfidenceMedium    = "medium"
	CalibreMatchConfidenceLow       = "low"
	CalibreMatchConfidenceAmbiguous = "ambiguous"
)

// Status values for CalibreWorkCrossReference.
const (
	CalibreMatchStatusMatched   = "matched"
	CalibreMatchStatusAmbiguous = "ambiguous"
	CalibreMatchStatusStale     = "stale"
	CalibreMatchStatusIgnored   = "ignored"
)

// CalibreWorkCrossReference links a Bindery work (Book) to a Calibre book ID
// for authoritative library mode (#4). It records match method, provenance,
// confidence, and a fingerprint of the Calibre record for stale detection.
type CalibreWorkCrossReference struct {
	ID                 int64     `json:"id"`
	BookID             int64     `json:"bookId"`
	CalibreID          int64     `json:"calibreId"`
	MatchMethod        string    `json:"matchMethod"`
	Confidence         string    `json:"confidence"`
	Status             string    `json:"status"`
	CalibreFingerprint string    `json:"calibreFingerprint"`
	MatchDetailsJSON   string    `json:"matchDetailsJson"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}
