package metadata

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// These limits apply to one canonical work. Discovery does not recurse into
// candidate ISBNs/editions: a provider's unrelated work must not become a seed.
const (
	rawDiscoveryMaxISBNSeeds  = 3
	rawDiscoveryMaxEditions   = 64
	rawDiscoveryMaxCandidates = 8
	rawDiscoveryMaxFanout     = 4
	rawDiscoveryTotalTimeout  = 20 * time.Second
	rawDiscoveryRootTimeout   = 5 * time.Second
	rawDiscoveryProviderTime  = 8 * time.Second
	rawDiscoveryCallTimeout   = 4 * time.Second
)

const (
	RawMethodExactBook     = "exact_book"
	RawMethodExactEditions = "exact_editions"
	RawMethodISBN          = "isbn"
	RawMethodTitleAuthor   = "title_author"

	RawOutcomeFound        = "found"
	RawOutcomeEmpty        = "empty"
	RawOutcomeFailed       = "failed"
	RawOutcomeUnconfigured = "unconfigured"
	RawOutcomeSkipped      = "skipped"
	RawOutcomeConflict     = "conflicting_root"
)

// RawISBNSeed is a checksum-valid ISBN-13 obtained only from the exact
// canonical provider response. Sources identify the root book or root edition
// that supplied it. Several ISBNs from one response are not independent votes.
type RawISBNSeed struct {
	ISBN    string
	Sources []string
}

// RawBookObservation is one provider call, not an assertion of identity.
// Book is the provider's unmodified GetBook/GetBookByISBN response; Editions
// are the canonical root's exact GetEditions response; Candidates are distinct
// search hits that passed strict title AND author comparison. Disjoint search
// hits are kept separately as RejectedCandidates (within the same cap), not
// linked or used to seed further calls. In particular, an ISBN hit is not
// promoted to a confirmed alias, even if its identifiers agree: the provider
// may have attached the ISBN to a different work.
// Err is retained for failed/unconfigured calls; empty and skipped are explicit.
type RawBookObservation struct {
	Provider           string
	Method             string
	Seed               string
	Outcome            string
	Book               *models.Book
	Editions           []models.Edition
	Candidates         []models.Book
	RejectedCandidates []models.Book // bounded disjoint search hits; never alias seeds
	RejectedCount      int           // total disjoint hits, including those truncated
	Truncated          bool
	Err                error
}

// RawBookDiscovery records independent, unmerged provider responses rooted in
// Bindery's canonical provider/id. No Calibre/CWA input, caching, enrichment,
// cross-provider alias resolution, persistence, or confidence decision occurs.
// A caller must independently validate candidates before persisting an alias;
// work and owned-edition identity are separate questions.
type RawBookDiscovery struct {
	CanonicalProvider  string
	CanonicalForeignID string
	ISBNSeeds          []RawISBNSeed
	SeedsTruncated     bool
	Observations       []RawBookObservation
}

