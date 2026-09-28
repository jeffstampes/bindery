package calibre

import (
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

func editionResolutionFixture() (models.CalibreIdentitySnapshot, *CalibreBook) {
	first, second := "9780306406157", "9781861972712"
	book := &models.Book{ID: 7, ForeignID: "OL100W", MetadataProvider: "openlibrary"}
	cb := &CalibreBook{CalibreID: 42, Identifiers: map[string]string{}}
	raw := metadata.RawBookDiscovery{CanonicalProvider: "openlibrary", CanonicalForeignID: "OL100W",
		Observations: []metadata.RawBookObservation{
			{Provider: "openlibrary", Method: metadata.RawMethodExactBook, Seed: "OL100W", Outcome: metadata.RawOutcomeFound,
				Book: &models.Book{ForeignID: "OL100W", Title: "Work", Author: &models.Author{Name: "Author"}}},
			{Provider: "openlibrary", Method: metadata.RawMethodExactEditions, Seed: "OL100W", Outcome: metadata.RawOutcomeFound,
				Editions: []models.Edition{
					{ForeignID: "OL40M", ISBN13: &first, Title: "First edition", Language: "en", Format: "EPUB", IsEbook: true},
					{ForeignID: "OL41M", ISBN13: &second, Title: "Second edition", Language: "fr", Format: "EPUB", IsEbook: true},
				}},
		}}
	return buildIdentitySnapshot(book, cb, raw), cb
}

func observedISBN(isbns ...string) models.CalibreArtifactScan {
	ids := make([]models.CalibreArtifactIdentifier, 0, len(isbns))
	for _, isbn := range isbns {
		ids = append(ids, models.CalibreArtifactIdentifier{NormalizedValue: isbn, Status: "matches_work", Source: "epub_opf"})
	}
	return models.CalibreArtifactScan{BookID: 7, CalibreID: 42, Format: "EPUB", FileName: "book",
		SHA256: "a", Outcome: "scanned", Lineage: "pre_writeback", AttestedOriginal: true, Identifiers: ids}
}

func TestResolveOwnedEditionExactArtifactAndIndependentWorkConfidence(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	snapshot.Artifacts = []models.CalibreArtifactScan{observedISBN("9780306406157")}
	resolution := ResolveOwnedEdition(snapshot, cb)
	if resolution.Confidence != "exact" || resolution.ReasonCode != models.CalibreEditionReasonOriginalFile || resolution.EditionID != "OL40M" || len(resolution.Candidates) != 2 || resolution.Reason == "" {
		t.Fatalf("unique pre-write-back artifact: %+v", resolution)
	}
	for _, e := range snapshot.Evidence {
		if e.EditionConfidence != "unresolved" || e.WorkConfidence != "exact" {
			t.Fatalf("resolver changed provider work/edition evidence: %+v", e)
		}
	}
	if snapshot.RootKey != "openlibrary:OL100W" {
		t.Fatalf("artifact redirected work: %s", snapshot.RootKey)
	}
}

func TestResolveOwnedEditionRelinkRejectsOldOwnershipScan(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	scan := observedISBN("9780306406157")
	scan.CalibreID = cb.CalibreID + 1 // old linked file still appears in the bulk inventory
	snapshot.Artifacts = []models.CalibreArtifactScan{scan}
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" || got.EditionID != "" {
		t.Fatalf("old ownership scan selected a new owned edition: %+v", got)
	}
	// Even direct callers of assessArtifactScans cannot turn the old scan into
	// a selected edition when the new Calibre book is now the active link.
	assessArtifactScans(&snapshot, snapshot.Artifacts, cb, "", nil)
	if got := ResolveOwnedEdition(snapshot, cb); got.EditionID != "" {
		t.Fatalf("assessed old-link scan selected new owner's edition: %+v", got)
	}
}

