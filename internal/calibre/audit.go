package calibre

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// AuditResult describes one complete comparison pass. Findings is the number
// of current differences, including unchanged and ignored ones; Updated is the
// number of finding rows written (new, reopened, resolved, or unmatched).
type AuditResult struct {
	TotalCalibreBooks int `json:"totalCalibreBooks"`
	ComparedBooks     int `json:"comparedBooks"`
	Findings          int `json:"findings"`
	Updated           int `json:"updated"`
	// TagUpdated counts changed Calibre book links; TagError reports a failed
	// optional write without rolling back committed advisory findings.
	TagUpdated int    `json:"tagUpdated,omitempty"`
	TagError   string `json:"tagError,omitempty"`
}

// WithAudit enables the advisory audit after reconciliation. Existing callers
// without an audit repo retain the #5 ownership-only behaviour.
func (s *AuthoritativeService) WithAudit(repo *db.CalibreAuditRepo) *AuthoritativeService {
	s.audits = repo
	return s
}

// Audit rereads both catalogues and rechecks only confidently matched,
// externally sourced owned books. It never changes ownership matches or
// curated metadata. Findings commit in Bindery first; the separately opted-in
// BinderyMismatch tag projection runs afterward and may fail independently.
// An unconfigured audit, missing data source, or disabled mode cannot clear
// existing findings.
func (s *AuthoritativeService) Audit(ctx context.Context) (*AuditResult, error) {
	if !s.IsEnabled(ctx) {
		return nil, ErrAuthoritativeDisabled
	}
	if s.books == nil || s.editions == nil || s.crossRef == nil || s.audits == nil {
		return nil, fmt.Errorf("calibre metadata audit requires book, edition, cross-reference and audit repositories")
	}
	s.passMu.Lock()
	defer s.passMu.Unlock()
	reader, err := OpenReader(s.LibraryPath(ctx))
	if err != nil {
		return nil, fmt.Errorf("open authoritative reader for audit: %w", err)
	}
	defer func() { _ = reader.Close() }()

	calibreBooks, err := reader.AllBooksMetadata(ctx)
	if err != nil {
		return nil, fmt.Errorf("read calibre books for audit: %w", err)
	}
	binderyBooks, err := s.books.ListIncludingExcluded(ctx)
	if err != nil {
		return nil, fmt.Errorf("read bindery books for audit: %w", err)
	}
	ids, err := s.books.ListAllBookIdentifiers(ctx)
	if err != nil {
		return nil, fmt.Errorf("read bindery identifiers for audit: %w", err)
	}
	editions, err := s.editions.ListAllEditions(ctx)
	if err != nil {
		return nil, fmt.Errorf("read bindery editions for audit: %w", err)
	}
	return s.auditSnapshot(ctx, calibreBooks, binderyBooks, ids, editions, NewLibraryIndex(calibreBooks))
}

type auditFindingKey struct {
	bookID int64
	field  string
	key    string
}

