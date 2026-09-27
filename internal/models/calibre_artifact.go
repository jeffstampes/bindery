package models

import "time"

// CalibreArtifactScan records an explicit read of one owned file, independently
// of CWA's mutable metadata claims and of provider-discovery refreshes.
type CalibreArtifactScan struct {
	BookID           int64                       `json:"bookId"`
	CalibreID        int64                       `json:"calibreId"`
	Format           string                      `json:"format"`
	FileName         string                      `json:"fileName"`
	FilePath         string                      `json:"filePath"` // relative to the configured Calibre library
	SHA256           string                      `json:"sha256,omitempty"`
	SizeBytes        int64                       `json:"sizeBytes"`
	ModifiedAt       time.Time                   `json:"modifiedAt"`
	ScannedAt        time.Time                   `json:"scannedAt"`
	Method           string                      `json:"method"`
	Outcome          string                      `json:"outcome"` // scanned, partial, unsupported, failed
	Error            string                      `json:"error,omitempty"`
	CorrelationGroup string                      `json:"correlationGroup,omitempty"`
	Stale            bool                        `json:"stale"`
	Historical       bool                        `json:"historical,omitempty"`
	AttestedOriginal bool                        `json:"attestedOriginal"` // explicit operator claim, never inferred
	Lineage          string                      `json:"lineage"`          // pre_writeback, potentially_cwa_derived, unknown
	Identifiers      []CalibreArtifactIdentifier `json:"identifiers"`
}

// CalibreArtifactWriteback is an operator-reported fact about an external
// Calibre-to-file metadata operation. It does not certify file contents.
type CalibreArtifactWriteback struct {
	BookID     int64     `json:"bookId"`
	CalibreID  int64     `json:"calibreId"`
	WrittenAt  time.Time `json:"writtenAt"`
	RecordedAt time.Time `json:"recordedAt"`
	Source     string    `json:"source"`
}

// CalibreArtifactIdentifier is an observation, not an edition assignment.
// Status and edition IDs are computed against the current canonical-root graph
// on read, so a later provider refresh cannot turn an old guess into authority.
type CalibreArtifactIdentifier struct {
	ObservedValue     string   `json:"observedValue"`
	NormalizedValue   string   `json:"normalizedValue"`
	Source            string   `json:"source"`   // epub_opf or epub_content
	Location          string   `json:"location"` // ZIP member name
	Selected          bool     `json:"selected"`
	Status            string   `json:"status"` // matches_work, conflict, unverified
	EditionIDs        []string `json:"editionIds"`
	EditionConfidence string   `json:"editionConfidence"` // candidate or unresolved
}