func TestResolveOwnedEditionTruncatedProviderListPreservesCandidates(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	snapshot.Artifacts = []models.CalibreArtifactScan{observedISBN("9780306406157")}
	cb.Identifiers["openlibrary_edition"] = "OL40M"
	for i := range snapshot.Lookups {
		if snapshot.Lookups[i].Method == metadata.RawMethodExactEditions {
			snapshot.Lookups[i].Outcome = models.CalibreIdentityLookupTruncated
		}
	}
	got := ResolveOwnedEdition(snapshot, cb)
	if got.Confidence != "ambiguous" || got.ReasonCode != models.CalibreEditionReasonIncomplete || got.EditionID != "" || len(got.Candidates) != 2 ||
		!strings.Contains(got.Reason, "incomplete") {
		t.Fatalf("truncated exact-editions list asserted uniqueness: %+v", got)
	}
}

func TestResolveOwnedEditionMultipleCandidatesAndCorrelatedClaims(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" || got.EditionID != "" || len(got.Candidates) != 2 {
		t.Fatalf("first provider result must not win: %+v", got)
	}
	cb.Identifiers["openlibrary_edition"] = "OL40M"
	cb.Identifiers["isbn"] = "9780306406157"
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "high" || got.EditionID != "OL40M" ||
		!strings.Contains(got.Reason, "one claim") || len(got.Candidates[0].Reasons) != 1 ||
		got.Candidates[0].Reasons[0] != "CWA claims (one correlated source)" ||
		len(got.Candidates[0].Claims) != 2 ||
		got.Candidates[0].Claims[0] != (models.CalibreEditionClaim{Type: "openlibrary_edition", Value: "OL40M"}) ||
		got.Candidates[0].Claims[1] != (models.CalibreEditionClaim{Type: "isbn", Value: "9780306406157"}) {
		t.Fatalf("correlated CWA claims must remain structured without changing confidence: %+v", got)
	}
	cb.Identifiers["isbn"] = "9781861972712"
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" || got.EditionID != "" {
		t.Fatalf("conflicting native ID and ISBN selected an edition: %+v", got)
	}
}

func TestResolveOwnedEditionMultipleISBNAndWrongWork(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	snapshot.Artifacts = []models.CalibreArtifactScan{observedISBN("9780306406157", "9781861972712")}
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" || got.EditionID != "" {
		t.Fatalf("multiple ISBNs from one file cast competing votes: %+v", got)
	}
	snapshot.Artifacts[0] = observedISBN("9780306406157", "9780140328721")
	snapshot.Artifacts[0].Identifiers[1].Status = "conflict"
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" || got.EditionID != "" ||
		!strings.Contains(got.Reason, "wrong-work") || snapshot.RootKey != "openlibrary:OL100W" {
		t.Fatalf("wrong-work artifact redirected or selected the canonical work: %+v", got)
	}
}

func TestResolveOwnedEditionPostAndUnknownNotIndependent(t *testing.T) {
	for _, lineage := range []string{"unknown", "potentially_cwa_derived"} {
		t.Run(lineage, func(t *testing.T) {
			snapshot, cb := editionResolutionFixture()
			scan := observedISBN("9780306406157")
			scan.Lineage = lineage
			snapshot.Artifacts = []models.CalibreArtifactScan{scan}
			if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" || got.EditionID != "" {
				t.Fatalf("unproven artifact independence selected edition: %+v", got)
			}
			cb.Identifiers["isbn"] = "9780306406157"
			if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "high" || got.EditionID != "OL40M" {
				t.Fatalf("CWA claim alone must remain high (never exact): %+v", got)
			}
		})
	}
}

func TestResolveOwnedEditionSharedCWAClaimAndIndependentFile(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	snapshot.Evidence[2].NormalizedIdentifiers["isbn"] = []string{"9780306406157", "9781861972712"}
	cb.Identifiers["isbn"] = "9780306406157"
	// The CWA identifier is shared, but a distinct original-file ISBN
	// narrows the owned edition without treating CWA as two votes.
	snapshot.Artifacts = []models.CalibreArtifactScan{observedISBN("9781861972712")}
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "exact" || got.EditionID != "OL41M" {
		t.Fatalf("independent artifact should disambiguate shared CWA claim: %+v", got)
	}
}

