package calibre

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

type identityDiscoverer interface {
	DiscoverRawBookEvidence(context.Context, string, string) metadata.RawBookDiscovery
}

// WithIdentityEvidence enables Bindery-rooted, read-only provider discovery.
// The optional seam keeps ownership-only services and non-authoritative mode
// unchanged; the repository writes only to Bindery's database.
func (s *AuthoritativeService) WithIdentityEvidence(repo *db.CalibreIdentityRepo, source identityDiscoverer) *AuthoritativeService {
	s.identity, s.identitySource = repo, source
	return s
}

// IdentitySnapshot returns the latest advisory evidence for a currently matched
// work. It cannot read a stale Calibre link or turn provider evidence into an
// ownership assertion.
func (s *AuthoritativeService) IdentitySnapshot(ctx context.Context, bookID int64) (*models.CalibreIdentitySnapshot, error) {
	if !s.IsEnabled(ctx) || s.identity == nil || s.crossRef == nil {
		return nil, ErrAuthoritativeDisabled
	}
	ref, err := s.crossRef.GetByBookID(ctx, bookID)
	if err != nil {
		return nil, fmt.Errorf("read identity ownership link: %w", err)
	}
	if ref == nil || ref.Status != models.CalibreMatchStatusMatched {
		return nil, nil
	}
	snapshot, err := s.identity.ListByBookID(ctx, bookID)
	if err != nil {
		return nil, fmt.Errorf("read identity evidence: %w", err)
	}
	if snapshot == nil || snapshot.CalibreID != ref.CalibreID {
		return nil, nil
	}
	if s.books != nil {
		book, err := s.books.GetByID(ctx, bookID)
		if err != nil {
			return nil, fmt.Errorf("read identity root: %w", err)
		}
		if !auditExternalBook(book) || snapshot.RootKey != identityRootKey(book) {
			return nil, nil
		}
	}
	return snapshot, nil
}

// Refresh a bounded slice of matched works on each audit/reconciliation. A
// provider search is network I/O, not a per-book SQL query; old snapshots are
// loaded in four bulk reads and writes are transactional batches. A global
// deadline prevents a large library from blocking an audit indefinitely.
const (
	identityRefreshLimit      = 256
	identityRefreshWindow     = 2 * time.Minute
	identityFreshFor          = 7 * 24 * time.Hour
	identityRetryAfterFailure = 6 * time.Hour
)

// RefreshIdentity advances the bounded provider-evidence backlog without
// running an entire metadata audit or changing CWA/Bindery catalogue records.
// It reads the same bulk Calibre and Bindery snapshots used by Audit.
func (s *AuthoritativeService) RefreshIdentity(ctx context.Context) error {
	if !s.IsEnabled(ctx) || s.identity == nil || s.identitySource == nil {
		return nil
	}
	if s.books == nil || s.editions == nil || s.crossRef == nil {
		return fmt.Errorf("calibre identity refresh requires book, edition and cross-reference repositories")
	}
	s.passMu.Lock()
	defer s.passMu.Unlock()
	reader, err := OpenReader(s.LibraryPath(ctx))
	if err != nil {
		return fmt.Errorf("open authoritative reader for identity refresh: %w", err)
	}
	defer func() { _ = reader.Close() }()
	calibreBooks, err := reader.AllBooksMetadata(ctx)
	if err != nil {
		return fmt.Errorf("read calibre books for identity refresh: %w", err)
	}
	books, err := s.books.ListIncludingExcluded(ctx)
	if err != nil {
		return fmt.Errorf("read bindery books for identity refresh: %w", err)
	}
	ids, err := s.books.ListAllBookIdentifiers(ctx)
	if err != nil {
		return fmt.Errorf("read bindery identifiers for identity refresh: %w", err)
	}
	editions, err := s.editions.ListAllEditions(ctx)
	if err != nil {
		return fmt.Errorf("read bindery editions for identity refresh: %w", err)
	}
	refs, err := s.crossRef.ListByStatus(ctx, "")
	if err != nil {
		return fmt.Errorf("read ownership links for identity refresh: %w", err)
	}
	byID := make(map[int64]*models.Book, len(books))
	for i := range books {
		book := &books[i]
		book.Identifiers, book.Editions = ids[book.ID], editions[book.ID]
		byID[book.ID] = book
	}
	_, err = s.refreshIdentity(ctx, refs, byID, NewLibraryIndex(calibreBooks))
	return err
}