// DiscoverRawBookEvidence resolves the exact canonical book and its editions,
// then asks every configured metadata provider about a small number of ISBNs
// found on those root responses. Providers without an ISBN hit receive one
// strict title+author search if the root supplied both. All calls, including
// misses, errors and unconfigured providers, remain in provider/seed order.
// A timeout is an incomplete observation, NEVER negative evidence. Providers
// are expected to honor context; a timed-out call is detached so an errant
// provider cannot hold the discovery request open indefinitely.
func (a *Aggregator) DiscoverRawBookEvidence(ctx context.Context, canonicalProvider, canonicalForeignID string) RawBookDiscovery {
	name := normalizedProviderName(canonicalProvider)
	id := strings.TrimSpace(canonicalForeignID)
	result := RawBookDiscovery{CanonicalProvider: name, CanonicalForeignID: id}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, rawDiscoveryTotalTimeout)
	defer cancel()

	var root Provider
	for _, p := range a.providers() {
		if p != nil && normalizedProviderName(p.Name()) == name {
			root = p
			break
		}
	}
	if name == "" || id == "" {
		result.Observations = append(result.Observations, RawBookObservation{
			Provider: name, Method: RawMethodExactBook, Seed: id,
			Outcome: RawOutcomeSkipped, Err: errors.New("canonical provider and id required"),
		})
		return result
	}
	if root == nil {
		for _, method := range []string{RawMethodExactBook, RawMethodExactEditions} {
			result.Observations = append(result.Observations, RawBookObservation{
				Provider: name, Method: method, Seed: id,
				Outcome: RawOutcomeUnconfigured, Err: ErrProviderNotConfigured,
			})
		}
		return result
	}

	bookCtx, bookCancel := context.WithTimeout(ctx, rawDiscoveryRootTimeout)
	book, bookErr := rawDiscoveryCall(bookCtx, func() (*models.Book, error) { return root.GetBook(bookCtx, id) })
	bookCancel()
	bookObs := RawBookObservation{Provider: name, Method: RawMethodExactBook, Seed: id, Book: book, Err: bookErr}
	bookObs.Outcome = rawDiscoveryOutcome(book != nil, bookErr)
	// An exact request which returns a different provider record must not
	// supply identity seeds, even when it came back without a transport error.
	if bookErr == nil && book != nil && !rawSameProviderID(name, id, book.ForeignID) {
		bookObs.Outcome = RawOutcomeConflict
	}
	result.Observations = append(result.Observations, bookObs)

	edCtx, edCancel := context.WithTimeout(ctx, rawDiscoveryRootTimeout)
	editions, edErr := rawDiscoveryCall(edCtx, func() ([]models.Edition, error) { return root.GetEditions(edCtx, id) })
	edCancel()
	edObs := RawBookObservation{Provider: name, Method: RawMethodExactEditions, Seed: id, Editions: editions, Err: edErr}
	edObs.Outcome = rawDiscoveryOutcome(len(editions) > 0, edErr)
	if len(editions) > rawDiscoveryMaxEditions {
		edObs.Editions = editions[:rawDiscoveryMaxEditions]
		edObs.Truncated = true
	}
	result.Observations = append(result.Observations, edObs)
	// If GetBook could not establish the canonical work, no ISBN or search
	// result may bootstrap its identity, even if GetEditions returned data.
	if bookObs.Outcome != RawOutcomeFound {
		return result
	}

	addSeed := func(raw, source string) {
		isbn := isbnutil.ToISBN13(raw)
		if isbn == "" {
			return
		}
		for i := range result.ISBNSeeds {
			if result.ISBNSeeds[i].ISBN == isbn {
				for _, existing := range result.ISBNSeeds[i].Sources {
					if existing == source {
						return
					}
				}
				result.ISBNSeeds[i].Sources = append(result.ISBNSeeds[i].Sources, source)
				return
			}
		}
		if len(result.ISBNSeeds) == rawDiscoveryMaxISBNSeeds {
			result.SeedsTruncated = true
			return
		}
		result.ISBNSeeds = append(result.ISBNSeeds, RawISBNSeed{ISBN: isbn, Sources: []string{source}})
	}
	for _, isbn := range book.ProviderISBNs {
		addSeed(isbn, "root_book")
	}
	// The GetBook record may carry its own edition list. These are still
	// canonical-provider observations, never ISBNs from a discovered candidate.
	for _, ed := range book.Editions {
		addSeed(rawEditionISBN(ed.ISBN13), "root_book_edition:"+ed.ForeignID)
		addSeed(rawEditionISBN(ed.ISBN10), "root_book_edition:"+ed.ForeignID)
	}
	if edErr == nil {
		for _, ed := range edObs.Editions {
			source := "root_edition:" + ed.ForeignID
			addSeed(rawEditionISBN(ed.ISBN13), source)
			addSeed(rawEditionISBN(ed.ISBN10), source)
		}
	}

	providers := a.providers()
	observations := make([][]RawBookObservation, len(providers))
	fanout := make(chan struct{}, rawDiscoveryMaxFanout)
	var wg sync.WaitGroup
	for i, p := range providers {
		if p == nil {
			continue
		}
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			pname := normalizedProviderName(p.Name())
			select {
			case fanout <- struct{}{}:
				defer func() { <-fanout }()
			case <-ctx.Done():
				if len(result.ISBNSeeds) == 0 {
					observations[i] = []RawBookObservation{{Provider: pname, Method: RawMethodTitleAuthor,
						Outcome: RawOutcomeSkipped, Err: ctx.Err()}}
				} else {
					for _, seed := range result.ISBNSeeds {
						observations[i] = append(observations[i], RawBookObservation{Provider: pname,
							Method: RawMethodISBN, Seed: seed.ISBN, Outcome: RawOutcomeSkipped, Err: ctx.Err()})
					}
				}
				return
			}
			pctx, stop := context.WithTimeout(ctx, rawDiscoveryProviderTime)
			defer stop()
			observations[i] = rawDiscoverProvider(pctx, p, result.ISBNSeeds, *book)
		}(i, p)
	}
	wg.Wait()
	for _, batch := range observations {
		result.Observations = append(result.Observations, batch...)
	}
	return result
}