func TestResolveOwnedEditionDerivedFileConflictsWithoutCorroboratingCWA(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	cb.Identifiers["isbn"] = "9780306406157"
	scan := observedISBN("9781861972712")
	scan.Lineage = "potentially_cwa_derived"
	scan.AttestedOriginal = false
	snapshot.Artifacts = []models.CalibreArtifactScan{scan}
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" || got.EditionID != "" {
		t.Fatalf("different derived-file ISBN must not silently select CWA's edition: %+v", got)
	}
	snapshot.Artifacts = []models.CalibreArtifactScan{observedISBN("9780306406157", "9781861972712")}
	snapshot.Artifacts[0].Lineage, snapshot.Artifacts[0].AttestedOriginal = "unknown", false
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" || got.EditionID != "" {
		t.Fatalf("multiple live-file ISBNs must remain ambiguous even with a CWA claim: %+v", got)
	}
}

func TestResolveOwnedEditionNativeIDNarrowsSharedISBNWithoutDoubleCounting(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	snapshot.Evidence[2].NormalizedIdentifiers["isbn"] = []string{"9780306406157"}
	cb.Identifiers["isbn"], cb.Identifiers["openlibrary_edition"] = "9780306406157", "OL40M"
	got := ResolveOwnedEdition(snapshot, cb)
	if got.Confidence != "high" || got.EditionID != "OL40M" || !strings.Contains(got.Reason, "correlated") {
		t.Fatalf("native edition ID did not narrow shared ISBN conservatively: %+v", got)
	}
	cb.Identifiers["openlibrary_edition"] = "OL41M"
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "high" || got.EditionID != "OL41M" {
		t.Fatalf("native edition ID should select the other shared-ISBN edition: %+v", got)
	}
	cb.Identifiers["openlibrary_edition"] = "OL99M"
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" || got.EditionID != "" {
		t.Fatalf("wrong-work native edition ID silently selected an edition: %+v", got)
	}
}

func TestResolveOwnedEditionDistinctCWAISBNsNeverBecomeSharedClaim(t *testing.T) {
	for _, field := range []string{"publisher", "language", "year"} {
		t.Run(field, func(t *testing.T) {
			snapshot, cb := editionResolutionFixture()
			// Calibre can expose two raw identifier keys that normalize to
			// ISBN. They are different values, each naming a different edition.
			cb.Identifiers["isbn"] = "9780306406157"
			cb.Identifiers["ISBN"] = "9781861972712"
			snapshot.Evidence[1].ProviderMetadata["publisher"] = "Ace Books"
			snapshot.Evidence[2].ProviderMetadata["publisher"] = "Other Press"
			snapshot.Evidence[1].ProviderMetadata["publicationDate"] = "2017-01-01"
			snapshot.Evidence[2].ProviderMetadata["publicationDate"] = "2019-01-01"
			switch field {
			case "publisher":
				cb.Publisher = "Ace Books"
			case "language":
				cb.Language = "eng"
			case "year":
				date := time.Date(2017, 1, 1, 0, 0, 0, 0, time.UTC)
				cb.PublishDate = &date
			}
			got := ResolveOwnedEdition(snapshot, cb)
			if got.Confidence != "ambiguous" || got.EditionID != "" || len(got.Candidates) != 2 ||
				len(got.Candidates[0].Reasons) == 0 || len(got.Candidates[1].Reasons) == 0 ||
				len(got.Candidates[0].Claims) != 1 || len(got.Candidates[1].Claims) != 1 ||
				got.Candidates[0].Claims[0] != (models.CalibreEditionClaim{Type: "isbn", Value: "9780306406157"}) ||
				got.Candidates[1].Claims[0] != (models.CalibreEditionClaim{Type: "isbn", Value: "9781861972712"}) {
				t.Fatalf("distinct ISBN claims were treated as one shared claim: %+v", got)
			}
		})
	}
}

func TestResolveOwnedEditionOverlappingDistinctISBNClaimsRemainAmbiguous(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	// One ISBN is shared and the second belongs only to B. Neither an
	// intersection of type names nor a coincidental intersection of matches
	// proves which ISBN identifies the owned copy.
	snapshot.Evidence[1].NormalizedIdentifiers["isbn"] = []string{"9780306406157"}
	snapshot.Evidence[2].NormalizedIdentifiers["isbn"] = []string{"9780306406157", "9781861972712"}
	cb.Identifiers["isbn"], cb.Identifiers["ISBN"] = "9780306406157", "9781861972712"
	cb.Language = "fre"
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" || got.EditionID != "" {
		t.Fatalf("overlapping distinct ISBN values fabricated a shared claim: %+v", got)
	}
}