type identityWork struct {
	book *models.Book
	cb   *CalibreBook
}

func (s *AuthoritativeService) refreshIdentity(ctx context.Context, refs []models.CalibreWorkCrossReference,
	books map[int64]*models.Book, idx *LibraryIndex,
) (map[int64]models.CalibreIdentitySnapshot, error) {
	if s.identity == nil || s.identitySource == nil || !s.IsEnabled(ctx) {
		return nil, nil
	}
	previous, err := s.identity.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("load calibre identity evidence: %w", err)
	}
	refreshCtx, cancel := context.WithTimeout(ctx, identityRefreshWindow)
	defer cancel()
	eligible := make(map[int64]bool)
	pending := make([]models.CalibreIdentitySnapshot, 0, 32)
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		if err := s.identity.ReplaceBatch(ctx, pending); err != nil {
			return fmt.Errorf("persist calibre identity evidence: %w", err)
		}
		for _, snapshot := range pending {
			previous[snapshot.BookID] = snapshot
		}
		pending = pending[:0]
		return nil
	}
	jobs := make([]identityWork, 0, identityRefreshLimit)
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		book := books[ref.BookID]
		cb, ok := idx.GetBook(ref.CalibreID)
		if !identityEligible(ref, book, ok) {
			continue
		}
		match := idx.MatchWork(auditMatchEvidence(book))
		if match.Status != models.CalibreMatchStatusMatched || match.CalibreID != cb.CalibreID {
			continue
		}
		eligible[book.ID] = true
		rootKey := identityRootKey(book)
		old, exists := previous[book.ID]
		if exists && old.CalibreID == cb.CalibreID && old.RootKey == rootKey &&
			time.Since(old.CheckedAt) < identitySnapshotTTL(old) {
			claims := identityClaims(old.Evidence, cb)
			if !sameIdentityClaims(old.Claims, claims) {
				old.Claims = claims
				pending = append(pending, old)
			}
		} else if len(jobs) < identityRefreshLimit && refreshCtx.Err() == nil {
			jobs = append(jobs, identityWork{book, cb})
		}
		if len(pending) == 32 {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	// Metadata calls for unrelated works are independent. Keep total provider
	// concurrency bounded (four works, each with at most four provider calls),
	// and persist only completed observations when the pass deadline arrives.
	results := make([]models.CalibreIdentitySnapshot, len(jobs))
	parallel := make(chan struct{}, 4)
	var workers sync.WaitGroup
launch:
	for i, job := range jobs {
		if refreshCtx.Err() != nil {
			break
		}
		select {
		case parallel <- struct{}{}:
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-parallel }()
				provider := strings.ToLower(strings.TrimSpace(job.book.MetadataProvider))
				if provider == "ol" {
					provider = "openlibrary"
				}
				if provider == "google" {
					provider = "googlebooks"
				}
				raw := s.identitySource.DiscoverRawBookEvidence(refreshCtx, provider, job.book.ForeignID)
				snapshot := buildIdentitySnapshot(job.book, job.cb, raw)
				s.diagnoseIdentityClaims(refreshCtx, job.book, &snapshot)
				results[i] = snapshot
			}()
		case <-refreshCtx.Done():
			break launch
		}
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, snapshot := range results {
		if snapshot.BookID == 0 {
			continue
		}
		pending = append(pending, snapshot)
		if len(pending) == 32 {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	// A vanished, stale or ambiguous ownership link cannot keep exposing an
	// old positive identity graph. It does not erase the historical audit rows.
	for id, old := range previous {
		if eligible[id] || books[id] == nil {
			continue
		}
		if old.RootKey != "" || len(old.Evidence) > 0 || len(old.Claims) > 0 {
			pending = append(pending, models.CalibreIdentitySnapshot{BookID: id, CalibreID: old.CalibreID, CheckedAt: time.Now().UTC()})
			if len(pending) == 32 {
				if err := flush(); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return previous, nil
}

func identityEligible(ref models.CalibreWorkCrossReference, book *models.Book, calibreExists bool) bool {
	return book != nil && calibreExists && !book.Excluded && book.WantsEbook() && auditExternalBook(book) &&
		ref.Status == models.CalibreMatchStatusMatched && ref.CalibreID > 0 &&
		(ref.Confidence == models.CalibreMatchConfidenceExact || ref.Confidence == models.CalibreMatchConfidenceHigh ||
			ref.Confidence == models.CalibreMatchConfidenceMedium)
}

func identityRootKey(book *models.Book) string {
	return strings.ToLower(strings.TrimSpace(book.MetadataProvider)) + ":" + strings.TrimSpace(book.ForeignID)
}

func identitySnapshotTTL(snapshot models.CalibreIdentitySnapshot) time.Duration {
	rootFound := false
	for _, e := range snapshot.Evidence {
		if e.Status == models.CalibreIdentityRoot && e.Method == metadata.RawMethodExactBook {
			rootFound = true
			break
		}
	}
	for _, lookup := range snapshot.Lookups {
		if lookup.Outcome != models.CalibreIdentityLookupAnswered && lookup.Outcome != models.CalibreIdentityLookupEmpty {
			return identityRetryAfterFailure
		}
	}
	if !rootFound {
		return identityRetryAfterFailure
	}
	return identityFreshFor
}

func sameIdentityClaims(a, b []models.CalibreIdentityClaim) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].IdentifierType != b[i].IdentifierType || a[i].IdentifierValue != b[i].IdentifierValue ||
			a[i].Status != b[i].Status || a[i].EvidenceKey != b[i].EvidenceKey {
			return false
		}
	}
	return true
}

func identityKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:12])
}

