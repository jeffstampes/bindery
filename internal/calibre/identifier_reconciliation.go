package calibre

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

const identifierWriteSetting = "calibre.identifier_write_enabled"

var (
	ErrIdentifierWriteDisabled = errors.New("calibre identifier writes are disabled")
	ErrIdentifierProposalStale = errors.New("identifier proposal changed; refresh the audit review")
)

// CalibreIdentifierProposal is an evidence-backed, single missing work-ID add.
// The browser cannot choose a table, field or replacement value at apply time.
type CalibreIdentifierProposal struct {
	FindingID             int64                         `json:"findingId"`
	BookID                int64                         `json:"bookId"`
	CalibreID             int64                         `json:"calibreId"`
	ComparisonFingerprint string                        `json:"comparisonFingerprint"`
	IdentifierType        string                        `json:"identifierType"`
	CurrentValue          string                        `json:"currentValue"`
	ProposedValue         string                        `json:"proposedValue"`
	Action                string                        `json:"action"`
	EvidenceKeys          []string                      `json:"evidenceKeys"`
	Evidence              []models.CalibreAuditEvidence `json:"evidence"`
	Reason                string                        `json:"reason"`
}

// WithIdentifierAttempts registers the Bindery-only provenance store. It does
// not provide the authoritative reader with a writable connection.
func (s *AuthoritativeService) WithIdentifierAttempts(repo *db.CalibreIdentifierAttemptRepo) *AuthoritativeService {
	s.identifierAttempts = repo
	return s
}

func (s *AuthoritativeService) identifierWritesEnabled(ctx context.Context) (bool, error) {
	if !s.IsEnabled(ctx) || s.identifierAttempts == nil || s.identity == nil || s.audits == nil || s.books == nil || s.editions == nil || s.crossRef == nil {
		return false, nil
	}
	setting, err := s.settings.Get(ctx, identifierWriteSetting)
	if err != nil {
		return false, fmt.Errorf("read identifier-write opt-in: %w", err)
	}
	return setting != nil && strings.EqualFold(setting.Value, "true"), nil
}

// IdentifierProposals validates a persisted finding against the current rooted
// evidence, ownership link and live read-only CWA book. No UI-supplied value is
// accepted as an authority. Edition IDs/ISBNs and replacements are excluded.
func (s *AuthoritativeService) IdentifierProposals(ctx context.Context, findingID int64) ([]CalibreIdentifierProposal, error) {
	s.passMu.Lock()
	defer s.passMu.Unlock()
	return s.identifierProposals(ctx, findingID)
}