func TestResolveOwnedEditionEquivalentISBNSpellingsDeduplicate(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	snapshot.Evidence[2].NormalizedIdentifiers["isbn"] = []string{"9780306406157"}
	cb.Identifiers["isbn"], cb.Identifiers["ISBN"] = "9780306406157", "0-306-40615-2"
	cb.Publisher = "Ace Books"
	snapshot.Evidence[1].ProviderMetadata["publisher"] = "Ace Books"
	snapshot.Evidence[2].ProviderMetadata["publisher"] = "Other Press"
	got := ResolveOwnedEdition(snapshot, cb)
	if got.Confidence != "high" || got.EditionID != "OL40M" ||
		len(got.Candidates) != 2 || len(got.Candidates[0].Reasons) != 1 ||
		len(got.Candidates[0].Claims) != 1 ||
		got.Candidates[0].Claims[0] != (models.CalibreEditionClaim{Type: "isbn", Value: "9780306406157"}) {
		t.Fatalf("equivalent ISBN-10/13 spellings became competing claims: %+v", got)
	}
}

func TestResolveOwnedEditionCompleteSoftFieldWithNoMatchIsConflict(t *testing.T) {
	for _, field := range []string{"publisher", "language", "year"} {
		t.Run(field, func(t *testing.T) {
			snapshot, cb := editionResolutionFixture()
			snapshot.Evidence[2].NormalizedIdentifiers["isbn"] = []string{"9780306406157"}
			cb.Identifiers["isbn"] = "9780306406157"
			snapshot.Evidence[1].ProviderMetadata["publisher"] = "Ace Books"
			snapshot.Evidence[2].ProviderMetadata["publisher"] = "Other Press"
			snapshot.Evidence[1].ProviderMetadata["publicationDate"] = "2017-01-01"
			snapshot.Evidence[2].ProviderMetadata["publicationDate"] = "2019-01-01"
			switch field {
			case "publisher":
				cb.Publisher = "Neither Press"
				cb.Language = "eng" // a different complete field selects A
			case "language":
				cb.Publisher = "Ace Books"
				cb.Language = "deu"
			case "year":
				cb.Publisher = "Ace Books"
				date := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
				cb.PublishDate = &date
			}
			got := ResolveOwnedEdition(snapshot, cb)
			label := field
			switch field {
			case "publisher":
				label = "publisher/imprint"
			case "year":
				label = "publication year"
			}
			if got.Confidence != "ambiguous" || got.EditionID != "" ||
				!strings.Contains(got.Reason, label) {
				t.Fatalf("complete %s evidence contradicts all editions: %+v", field, got)
			}
		})
	}
}

func TestResolveOwnedEditionPublisherTieBreakAndCorrelatedConflict(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	snapshot.Evidence[2].NormalizedIdentifiers["isbn"] = []string{"9780306406157"}
	snapshot.Evidence[1].ProviderMetadata["publisher"] = "Ace Books"
	snapshot.Evidence[2].ProviderMetadata["publisher"] = "Other Press"
	cb.Identifiers["isbn"], cb.Publisher = "9780306406157", "Ace Books"
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "high" || got.EditionID != "OL40M" ||
		got.ReasonCode != models.CalibreEditionReasonCalibreFields || !strings.Contains(got.Reason, "publisher") {
		t.Fatalf("owned publisher did not disambiguate shared ISBN: %+v", got)
	}
	cb.Language = "fre"
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" || got.EditionID != "" {
		t.Fatalf("correlated publisher/language conflict chose edition: %+v", got)
	}
	cb.Language = ""
	delete(snapshot.Evidence[2].ProviderMetadata, "publisher")
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" || got.EditionID != "" {
		t.Fatalf("missing competing publisher invented unique match: %+v", got)
	}
}