func buildIdentitySnapshot(book *models.Book, cb *CalibreBook, raw metadata.RawBookDiscovery) models.CalibreIdentitySnapshot {
	checked := time.Now().UTC()
	rootKey := identityRootKey(book)
	snapshot := models.CalibreIdentitySnapshot{BookID: book.ID, CalibreID: cb.CalibreID, RootKey: rootKey, CheckedAt: checked}
	rootOK := false
	for _, obs := range raw.Observations {
		if obs.Method == metadata.RawMethodExactBook && obs.Provider == raw.CanonicalProvider &&
			obs.Seed == raw.CanonicalForeignID && obs.Outcome == metadata.RawOutcomeFound && obs.Book != nil {
			rootOK = true
		}
	}
	providerIDs := make(map[string]map[string]bool)
	for _, obs := range raw.Observations {
		outcome := models.CalibreIdentityLookupEmpty
		switch obs.Outcome {
		case metadata.RawOutcomeFound:
			outcome = models.CalibreIdentityLookupAnswered
		case metadata.RawOutcomeFailed, metadata.RawOutcomeConflict:
			outcome = models.CalibreIdentityLookupFailed
		case metadata.RawOutcomeUnconfigured:
			outcome = models.CalibreIdentityLookupUnconfigured
		case metadata.RawOutcomeSkipped:
			outcome = models.CalibreIdentityLookupNotAttempted
		}
		if obs.Truncated {
			outcome = models.CalibreIdentityLookupTruncated
		}
		reason := ""
		if obs.Err != nil {
			reason = obs.Err.Error()
		}
		if obs.Outcome == metadata.RawOutcomeConflict {
			reason = "canonical provider returned a different record"
		}
		if len(reason) > 300 {
			reason = reason[:300]
		}
		snapshot.Lookups = append(snapshot.Lookups, models.CalibreIdentityLookup{
			Provider: obs.Provider, Method: obs.Method, Seed: obs.Seed, Outcome: outcome, Error: reason, CheckedAt: checked,
		})
		if !rootOK {
			continue
		}
		status := models.CalibreIdentityCandidate
		if obs.Provider == raw.CanonicalProvider && (obs.Method == metadata.RawMethodExactBook || obs.Method == metadata.RawMethodExactEditions) {
			status = models.CalibreIdentityRoot
		}
		if obs.Book != nil && obs.Outcome == metadata.RawOutcomeFound {
			candidate := obs.Book
			if obs.Method == metadata.RawMethodISBN && status != models.CalibreIdentityRoot && identitySameWork(raw, candidate) {
				// A provider's ISBN lookup is itself an observation that this
				// record answers the seed, even when its thin response omits ISBNs.
				status = models.CalibreIdentityCorroborated
			}
			// An ISBN can map to several different works at the same provider;
			// suppress corroboration of all of them until a human resolves it.
			if obs.Method == metadata.RawMethodISBN && status != models.CalibreIdentityRoot {
				if providerIDs[obs.Provider] == nil {
					providerIDs[obs.Provider] = make(map[string]bool)
				}
				providerIDs[obs.Provider][candidate.ForeignID] = true
			}
			snapshot.Evidence = append(snapshot.Evidence, identityBookEvidence(book, cb, obs, *candidate, status, checked))
		}
		for _, candidate := range obs.Candidates {
			snapshot.Evidence = append(snapshot.Evidence, identityBookEvidence(book, cb, obs, candidate, status, checked))
		}
		for _, candidate := range obs.RejectedCandidates {
			snapshot.Evidence = append(snapshot.Evidence, identityBookEvidence(book, cb, obs, candidate, models.CalibreIdentityConflict, checked))
		}
		if obs.Method == metadata.RawMethodExactEditions && obs.Outcome == metadata.RawOutcomeFound {
			for _, ed := range obs.Editions {
				snapshot.Evidence = append(snapshot.Evidence, identityEditionEvidence(book, cb, obs, ed, checked))
			}
		}
	}
	for i := range snapshot.Evidence {
		e := &snapshot.Evidence[i]
		if e.Status == models.CalibreIdentityCorroborated && len(providerIDs[e.Provider]) > 1 {
			e.Status, e.WorkConfidence = models.CalibreIdentityCandidate, "low"
		}
	}
	if raw.SeedsTruncated {
		snapshot.Lookups = append(snapshot.Lookups, models.CalibreIdentityLookup{
			Provider: raw.CanonicalProvider, Method: metadata.RawMethodISBN, Seed: "more_root_isbns",
			Outcome: models.CalibreIdentityLookupTruncated, CheckedAt: checked,
		})
	}
	snapshot.Claims = identityClaims(snapshot.Evidence, cb)
	return snapshot
}

