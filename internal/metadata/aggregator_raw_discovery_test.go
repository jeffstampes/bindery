package metadata

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func rawObservation(t *testing.T, result RawBookDiscovery, provider, method, seed string) RawBookObservation {
	t.Helper()
	for _, obs := range result.Observations {
		if obs.Provider == provider && obs.Method == method && obs.Seed == seed {
			return obs
		}
	}
	t.Fatalf("missing %s %s %s: %+v", provider, method, seed, result.Observations)
	return RawBookObservation{}
}

func TestDiscoverRawBookEvidenceRootOnlySeedsAndUnmergedCandidates(t *testing.T) {
	root := &mockProvider{name: "openlibrary", getBook: &models.Book{
		ForeignID: "OL1W", Title: "The Book", Author: &models.Author{Name: "Jane Doe"},
		ProviderISBNs: []string{"0-306-40615-2", "9780306406157", "9780306406158", "bad"},
	}, getEditions: []models.Edition{{ForeignID: "OL1M", ISBN13: rawString("9780140328721")}}}
	other := &mockProvider{name: "hardcover", getByISBNByISBN: map[string]*models.Book{
		"9780306406157": {ForeignID: "hc:10", Title: "The Book", ProviderISBNs: []string{"9789999999995"}},
		"9780140328721": {ForeignID: "hc:11", Title: "The Book"},
	}}
	result := newTestAggregator(root, other).DiscoverRawBookEvidence(context.Background(), "ol", "OL1W")
	if root.getBookCalls != 1 || !reflect.DeepEqual(root.gotBookIDs, []string{"OL1W"}) {
		t.Fatalf("root exact calls: %v", root.gotBookIDs)
	}
	if !reflect.DeepEqual(result.ISBNSeeds, []RawISBNSeed{
		{ISBN: "9780306406157", Sources: []string{"root_book"}},
		{ISBN: "9780140328721", Sources: []string{"root_edition:OL1M"}},
	}) {
		t.Fatalf("seed provenance/validation: %+v", result.ISBNSeeds)
	}
	if obs := rawObservation(t, result, "openlibrary", RawMethodExactBook, "OL1W"); obs.Outcome != RawOutcomeFound || obs.Book == nil {
		t.Fatalf("root book: %+v", obs)
	}
	if obs := rawObservation(t, result, "openlibrary", RawMethodExactEditions, "OL1W"); obs.Outcome != RawOutcomeFound || len(obs.Editions) != 1 {
		t.Fatalf("root editions: %+v", obs)
	}
	if obs := rawObservation(t, result, "hardcover", RawMethodISBN, "9780306406157"); obs.Outcome != RawOutcomeFound || obs.Book.ForeignID != "hc:10" {
		t.Fatalf("first candidate: %+v", obs)
	}
	if obs := rawObservation(t, result, "hardcover", RawMethodISBN, "9780140328721"); obs.Outcome != RawOutcomeFound || obs.Book.ForeignID != "hc:11" {
		t.Fatalf("second candidate: %+v", obs)
	}
	if len(other.searchBookQueries) != 0 || other.getBookCalls != 0 {
		t.Fatalf("ISBN hits should not trigger search/expansion: search=%v book=%v", other.searchBookQueries, other.gotBookIDs)
	}
}

func rawString(s string) *string { return &s }