func (s *AuthoritativeService) identifierProposals(ctx context.Context, findingID int64) ([]CalibreIdentifierProposal, error) {
	allowed, err := s.identifierWritesEnabled(ctx)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrIdentifierWriteDisabled
	}
	finding, err := s.audits.GetByID(ctx, findingID)
	if err != nil {
		return nil, err
	}
	if finding == nil || finding.State != models.CalibreAuditUnresolved ||
		finding.FindingType != models.CalibreAuditIdentifierMissing || finding.Assessment != models.CalibreAuditNeedsReview ||
		finding.Field != models.CalibreAuditFieldIdentifiers || !workIdentifierType(finding.EvidenceKey) {
		return nil, nil
	}
	ref, err := s.crossRef.GetByBookID(ctx, finding.BookID)
	if err != nil {
		return nil, err
	}
	if ref == nil || ref.CalibreID != finding.CalibreID || ref.Status != models.CalibreMatchStatusMatched ||
		(ref.Confidence != models.CalibreMatchConfidenceExact && ref.Confidence != models.CalibreMatchConfidenceHigh) || ref.CalibreFingerprint == "" {
		return nil, nil
	}
	book, err := s.books.GetByID(ctx, finding.BookID)
	if err != nil {
		return nil, err
	}
	if book == nil || book.Excluded || !book.WantsEbook() || !auditExternalBook(book) {
		return nil, nil
	}
	book.Identifiers, err = s.books.ListBookIdentifiers(ctx, book.ID)
	if err != nil {
		return nil, fmt.Errorf("read current Bindery work identifiers: %w", err)
	}
	book.Editions, err = s.editions.ListByBook(ctx, book.ID)
	if err != nil {
		return nil, fmt.Errorf("read current Bindery editions: %w", err)
	}
	snapshot, err := s.identity.ListByBookID(ctx, book.ID)
	if err != nil {
		return nil, err
	}
	if snapshot == nil || snapshot.CalibreID != finding.CalibreID || snapshot.RootKey != identityRootKey(book) ||
		snapshot.CheckedAt.IsZero() || time.Since(snapshot.CheckedAt) > identityFreshFor {
		return nil, nil
	}
	reader, err := OpenReader(s.LibraryPath(ctx))
	if err != nil {
		return nil, fmt.Errorf("open live Calibre identifier preview: %w", err)
	}
	defer func() { _ = reader.Close() }()
	calibreBook, err := reader.GetBook(ctx, finding.CalibreID)
	if errors.Is(err, ErrBookNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read current Calibre identifier: %w", err)
	}
	// Re-run the audit's independent ownership matcher against live Calibre
	// candidates. A second book can make the old link ambiguous without
	// changing this book's fingerprint. Apply reruns this under the writer lock.
	match, err := MatchWork(ctx, auditMatchEvidence(book), reader)
	if err != nil {
		return nil, fmt.Errorf("recheck live Calibre ownership: %w", err)
	}
	if match.Status != models.CalibreMatchStatusMatched || match.CalibreID != finding.CalibreID ||
		(match.Confidence != models.CalibreMatchConfidenceExact && match.Confidence != models.CalibreMatchConfidenceHigh) {
		return nil, nil
	}
	// A changed CWA record must be reconciled before this old ownership or
	// finding can authorize a write. The writer separately checks raw rows.
	if CalculateFingerprint(calibreBook) != ref.CalibreFingerprint || len(calibreAuditIdentifiers(calibreBook)[finding.EvidenceKey]) != 0 {
		return nil, nil
	}
	rootISBNs := make(map[string]bool)
	rootFound := false
	for _, evidence := range snapshot.Evidence {
		if evidence.Status == models.CalibreIdentityRoot && evidence.Method == metadata.RawMethodExactBook &&
			evidence.Provider == identityCanonicalProvider(book.MetadataProvider) && evidence.ForeignID == book.ForeignID &&
			evidence.Seed == book.ForeignID && evidence.EditionID == "" && evidence.CanonicalIdentity == snapshot.RootKey {
			rootFound = true
			for _, isbn := range evidence.NormalizedIdentifiers["isbn"] {
				rootISBNs[isbn] = true
			}
		}
	}
	if !rootFound {
		return nil, nil
	}
	values := make(map[string][]string)
	for _, evidence := range snapshot.Evidence {
		if evidence.EditionID != "" || evidence.CanonicalIdentity != snapshot.RootKey || evidence.Key == "" || evidence.ProvenanceGroup == "" ||
			auditIdentifier(finding.EvidenceKey, evidence.ForeignID) == "" {
			continue
		}
		validRoot := evidence.Status == models.CalibreIdentityRoot && evidence.WorkConfidence == "exact" &&
			evidence.Method == metadata.RawMethodExactBook && evidence.Provider == identityCanonicalProvider(book.MetadataProvider) &&
			evidence.ForeignID == book.ForeignID && evidence.Seed == book.ForeignID
		validCorroboration := evidence.Status == models.CalibreIdentityCorroborated && evidence.WorkConfidence == "high" &&
			evidence.Method == metadata.RawMethodISBN && rootISBNs[evidence.Seed]
		if !validRoot && !validCorroboration {
			continue
		}
		for _, value := range evidence.NormalizedIdentifiers[finding.EvidenceKey] {
			if value != "" && value == auditIdentifier(finding.EvidenceKey, evidence.ForeignID) {
				values[value] = append(values[value], evidence.Key)
			}
		}
	}
	if len(values) != 1 {
		return nil, nil
	}
	for value, keys := range values {
		// A current audit discrepancy is a review anchor, not an independent
		// vote. It must describe this same rooted, uniquely supported value.
		found := false
		for _, e := range finding.BinderyEvidence {
			if auditIdentifier(finding.EvidenceKey, e.Value) == value {
				found = true
			}
		}
		if !found {
			return nil, nil
		}
		for _, e := range finding.CalibreEvidence {
			if strings.TrimSpace(e.Value) != "" {
				return nil, nil
			}
		}
		slices.Sort(keys)
		keys = slices.Compact(keys)
		proposal := CalibreIdentifierProposal{
			FindingID: finding.ID, BookID: book.ID, CalibreID: finding.CalibreID,
			ComparisonFingerprint: finding.ComparisonFingerprint, IdentifierType: finding.EvidenceKey,
			CurrentValue: "", ProposedValue: value, Action: "add", EvidenceKeys: keys,
			Reason: "Exact canonical work or rooted ISBN corroboration; no edition identity asserted.",
		}
		for _, evidence := range snapshot.Evidence {
			if slices.Contains(keys, evidence.Key) {
				proposal.Evidence = append(proposal.Evidence, models.CalibreAuditEvidence{
					Value: value, Source: "calibre_identity_evidence." + evidence.Method,
					Provider: evidence.Provider, ForeignID: evidence.ForeignID,
				})
			}
		}
		return []CalibreIdentifierProposal{proposal}, nil
	}
	return nil, nil
}

func workIdentifierType(typ string) bool {
	switch typ {
	case "openlibrary", "google", "hardcover", "dnb":
		return true
	}
	return false
}

// IdentifierAttempts exposes bounded durable history for an individual
// finding, including rejected, failed and interrupted writes.
func (s *AuthoritativeService) IdentifierAttempts(ctx context.Context, findingID int64) ([]db.CalibreIdentifierAttempt, error) {
	if s.identifierAttempts == nil || !s.IsEnabled(ctx) {
		return nil, ErrIdentifierWriteDisabled
	}
	return s.identifierAttempts.ListByFinding(ctx, findingID)
}