func TestResolveOwnedEditionSharedISBNUsesEditionLanguageNotWorkDate(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	snapshot.Evidence[2].NormalizedIdentifiers["isbn"] = []string{"9780306406157"}
	cb.Identifiers["isbn"] = "9780306406157"
	cb.Language = "eng"
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "high" || got.EditionID != "OL40M" ||
		!strings.Contains(got.Reason, "language") {
		t.Fatalf("shared ISBN and edition language did not resolve conservatively: %+v", got)
	}
	cb.Language = ""
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" {
		t.Fatalf("work release date or missing language fabricated selection: %+v", got)
	}
	cb.PublishDate = &time.Time{}
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" {
		t.Fatalf("unknown owned publication year fabricated selection: %+v", got)
	}
	first := time.Date(2017, 1, 1, 0, 0, 0, 0, time.UTC)
	cb.PublishDate = &first
	snapshot.Evidence[1].ProviderMetadata["publicationDate"] = "2017-01-01"
	snapshot.Evidence[2].ProviderMetadata["publicationDate"] = "2019-01-01"
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "high" || got.EditionID != "OL40M" ||
		!strings.Contains(got.Reason, "publication year") {
		t.Fatalf("edition year did not distinguish shared ISBN: %+v", got)
	}
	cb.Language = "fre"
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" || got.EditionID != "" {
		t.Fatalf("conflicting CWA language and date selected an edition: %+v", got)
	}
}

func TestResolveOwnedEditionHistoricalPreWritebackLimitedSupport(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	past := observedISBN("9780306406157")
	past.FilePath, past.Lineage, past.Historical = "book.epub", "pre_writeback", true
	current := past
	current.Historical, current.Lineage = false, "potentially_cwa_derived"
	snapshot.ArtifactHistory = []models.CalibreArtifactScan{past}
	snapshot.Artifacts = []models.CalibreArtifactScan{current}
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "high" || got.ReasonCode != models.CalibreEditionReasonHistoricFile || got.EditionID != "OL40M" ||
		!strings.Contains(got.Reason, "historical") {
		t.Fatalf("pre-write-back observation lost its distinct limited provenance: %+v", got)
	}
	current.FilePath = "replacement.epub"
	snapshot.Artifacts = []models.CalibreArtifactScan{current}
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" || got.EditionID != "" {
		t.Fatalf("old file observation applied to replacement: %+v", got)
	}
}

func TestResolveOwnedEditionRejectsCrossProviderCandidate(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	wrong := snapshot.Evidence[1]
	wrong.Provider, wrong.EditionID, wrong.Status = "googlebooks", "gb:wrong", models.CalibreIdentityConflict
	wrong.NormalizedIdentifiers["isbn"] = []string{"9780140328721"}
	snapshot.Evidence = append(snapshot.Evidence, wrong)
	snapshot.Artifacts = []models.CalibreArtifactScan{observedISBN("9780140328721")}
	snapshot.Artifacts[0].Identifiers[0].Status = "conflict"
	if got := ResolveOwnedEdition(snapshot, cb); got.Confidence != "ambiguous" || got.EditionID != "" ||
		len(got.Candidates) != 2 {
		t.Fatalf("cross-provider/wrong-work candidate became owned edition: %+v", got)
	}
}

// The category describes the resolver branch, not a second interpretation of
// the snapshot's raw, potentially contradictory observations.
func TestResolveOwnedEditionPresentationReason(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	snapshot.Artifacts = []models.CalibreArtifactScan{observedISBN("9780306406157")}
	cb.Identifiers["isbn"] = "9780306406157"
	if got := ResolveOwnedEdition(snapshot, cb); got.ReasonCode != "independent_file_isbn" || got.Confidence != "exact" {
		t.Fatalf("independent file should establish exact edition: %+v", got)
	}
	for i := range snapshot.Lookups {
		if snapshot.Lookups[i].Method == metadata.RawMethodExactEditions {
			snapshot.Lookups[i].Outcome = models.CalibreIdentityLookupTruncated
		}
	}
	snapshot.Artifacts = []models.CalibreArtifactScan{observedISBN("9780306406157", "9781861972712")}
	if got := ResolveOwnedEdition(snapshot, cb); got.ReasonCode != "lookup_incomplete" || got.Confidence != "ambiguous" {
		t.Fatalf("incomplete lookup wins before conflicting evidence: %+v", got)
	}
	for i := range snapshot.Lookups {
		if snapshot.Lookups[i].Method == metadata.RawMethodExactEditions {
			snapshot.Lookups[i].Outcome = models.CalibreIdentityLookupAnswered
		}
	}
	if got := ResolveOwnedEdition(snapshot, cb); got.ReasonCode != "conflicting_evidence" || got.Confidence != "ambiguous" {
		t.Fatalf("complete lookup with competing ISBNs: %+v", got)
	}
	scan := observedISBN("9780306406157")
	scan.Lineage, scan.AttestedOriginal = "potentially_cwa_derived", false
	snapshot.Artifacts = []models.CalibreArtifactScan{scan}
	if got := ResolveOwnedEdition(snapshot, cb); got.ReasonCode != "calibre_identifiers" ||
		got.Confidence != "high" || got.ArtifactWarning != "possibly_calibre_derived" {
		t.Fatalf("CWA claim, with a non-independent file observation: %+v", got)
	}
	delete(cb.Identifiers, "isbn")
	if got := ResolveOwnedEdition(snapshot, cb); got.ReasonCode != "multiple_editions" || got.Confidence != "ambiguous" {
		t.Fatalf("non-independent observation selected an edition: %+v", got)
	}
}