func TestDiscoverRawBookEvidenceConservativeFallbackAndPartialOutcomes(t *testing.T) {
	root := &mockProvider{name: "openlibrary", getBook: &models.Book{ForeignID: "OL1W", Title: "The Book", Author: &models.Author{Name: "Doe, Jane"}}, getEditionsErr: errors.New("editions offline")}
	good := &mockProvider{name: "googlebooks", searchBooks: []models.Book{
		{ForeignID: "gb:1", Title: "The Book", Author: &models.Author{Name: "Jane Doe"}},
		{ForeignID: "gb:2", Title: "The Book", Author: &models.Author{Name: "Jane Doe"}},
		{ForeignID: "gb:3", Title: "The Book Summary", Author: &models.Author{Name: "Jane Doe"}},
		{ForeignID: "gb:4", Title: "The Book", Author: &models.Author{Name: "Someone Else"}},
	}}
	missing := &mockProvider{name: "hardcover", searchBookErr: ErrProviderNotConfigured}
	bad := &mockProvider{name: "dnb", searchBookErr: errors.New("upstream failed")}
	result := newTestAggregator(root, good, missing, bad).DiscoverRawBookEvidence(context.Background(), "openlibrary", "OL1W")
	if len(result.ISBNSeeds) != 0 {
		t.Fatalf("unexpected seeds: %+v", result.ISBNSeeds)
	}
	if obs := rawObservation(t, result, "openlibrary", RawMethodExactEditions, "OL1W"); obs.Outcome != RawOutcomeFailed || obs.Err == nil {
		t.Fatalf("edition failure lost: %+v", obs)
	}
	found := rawObservation(t, result, "googlebooks", RawMethodTitleAuthor, "The Book Jane Doe")
	if found.Outcome != RawOutcomeFound || len(found.Candidates) != 2 || found.Candidates[0].ForeignID != "gb:1" || found.Candidates[1].ForeignID != "gb:2" ||
		found.RejectedCount != 2 || len(found.RejectedCandidates) != 2 || found.RejectedCandidates[0].ForeignID != "gb:3" {
		t.Fatalf("filtered candidates should retain distinct exact matches and disjoint diagnostics: %+v", found)
	}
	if obs := rawObservation(t, result, "hardcover", RawMethodTitleAuthor, "The Book Jane Doe"); obs.Outcome != RawOutcomeUnconfigured {
		t.Fatalf("not configured: %+v", obs)
	}
	if obs := rawObservation(t, result, "dnb", RawMethodTitleAuthor, "The Book Jane Doe"); obs.Outcome != RawOutcomeFailed || obs.Err == nil {
		t.Fatalf("provider failure: %+v", obs)
	}
}

func TestDiscoverRawBookEvidenceSeedAndCandidateCaps(t *testing.T) {
	root := &mockProvider{name: "openlibrary", getBook: &models.Book{ForeignID: "OL1W", Title: "Title", Author: &models.Author{Name: "Author"}, ProviderISBNs: []string{"9780306406157", "9780140328721", "9780061120084", "9780743273565"}}}
	other := &mockProvider{name: "dnb", searchBooks: make([]models.Book, rawDiscoveryMaxCandidates+3)}
	for i := range other.searchBooks {
		other.searchBooks[i] = models.Book{ForeignID: "dnb:1", Title: "Title", Author: &models.Author{Name: "Author"}}
	}
	result := newTestAggregator(root, other).DiscoverRawBookEvidence(context.Background(), "openlibrary", "OL1W")
	if len(result.ISBNSeeds) != rawDiscoveryMaxISBNSeeds || !result.SeedsTruncated {
		t.Fatalf("seed bound: %+v", result)
	}
	if len(other.gotISBNs) != rawDiscoveryMaxISBNSeeds {
		t.Fatalf("ISBN fanout was unbounded: %v", other.gotISBNs)
	}
	fallback := rawObservation(t, result, "dnb", RawMethodTitleAuthor, "Title Author")
	if len(fallback.Candidates) != rawDiscoveryMaxCandidates || !fallback.Truncated {
		t.Fatalf("candidate bound: %+v", fallback)
	}
}