// AddIdentifier applies one fresh, independently revalidated add. Attempts are
// durable in Bindery before the external transaction; only committed writes
// are marked applied. Failed and rejected attempts do not edit audit findings.
func (s *AuthoritativeService) AddIdentifier(ctx context.Context, findingID, actorID int64, fingerprint, proposedValue string) (*db.CalibreIdentifierAttempt, string, error) {
	s.passMu.Lock()
	allowed, err := s.identifierWritesEnabled(ctx)
	if err != nil || !allowed {
		s.passMu.Unlock()
		if err != nil {
			return nil, "", err
		}
		return nil, "", ErrIdentifierWriteDisabled
	}
	finding, err := s.audits.GetByID(ctx, findingID)
	if err != nil {
		s.passMu.Unlock()
		return nil, "", err
	}
	if finding == nil {
		s.passMu.Unlock()
		return nil, "", ErrIdentifierProposalStale
	}
	attempt := &db.CalibreIdentifierAttempt{
		FindingID: finding.ID, ActorUserID: actorID, BookID: finding.BookID, CalibreID: finding.CalibreID,
		IdentifierType: finding.EvidenceKey, ProposedValue: proposedValue, Action: "add",
		ComparisonFingerprint: fingerprint,
	}
	if attempt.ProposedValue == "" {
		s.passMu.Unlock()
		return nil, "", ErrIdentifierProposalStale
	}
	if err := s.identifierAttempts.Begin(ctx, attempt); err != nil {
		s.passMu.Unlock()
		return nil, "", err
	}
	proposals, err := s.identifierProposals(ctx, findingID)
	if err != nil {
		finishErr := s.identifierAttempts.Finish(ctx, attempt.ID, "failed", err.Error())
		s.passMu.Unlock()
		if finishErr != nil {
			return attempt, "", errors.Join(err, finishErr)
		}
		return attempt, "", err
	}
	if len(proposals) != 1 || proposals[0].ComparisonFingerprint != fingerprint ||
		proposals[0].ProposedValue != proposedValue {
		finishErr := s.identifierAttempts.Finish(ctx, attempt.ID, "rejected", "stale or ineligible identifier proposal")
		s.passMu.Unlock()
		if finishErr != nil {
			return attempt, "", finishErr
		}
		return attempt, "", ErrIdentifierProposalStale
	}
	attempt.EvidenceKeys = proposals[0].EvidenceKeys
	// The pre-write log must include the evidence IDs, so finish the pending
	// row's proof before opening the separately bounded Calibre writer.
	if err := s.identifierAttempts.SetEvidenceKeys(ctx, attempt.ID, attempt.EvidenceKeys); err != nil {
		s.passMu.Unlock()
		return attempt, "", err
	}
	libraryPath := s.LibraryPath(ctx)
	err = db.NewCalibreIdentifierWriter(libraryPath).AddMissingIf(ctx, attempt.CalibreID, attempt.IdentifierType, attempt.ProposedValue,
		func(lockedCtx context.Context) error {
			if s.LibraryPath(lockedCtx) != libraryPath {
				return ErrIdentifierProposalStale
			}
			// Recheck ownership, the complete book fingerprint, and rooted
			// evidence while the writer holds SQLite's reserved write lock.
			fresh, checkErr := s.identifierProposals(lockedCtx, findingID)
			if checkErr != nil {
				return checkErr
			}
			if len(fresh) != 1 || fresh[0].CalibreID != attempt.CalibreID ||
				fresh[0].ComparisonFingerprint != fingerprint || fresh[0].ProposedValue != proposedValue ||
				!slices.Equal(fresh[0].EvidenceKeys, attempt.EvidenceKeys) {
				return ErrIdentifierProposalStale
			}
			return nil
		})
	if err != nil {
		outcome := "failed"
		if errors.Is(err, ErrIdentifierProposalStale) || errors.Is(err, db.ErrCalibreIdentifierConflict) {
			outcome = "rejected"
		}
		finishErr := s.identifierAttempts.Finish(ctx, attempt.ID, outcome, err.Error())
		s.passMu.Unlock()
		if finishErr != nil {
			return attempt, "", fmt.Errorf("calibre write and attempt recording failed: %w", errors.Join(err, finishErr))
		}
		return attempt, "", fmt.Errorf("write Calibre identifier: %w", err)
	}
	if err := s.identifierAttempts.Finish(ctx, attempt.ID, "applied", ""); err != nil {
		s.passMu.Unlock()
		return attempt, "", fmt.Errorf("calibre identifier committed but attempt outcome not recorded; inspect library before retrying: %w", err)
	}
	attempt.Outcome = "applied"
	s.passMu.Unlock()
	// The ordinary audit compares fresh CWA state and retires discrepancies;
	// it never marks a finding resolved just because the write succeeded.
	_, auditErr := s.Audit(ctx)
	if auditErr != nil {
		return attempt, auditErr.Error(), nil // committed write remains inspectable; retry a recheck
	}
	return attempt, "", nil
}