// diagnoseIdentityClaims follows at most two CWA provider IDs *after* the
// Bindery root has been resolved. These fresh exact responses explain a claim,
// but remain candidates: their IDs/ISBNs never become discovery seeds, votes,
// ownership matches or permanent book aliases.
func (s *AuthoritativeService) diagnoseIdentityClaims(ctx context.Context, book *models.Book, snapshot *models.CalibreIdentitySnapshot) {
	lookup, ok := s.identitySource.(interface {
		GetBookFromProvider(context.Context, string, string) (*models.Book, error)
	})
	if !ok || ctx.Err() != nil {
		return
	}
	rootFound := false
	for _, e := range snapshot.Evidence {
		if e.Status == models.CalibreIdentityRoot && e.Method == metadata.RawMethodExactBook {
			rootFound = true
			break
		}
	}
	if !rootFound {
		return
	}
	checked := snapshot.CheckedAt
	count := 0
	claims := slices.Clone(snapshot.Claims)
	slices.SortStableFunc(claims, func(a, b models.CalibreIdentityClaim) int {
		if a.Status == models.CalibreIdentityClaimConflicts && b.Status != a.Status {
			return -1
		}
		if b.Status == models.CalibreIdentityClaimConflicts && a.Status != b.Status {
			return 1
		}
		return 0
	})
	for _, claim := range claims {
		if count >= 2 || ctx.Err() != nil {
			break
		}
		if claim.Status == models.CalibreIdentityClaimAgrees {
			continue
		}
		provider, id := "", auditIdentifier(claim.IdentifierType, claim.IdentifierValue)
		switch claim.IdentifierType {
		case "openlibrary":
			if openLibraryWorkIDRe.MatchString(id) {
				provider = "openlibrary"
			}
		case "hardcover":
			provider, id = "hardcover", "hc:"+id
		case "google":
			provider, id = "googlebooks", "gb:"+id
		case "dnb":
			provider, id = "dnb", "dnb:"+id
		}
		if provider == "" || len(id) > 72 || strings.ContainsAny(id, "/?#\r\n\x00") {
			continue
		}
		count++
		callCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		found, err := lookup.GetBookFromProvider(callCtx, provider, id)
		cancel()
		outcome, reason := models.CalibreIdentityLookupEmpty, ""
		if err != nil {
			outcome = models.CalibreIdentityLookupFailed
			if errors.Is(err, metadata.ErrProviderNotConfigured) {
				outcome = models.CalibreIdentityLookupUnconfigured
			}
			reason = err.Error()
			if len(reason) > 300 {
				reason = reason[:300]
			}
		} else if found != nil && auditIdentifier(claim.IdentifierType, found.ForeignID) == auditIdentifier(claim.IdentifierType, id) {
			outcome = models.CalibreIdentityLookupAnswered
			obs := metadata.RawBookObservation{Provider: provider, Method: "diagnostic_exact", Seed: id}
			evidence := identityBookEvidence(book, &CalibreBook{CalibreID: snapshot.CalibreID}, obs, *found, models.CalibreIdentityCandidate, checked)
			evidence.ProvenanceGroup = "cwa_claim:" + provider + ":" + id
			snapshot.Evidence = append(snapshot.Evidence, evidence)
		} else if found != nil {
			outcome, reason = models.CalibreIdentityLookupFailed, "provider returned a different record"
		}
		snapshot.Lookups = append(snapshot.Lookups, models.CalibreIdentityLookup{
			Provider: provider, Method: "diagnostic_exact", Seed: id, Outcome: outcome, Error: reason, CheckedAt: checked,
		})
	}
}