func TestArtifactLineageTemporalBoundary(t *testing.T) {
	before := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	event := models.CalibreArtifactWriteback{BookID: 7, CalibreID: 42, WrittenAt: before.Add(time.Hour)}
	for _, tt := range []struct {
		at   time.Time
		want string
	}{
		{before, "pre_writeback"},
		{event.WrittenAt, "potentially_cwa_derived"},
		{event.WrittenAt.Add(time.Hour), "potentially_cwa_derived"},
	} {
		scan := observedISBN("9780306406157")
		scan.ScannedAt = tt.at
		classifyArtifactLineage(&scan, []models.CalibreArtifactWriteback{event})
		if scan.Lineage != tt.want {
			t.Fatalf("at %s: lineage=%s want %s", tt.at, scan.Lineage, tt.want)
		}
	}
	unknown := observedISBN("9780306406157")
	unknown.AttestedOriginal = false
	classifyArtifactLineage(&unknown, nil)
	if unknown.Lineage != "unknown" {
		t.Fatalf("no event is not independent: %+v", unknown)
	}
}

func TestAuditResolvedEditionOverridesAmbiguousWorkEditions(t *testing.T) {
	snapshot, cb := editionResolutionFixture()
	book, owned, ref, series := testAuditComparison()
	book.ID, book.ForeignID = snapshot.BookID, "OL100W"
	book.Editions = []models.Edition{
		{ForeignID: "OL40M", Title: "First edition", Format: "EPUB", IsEbook: true},
		{ForeignID: "OL41M", Title: "Second edition", Format: "EPUB", IsEbook: true},
	}
	owned.CalibreID = cb.CalibreID
	ref.CalibreID = cb.CalibreID
	owned.Title = "First edition"
	pub := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	other := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	snapshot.Evidence[1].ProviderMetadata["publicationDate"] = pub.Format("2006-01-02")
	snapshot.Evidence[2].ProviderMetadata["publicationDate"] = other.Format("2006-01-02")
	owned.PublishDate = &other
	snapshot.Artifacts = []models.CalibreArtifactScan{observedISBN("9780306406157")}
	snapshot.Edition = ResolveOwnedEdition(snapshot, cb)
	outcomes := compareAuditBookWithIdentity(book, owned, ref, series, snapshot)
	if title := outcomes[auditFieldKey{models.CalibreAuditFieldTitle, ""}]; title.different ||
		title.finding.BinderyEvidence[0].ForeignID != "OL40M" {
		t.Fatalf("audit used arbitrary work title instead of selected edition: %+v", title)
	}
	if date, ok := outcomes[auditFieldKey{models.CalibreAuditFieldPubDate, ""}]; !ok || !date.different ||
		date.finding.BinderyEvidence[0].ForeignID != "OL40M" {
		t.Fatalf("audit did not compare selected edition date: %+v", date)
	}
	snapshot.Edition = models.CalibreEditionResolution{Confidence: "ambiguous"}
	if _, ok := compareAuditBookWithIdentity(book, owned, ref, series, snapshot)[auditFieldKey{models.CalibreAuditFieldPubDate, ""}]; ok {
		t.Fatal("ambiguous edition must not compare publication date")
	}
}
