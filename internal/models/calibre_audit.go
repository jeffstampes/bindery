package models

import "time"

// Calibre audit states describe a comparison, not an edit to either catalogue.
const (
	CalibreAuditUnresolved = "unresolved"
	CalibreAuditIgnored    = "ignored"
	CalibreAuditResolved   = "resolved"
	CalibreAuditUnmatched  = "unmatched"
)

const (
	CalibreAuditFieldIdentifiers = "identifiers"
	CalibreAuditFieldTitle       = "title"
	CalibreAuditFieldAuthors     = "authors"
	CalibreAuditFieldSeries      = "series_membership"
	CalibreAuditFieldPosition    = "series_position"
	CalibreAuditFieldLanguage    = "language"
	CalibreAuditFieldPubDate     = "publication_date"
)

const (
	CalibreAuditIdentifierMissing  = "identifier_missing"
	CalibreAuditIdentifierConflict = "identifier_conflict"
	CalibreAuditTitleDifference    = "title_difference"
	CalibreAuditAuthorDifference   = "author_difference"
	CalibreAuditSeriesDifference   = "series_membership_difference"
	CalibreAuditPositionDifference = "series_position_difference"
	CalibreAuditLanguageDifference = "language_difference"
	CalibreAuditPubDateDifference  = "publication_date_difference"
)

const (
	CalibreAuditNeedsReview = "needs_review"
	CalibreAuditAmbiguous   = "ambiguous"
)

// CalibreAuditEvidence names the exact stored value and its origin. Bindery
// evidence may be work-level or edition-level; neither outranks Calibre.
type CalibreAuditEvidence struct {
	Value     string `json:"value"`
	Source    string `json:"source"`
	Provider  string `json:"provider,omitempty"`
	ForeignID string `json:"foreignId,omitempty"`
	RecordID  int64  `json:"recordId,omitempty"`
}

// CalibreAuditFinding is an advisory comparison for one matched owned book.
// EvidenceKey distinguishes identifier types and series positions within a
// field. The fingerprint covers only materially compared values and identities:
// an ignored finding is reopened if either side changes, not for formatting.
// Resolved and unmatched rows retain the last evidence for review history.
type CalibreAuditFinding struct {
	ID     int64 `json:"id"`
	BookID int64 `json:"bookId"`
	// BookTitle is a read-only display projection, not persisted in audit findings.
	BookTitle             string                 `json:"bookTitle,omitempty"`
	CalibreID             int64                  `json:"calibreId"`
	Field                 string                 `json:"field"`
	EvidenceKey           string                 `json:"evidenceKey"`
	FindingType           string                 `json:"findingType"`
	Assessment            string                 `json:"assessment"`
	CalibreEvidence       []CalibreAuditEvidence `json:"calibreEvidence"`
	BinderyEvidence       []CalibreAuditEvidence `json:"binderyEvidence"`
	MatchMethod           string                 `json:"matchMethod"`
	MatchConfidence       string                 `json:"matchConfidence"`
	Reason                string                 `json:"reason"`
	ComparisonFingerprint string                 `json:"comparisonFingerprint"`
	// IgnoredFingerprint retains a reviewer decision while a comparison is
	// temporarily unmatched; it is cleared when materially new evidence appears.
	IgnoredFingerprint string    `json:"-"`
	State              string    `json:"state"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}