func identitySameWork(raw metadata.RawBookDiscovery, candidate *models.Book) bool {
	if candidate == nil || candidate.ForeignID == "" {
		return false
	}
	for _, obs := range raw.Observations {
		if obs.Method != metadata.RawMethodExactBook || obs.Provider != raw.CanonicalProvider || obs.Book == nil ||
			obs.Outcome != metadata.RawOutcomeFound {
			continue
		}
		root := obs.Book
		return textutil.FoldForTitleMatch(root.Title) != "" &&
			textutil.FoldForTitleMatch(root.Title) == textutil.FoldForTitleMatch(candidate.Title) &&
			root.Author != nil && candidate.Author != nil && auditAuthor(root.Author.Name) != "" &&
			auditAuthor(root.Author.Name) == auditAuthor(candidate.Author.Name)
	}
	return false
}

func identityBookEvidence(book *models.Book, cb *CalibreBook, obs metadata.RawBookObservation,
	candidate models.Book, status string, checked time.Time) models.CalibreIdentityEvidence {
	ids := map[string][]string{}
	if typ := auditIdentifierType(obs.Provider, candidate.ForeignID); typ != "" {
		ids[typ] = []string{auditIdentifier(typ, candidate.ForeignID)}
	}
	for _, isbn := range candidate.ProviderISBNs {
		identityAddISBN(ids, isbn)
	}
	if obs.Method == metadata.RawMethodISBN {
		identityAddISBN(ids, obs.Seed)
	}
	for _, ed := range candidate.Editions {
		if ed.ISBN13 != nil {
			identityAddISBN(ids, *ed.ISBN13)
		}
		if ed.ISBN10 != nil {
			identityAddISBN(ids, *ed.ISBN10)
		}
	}
	confidence := "low"
	if status == models.CalibreIdentityRoot {
		confidence = "exact"
	}
	if status == models.CalibreIdentityCorroborated {
		confidence = "high"
	}
	if status == models.CalibreIdentityConflict {
		confidence = "none"
	}
	metadata := map[string]any{"title": candidate.Title, "language": candidate.Language}
	if candidate.Author != nil {
		metadata["author"] = candidate.Author.Name
	}
	return models.CalibreIdentityEvidence{
		Key: identityKey(obs.Provider, obs.Method, obs.Seed, candidate.ForeignID), BookID: book.ID,
		CalibreID: cb.CalibreID, CanonicalIdentity: identityRootKey(book), Provider: obs.Provider,
		ForeignID: candidate.ForeignID, Method: obs.Method, Seed: obs.Seed,
		ProvenanceGroup: obs.Provider + ":" + candidate.ForeignID, Status: status,
		WorkConfidence: confidence, EditionConfidence: "unresolved",
		NormalizedIdentifiers: ids, ProviderMetadata: metadata, CheckedAt: checked,
	}
}