func TestDiscoverRawBookEvidencePerProviderFanoutBound(t *testing.T) {
	var active, peak atomic.Int32
	root := &fanoutRawProvider{mockProvider: mockProvider{name: "openlibrary", getBook: &models.Book{
		ForeignID: "OL1W", ProviderISBNs: []string{"9780306406157"},
	}}, active: &active, peak: &peak}
	providers := []Provider{root}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		providers = append(providers, &fanoutRawProvider{mockProvider: mockProvider{name: name}, active: &active, peak: &peak})
	}
	result := newTestAggregator(providers[0], providers[1:]...).DiscoverRawBookEvidence(context.Background(), "openlibrary", "OL1W")
	if got := peak.Load(); got > rawDiscoveryMaxFanout || got < 2 {
		t.Fatalf("expected parallel calls capped at %d, saw %d", rawDiscoveryMaxFanout, got)
	}
	for _, p := range providers {
		if obs := rawObservation(t, result, p.Name(), RawMethodISBN, "9780306406157"); obs.Outcome != RawOutcomeEmpty {
			t.Fatalf("missing outcome for %s: %+v", p.Name(), obs)
		}
	}
}

type fanoutRawProvider struct {
	mockProvider
	active, peak *atomic.Int32
}

func (p *fanoutRawProvider) GetBookByISBN(ctx context.Context, _ string) (*models.Book, error) {
	n := p.active.Add(1)
	for prior := p.peak.Load(); n > prior; prior = p.peak.Load() {
		if p.peak.CompareAndSwap(prior, n) {
			break
		}
	}
	defer p.active.Add(-1)
	select {
	case <-time.After(15 * time.Millisecond):
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestDiscoverRawBookEvidenceEditionCapAndSeedProvenance(t *testing.T) {
	root := &mockProvider{name: "openlibrary", getBook: &models.Book{ForeignID: "OL1W", Title: "Title", Author: &models.Author{Name: "Author"}, ProviderISBNs: []string{"0-306-40615-2"}},
		getEditions: make([]models.Edition, rawDiscoveryMaxEditions+1)}
	root.getEditions[0] = models.Edition{ForeignID: "OL1M", ISBN13: rawString("9780306406157")}
	root.getEditions[1] = models.Edition{ForeignID: "OL2M", ISBN13: rawString("9780140328721")}
	root.getEditions[rawDiscoveryMaxEditions] = models.Edition{ForeignID: "OLlastM", ISBN13: rawString("9780061120084")}
	result := newTestAggregator(root).DiscoverRawBookEvidence(context.Background(), "openlibrary", "OL1W")
	obs := rawObservation(t, result, "openlibrary", RawMethodExactEditions, "OL1W")
	if len(obs.Editions) != rawDiscoveryMaxEditions || !obs.Truncated {
		t.Fatalf("root edition cap: %+v", obs)
	}
	if !reflect.DeepEqual(result.ISBNSeeds, []RawISBNSeed{
		{ISBN: "9780306406157", Sources: []string{"root_book", "root_edition:OL1M"}},
		{ISBN: "9780140328721", Sources: []string{"root_edition:OL2M"}},
	}) {
		t.Fatalf("unexpected seeds from capped edition list: %+v", result.ISBNSeeds)
	}
}

func TestDiscoverRawBookEvidenceNoUsableSeedReportsSkippedProviders(t *testing.T) {
	root := &mockProvider{name: "openlibrary", getBook: &models.Book{ForeignID: "OL1W", Title: "No author"}}
	other := &mockProvider{name: "hardcover"}
	result := newTestAggregator(root, other).DiscoverRawBookEvidence(context.Background(), "openlibrary", "OL1W")
	if obs := rawObservation(t, result, "hardcover", RawMethodTitleAuthor, ""); obs.Outcome != RawOutcomeSkipped {
		t.Fatalf("provider was not queried, not an empty result: %+v", obs)
	}
	if other.getByISBNCalls != 0 || len(other.searchBookQueries) != 0 {
		t.Fatalf("unexpected provider lookup: %+v", other)
	}
}

func TestDiscoverRawBookEvidenceMissingRootAndTimeout(t *testing.T) {
	root := &mockProvider{name: "openlibrary", getBook: nil, getEditions: nil}
	other := &mockProvider{name: "hardcover"}
	result := newTestAggregator(root, other).DiscoverRawBookEvidence(context.Background(), "openlibrary", "OL1W")
	if obs := rawObservation(t, result, "openlibrary", RawMethodExactBook, "OL1W"); obs.Outcome != RawOutcomeEmpty {
		t.Fatal(obs)
	}
	if len(other.gotISBNs) != 0 || len(other.searchBookQueries) != 0 {
		t.Fatalf("missing root must not discover from noncanonical data: %+v", other)
	}
	result = newTestAggregator(other).DiscoverRawBookEvidence(context.Background(), "openlibrary", "OL1W")
	if obs := rawObservation(t, result, "openlibrary", RawMethodExactBook, "OL1W"); obs.Outcome != RawOutcomeUnconfigured {
		t.Fatal(obs)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	blocking := &deadlineRawProvider{mockProvider: mockProvider{name: "openlibrary"}}
	start := time.Now()
	result = newTestAggregator(blocking).DiscoverRawBookEvidence(ctx, "openlibrary", "OL1W")
	if time.Since(start) > time.Second {
		t.Fatal("discovery ignored context deadline")
	}
	if obs := rawObservation(t, result, "openlibrary", RawMethodExactBook, "OL1W"); obs.Outcome != RawOutcomeFailed || !errors.Is(obs.Err, context.DeadlineExceeded) {
		t.Fatalf("timeout should not be an empty result: %+v", obs)
	}
}

func TestDiscoverRawBookEvidenceDoesNotTrustMismatchedExactRootOrCachedMetadata(t *testing.T) {
	root := &mockProvider{name: "hardcover", getBook: &models.Book{
		ForeignID: "hc:wrong", Title: "Wrong", Author: &models.Author{Name: "Wrong Author"},
		ProviderISBNs: []string{"9780306406157"},
	}, getEditions: []models.Edition{{ForeignID: "hc:edition", ISBN13: rawString("9780140328721")}}}
	other := &mockProvider{name: "googlebooks"}
	a := newTestAggregator(root, other)
	a.cache.set("book:hc:original", &models.Book{ForeignID: "hc:original", Title: "Cached"})
	a.cache.set("editions:hc:original", []models.Edition{{ForeignID: "cached"}})
	result := a.DiscoverRawBookEvidence(context.Background(), "hardcover", "hc:original")
	if obs := rawObservation(t, result, "hardcover", RawMethodExactBook, "hc:original"); obs.Outcome != RawOutcomeConflict || obs.Book.ForeignID != "hc:wrong" {
		t.Fatalf("mismatched provider response must be retained, not trusted: %+v", obs)
	}
	if obs := rawObservation(t, result, "hardcover", RawMethodExactEditions, "hc:original"); obs.Editions[0].ForeignID != "hc:edition" {
		t.Fatalf("cache read instead of provider: %+v", obs)
	}
	if root.getBookCalls != 1 || len(result.ISBNSeeds) != 0 || len(other.gotISBNs) != 0 || len(other.searchBookQueries) != 0 {
		t.Fatalf("mismatched root must not expand: %+v", result)
	}
}

func TestDiscoverRawBookEvidenceUncooperativeProviderDeadline(t *testing.T) {
	root := &uncooperativeRawProvider{mockProvider: mockProvider{name: "openlibrary"}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	start := time.Now()
	result := newTestAggregator(root).DiscoverRawBookEvidence(ctx, "openlibrary", "OL1W")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("provider ignored context and held request for %s", elapsed)
	}
	if obs := rawObservation(t, result, "openlibrary", RawMethodExactBook, "OL1W"); obs.Outcome != RawOutcomeFailed || !errors.Is(obs.Err, context.DeadlineExceeded) {
		t.Fatalf("lost timeout: %+v", obs)
	}
}

type uncooperativeRawProvider struct{ mockProvider }

func (p *uncooperativeRawProvider) GetBook(_ context.Context, _ string) (*models.Book, error) {
	time.Sleep(80 * time.Millisecond)
	return nil, nil
}

type deadlineRawProvider struct{ mockProvider }

func (p *deadlineRawProvider) GetBook(ctx context.Context, _ string) (*models.Book, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