func rawSameProviderID(provider, requested, returned string) bool {
	if returned == "" {
		return false
	}
	prefix := ""
	switch provider {
	case "hardcover":
		prefix = "hc:"
	case "googlebooks":
		prefix = "gb:"
	case "dnb":
		prefix = "dnb:"
	}
	return strings.TrimPrefix(requested, prefix) == strings.TrimPrefix(returned, prefix)
}

func rawEditionISBN(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func rawDiscoverProvider(ctx context.Context, p Provider, seeds []RawISBNSeed, root models.Book) []RawBookObservation {
	name := normalizedProviderName(p.Name())
	out := make([]RawBookObservation, 0, len(seeds)+1)
	hit, configured := false, true
	for _, seed := range seeds {
		obs := RawBookObservation{Provider: name, Method: RawMethodISBN, Seed: seed.ISBN}
		if err := ctx.Err(); err != nil {
			obs.Outcome, obs.Err = RawOutcomeSkipped, err
		} else {
			callCtx, cancel := context.WithTimeout(ctx, rawDiscoveryCallTimeout)
			obs.Book, obs.Err = rawDiscoveryCall(callCtx, func() (*models.Book, error) {
				return p.GetBookByISBN(callCtx, seed.ISBN)
			})
			cancel()
			obs.Outcome = rawDiscoveryOutcome(obs.Book != nil, obs.Err)
		}
		out = append(out, obs)
		if obs.Outcome == RawOutcomeFound {
			hit = true
		}
		if obs.Outcome == RawOutcomeUnconfigured {
			configured = false
			break
		}
	}
	if hit || !configured || ctx.Err() != nil {
		return out
	}
	if strings.TrimSpace(root.Title) == "" || bookAuthorName(root) == "" {
		if len(seeds) == 0 {
			out = append(out, RawBookObservation{Provider: name, Method: RawMethodTitleAuthor, Outcome: RawOutcomeSkipped})
		}
		return out
	}
	// Only run fallback for this provider when ISBN evidence did not find a
	// record. Exact equality of both folded fields is required; no fuzzy
	// title/author ranking and no use of another provider's ISBNs.
	title := strings.TrimSpace(root.Title)
	author := uninvertAuthorName(bookAuthorName(root))
	query := title + " " + author
	obs := RawBookObservation{Provider: name, Method: RawMethodTitleAuthor, Seed: query}
	callCtx, cancel := context.WithTimeout(ctx, rawDiscoveryCallTimeout)
	books, err := rawDiscoveryCall(callCtx, func() ([]models.Book, error) { return p.SearchBooks(callCtx, query) })
	cancel()
	obs.Err = err
	if err == nil {
		wantTitle := textutil.FoldForTitleMatch(title)
		wantAuthor := canonicalAuthorKey(author)
		for _, candidate := range books {
			if candidate.ForeignID == "" || wantTitle == "" || wantAuthor == "" ||
				textutil.FoldForTitleMatch(candidate.Title) != wantTitle ||
				canonicalAuthorKey(bookAuthorName(candidate)) != wantAuthor {
				obs.RejectedCount++
				continue
			}
			if len(obs.Candidates) == rawDiscoveryMaxCandidates {
				obs.Truncated = true
				continue
			}
			obs.Candidates = append(obs.Candidates, candidate)
		}
		// Preserve disjoint responses as raw diagnostic candidates, but never
		// allow them to displace a strict match under the candidate cap.
		for _, candidate := range books {
			if candidate.ForeignID != "" && wantTitle != "" && wantAuthor != "" &&
				textutil.FoldForTitleMatch(candidate.Title) == wantTitle &&
				canonicalAuthorKey(bookAuthorName(candidate)) == wantAuthor {
				continue
			}
			if len(obs.Candidates)+len(obs.RejectedCandidates) == rawDiscoveryMaxCandidates {
				obs.Truncated = true
				break
			}
			obs.RejectedCandidates = append(obs.RejectedCandidates, candidate)
		}
	}
	obs.Outcome = rawDiscoveryOutcome(len(obs.Candidates) > 0, err)
	return append(out, obs)
}

func rawDiscoveryOutcome(found bool, err error) string {
	if errors.Is(err, ErrProviderNotConfigured) {
		return RawOutcomeUnconfigured
	}
	if err != nil {
		return RawOutcomeFailed
	}
	if found {
		return RawOutcomeFound
	}
	return RawOutcomeEmpty
}

// rawDiscoveryCall does not wait on a client that ignores cancellation. Its
// result channel is buffered so a late provider response cannot block a worker.
func rawDiscoveryCall[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	type response struct {
		value T
		err   error
	}
	answer := make(chan response, 1)
	go func() {
		value, err := fn()
		answer <- response{value, err}
	}()
	select {
	case r := <-answer:
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		return r.value, r.err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