// auditSnapshot uses the live Calibre snapshot and bulk-loaded stored provider
// evidence from the same pass as reconciliation. Joins and old results are
// fetched once; all comparisons and lifecycle decisions use indexed maps.
func (s *AuthoritativeService) auditSnapshot(
	ctx context.Context, calibreBooks []CalibreBook, binderyBooks []models.Book,
	ids map[int64][]models.BookIdentifier, editions map[int64][]models.Edition, idx *LibraryIndex,
) (*AuditResult, error) {
	refs, err := s.crossRef.ListByStatus(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("read calibre audit cross-references: %w", err)
	}
	previous, err := s.audits.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("read previous calibre audit findings: %w", err)
	}
	series, err := s.audits.ListMatchedSeriesEvidence(ctx)
	if err != nil {
		return nil, fmt.Errorf("read calibre audit series evidence: %w", err)
	}

	booksByID := make(map[int64]*models.Book, len(binderyBooks))
	for i := range binderyBooks {
		b := &binderyBooks[i]
		b.Identifiers = ids[b.ID]
		b.Editions = editions[b.ID]
		booksByID[b.ID] = b
	}
	identity, err := s.refreshIdentity(ctx, refs, booksByID, idx)
	if err != nil {
		return nil, fmt.Errorf("refresh calibre identity evidence: %w", err)
	}
	old := make(map[auditFindingKey]models.CalibreAuditFinding, len(previous))
	for _, f := range previous {
		old[auditFindingKey{f.BookID, f.Field, f.EvidenceKey}] = f
	}

	result := &AuditResult{TotalCalibreBooks: len(calibreBooks)}
	var changes []models.CalibreAuditFinding
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("calibre metadata audit interrupted: %w", err)
		}
		if ref.Status != models.CalibreMatchStatusMatched ||
			(ref.Confidence != models.CalibreMatchConfidenceExact &&
				ref.Confidence != models.CalibreMatchConfidenceHigh &&
				ref.Confidence != models.CalibreMatchConfidenceMedium) || ref.CalibreID <= 0 {
			continue
		}
		book := booksByID[ref.BookID]
		cb, exists := idx.GetBook(ref.CalibreID)
		if book == nil || !exists || book.Excluded || !book.WantsEbook() || !auditExternalBook(book) {
			continue
		}
		// A persisted match can become ambiguous when an edition, provider ID,
		// or Calibre record changes before the next reconciliation. Do not
		// compare (or replace prior findings) until the same match still holds.
		match := idx.MatchWork(auditMatchEvidence(book))
		if match.Status != models.CalibreMatchStatusMatched || match.CalibreID != cb.CalibreID {
			continue
		}

		result.ComparedBooks++
		currentRef := ref
		currentRef.MatchMethod = match.MatchMethod
		currentRef.Confidence = match.Confidence
		outcomes := compareAuditBookWithIdentity(book, cb, &currentRef, series[book.ID], identity[book.ID])
		for key, outcome := range outcomes {
			findingKey := auditFindingKey{ref.BookID, key.field, key.key}
			prior, exists := old[findingKey]
			delete(old, findingKey)
			if !outcome.different {
				if !exists { // Never persist every clean value as a shadow catalogue.
					continue
				}
				f := outcome.finding
				f.FindingType = prior.FindingType
				f.Assessment = prior.Assessment
				f.State = models.CalibreAuditResolved
				f.Reason = "Current values agree under the field's conservative audit normalization."
				if !sameAuditFinding(prior, f, f.State) {
					changes = append(changes, f)
				}
				continue
			}
			result.Findings++
			f := outcome.finding
			if !exists || !sameAuditFinding(prior, f, auditEffectiveState(prior, f)) {
				changes = append(changes, f)
			}
		}
	}
	// A former finding with no confident match or no longer comparable
	// provider evidence is not resolved: it is unknown, and remains inspectable.
	for _, prior := range old {
		if prior.State == models.CalibreAuditUnmatched {
			continue
		}
		prior.State = models.CalibreAuditUnmatched
		prior.Reason = "No current confident match or comparable external evidence; the prior difference cannot be rechecked."
		changes = append(changes, prior)
	}
	written, err := s.audits.Apply(ctx, changes)
	if err != nil {
		return nil, fmt.Errorf("persist calibre audit findings: %w", err)
	}
	result.Updated = written
	result.TagUpdated, err = s.reconcileAuditTags(ctx)
	if err != nil {
		// Findings are already committed: never misrepresent them or require a
		// Calibre write for audit to succeed. A later full pass retries even if
		// none of the finding rows change.
		result.TagError = err.Error()
		slog.Warn("calibre audit tag reconciliation failed; findings remain committed", "error", err)
	}
	return result, nil
}

// An unchanged value and owned match preserve an ignore even when the raw
// spelling or harmless formatting changed. Apply also enforces this rule in
// SQL if a human ignores a finding concurrently with a running audit.
func auditEffectiveState(prior, next models.CalibreAuditFinding) string {
	if prior.IgnoredFingerprint != "" && prior.CalibreID == next.CalibreID &&
		prior.IgnoredFingerprint == next.ComparisonFingerprint {
		return models.CalibreAuditIgnored
	}
	return next.State
}

func sameAuditFinding(prior, next models.CalibreAuditFinding, effectiveState string) bool {
	return prior.CalibreID == next.CalibreID && prior.Field == next.Field && prior.EvidenceKey == next.EvidenceKey &&
		prior.FindingType == next.FindingType && prior.Assessment == next.Assessment &&
		prior.MatchMethod == next.MatchMethod && prior.MatchConfidence == next.MatchConfidence &&
		prior.Reason == next.Reason && prior.ComparisonFingerprint == next.ComparisonFingerprint &&
		prior.State == effectiveState &&
		slices.Equal(prior.CalibreEvidence, next.CalibreEvidence) &&
		slices.Equal(prior.BinderyEvidence, next.BinderyEvidence)
}