func identityEditionEvidence(book *models.Book, cb *CalibreBook, obs metadata.RawBookObservation,
	ed models.Edition, checked time.Time) models.CalibreIdentityEvidence {
	ids := map[string][]string{}
	if typ := auditIdentifierType(obs.Provider+"_edition", ed.ForeignID); typ != "" {
		ids[typ] = []string{auditIdentifier(typ, ed.ForeignID)}
	}
	if ed.ISBN13 != nil {
		identityAddISBN(ids, *ed.ISBN13)
	}
	if ed.ISBN10 != nil {
		identityAddISBN(ids, *ed.ISBN10)
	}
	if ed.ASIN != nil && auditExternalEdition(&ed) {
		ids["asin"] = []string{auditIdentifier("asin", *ed.ASIN)}
	}
	metadata := map[string]any{"title": ed.Title, "publisher": ed.Publisher, "language": ed.Language,
		"format": ed.Format, "ebook": auditExternalEdition(&ed)}
	if ed.PublishDate != nil {
		metadata["publicationDate"] = ed.PublishDate.Format("2006-01-02")
	}
	return models.CalibreIdentityEvidence{
		Key: identityKey(obs.Provider, obs.Method, obs.Seed, ed.ForeignID), BookID: book.ID,
		CalibreID: cb.CalibreID, CanonicalIdentity: identityRootKey(book), Provider: obs.Provider,
		ForeignID: book.ForeignID, EditionID: ed.ForeignID, Method: obs.Method, Seed: obs.Seed,
		ProvenanceGroup: identityRootKey(book), Status: models.CalibreIdentityRoot,
		WorkConfidence: "exact", EditionConfidence: "unresolved",
		NormalizedIdentifiers: ids, ProviderMetadata: metadata, CheckedAt: checked,
	}
}

func identityAddISBN(ids map[string][]string, raw string) {
	if isbn := isbnutil.ToISBN13(raw); isbn != "" && !slices.Contains(ids["isbn"], isbn) {
		ids["isbn"] = append(ids["isbn"], isbn)
	}
}

// CWA identifiers are compared only after all discovery observations have
// been classified. Neither this function nor DiscoverRawBookEvidence takes a
// CWA identifier as a lookup seed, and multiple CWA claims add zero votes.
func identityClaims(evidence []models.CalibreIdentityEvidence, cb *CalibreBook) []models.CalibreIdentityClaim {
	claims := calibreAuditIdentifiers(cb)
	types := make([]string, 0, len(claims))
	for typ := range claims {
		types = append(types, typ)
	}
	slices.Sort(types)
	out := make([]models.CalibreIdentityClaim, 0, len(types))
	for _, typ := range types {
		for _, raw := range claims[typ] {
			val := auditIdentifier(typ, raw.Value)
			if val == "" {
				continue
			}
			claim := models.CalibreIdentityClaim{IdentifierType: typ, IdentifierValue: raw.Value,
				Status: models.CalibreIdentityClaimUnverified}
			var expected map[string]bool
			var eligible []models.CalibreIdentityEvidence
			for _, e := range evidence {
				if e.Status != models.CalibreIdentityRoot && e.Status != models.CalibreIdentityCorroborated {
					continue
				}
				if _, ok := e.NormalizedIdentifiers[typ]; !ok {
					continue
				}
				eligible = append(eligible, e)
				if expected == nil {
					expected = make(map[string]bool)
				}
				for _, v := range e.NormalizedIdentifiers[typ] {
					expected[v] = true
				}
			}
			for _, e := range eligible {
				if slices.Contains(e.NormalizedIdentifiers[typ], val) {
					claim.Status, claim.EvidenceKey = models.CalibreIdentityClaimAgrees, e.Key
					break
				}
			}
			// Provider work IDs are unique within their catalogue. An explicit
			// incompatible CWA work ID can conflict with a unique rooted record;
			// edition ISBNs/IDs are never treated as exclusive to one work.
			if claim.Status == models.CalibreIdentityClaimUnverified && typ != "isbn" && typ != "asin" &&
				typ != "openlibrary_edition" && len(expected) == 1 {
				claim.Status = models.CalibreIdentityClaimConflicts
			}
			out = append(out, claim)
		}
	}
	return out
}
