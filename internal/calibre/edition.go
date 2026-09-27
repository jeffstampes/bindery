package calibre

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// classifyArtifactLineage never infers independence from a file's metadata.
// A reported write-back is book/link-scoped and conservatively covers all formats.
func classifyArtifactLineage(scan *models.CalibreArtifactScan, events []models.CalibreArtifactWriteback) {
	scan.Lineage = "unknown"
	if scan.AttestedOriginal {
		scan.Lineage = "pre_writeback" // explicit operator attestation
	}
	for _, event := range events {
		if event.CalibreID != scan.CalibreID || event.BookID != scan.BookID || event.WrittenAt.IsZero() {
			continue
		}
		if !scan.ScannedAt.Before(event.WrittenAt) {
			scan.Lineage = "potentially_cwa_derived"
			return
		}
		scan.Lineage = "pre_writeback"
	}
}

// ResolveOwnedEdition compares only editions returned by an exact lookup of
// the established canonical work. CWA and file identifiers never seed provider
// discovery or add candidates from a different work. A provider record is one
// source regardless of how many identifiers or metadata fields it contains.
func ResolveOwnedEdition(snapshot models.CalibreIdentitySnapshot, cb *CalibreBook) models.CalibreEditionResolution {
	result := models.CalibreEditionResolution{Confidence: "unresolved", Reason: "No eligible edition of the canonical work has identifying evidence.", Candidates: []models.CalibreEditionCandidate{}}
	if cb == nil || snapshot.CalibreID != cb.CalibreID || snapshot.RootKey == "" {
		result.Reason = "Canonical work or active ownership link is unavailable."
		return result
	}
	rooted := false
	for _, e := range snapshot.Evidence {
		if e.Status == models.CalibreIdentityRoot && e.Method == metadata.RawMethodExactBook && e.CanonicalIdentity == snapshot.RootKey {
			rooted = true
			break
		}
	}
	if !rooted {
		result.Reason = "Exact canonical provider work lookup is unavailable."
		return result
	}
	type editionClaim struct {
		typ   string
		value string
	}
	type candidate struct {
		e        models.CalibreIdentityEvidence
		cwa      []editionClaim
		artifact []string
	}
	var candidates []candidate
	for _, e := range snapshot.Evidence {
		if e.Status != models.CalibreIdentityRoot || e.Method != metadata.RawMethodExactEditions ||
			e.CanonicalIdentity != snapshot.RootKey || e.EditionID == "" || e.ProviderMetadata["ebook"] != true {
			continue
		}
		candidates = append(candidates, candidate{e: e})
	}
	if len(candidates) == 0 {
		result.Reason = "No ebook editions were returned by the canonical work's exact-editions lookup."
		return result
	}
	cwaIDs := calibreAuditIdentifiers(cb)
	// Retain each normalized CWA type/value claim. Different ISBNs (including
	// aliases of the same identifier type) cannot become one synthetic vote.
	cwaClaims := []editionClaim{}
	for _, typ := range []string{"openlibrary_edition", "isbn", "asin"} {
		for _, v := range cwaIDs[typ] {
			value := auditIdentifier(typ, v.Value)
			if typ == "isbn" {
				value = isbnutil.ToISBN13(v.Value)
			}
			claim := editionClaim{typ, value}
			if value != "" && !slices.Contains(cwaClaims, claim) {
				cwaClaims = append(cwaClaims, claim)
			}
		}
	}
	for i := range candidates {
		c := &candidates[i]
		for _, claim := range cwaClaims {
			if slices.Contains(c.e.NormalizedIdentifiers[claim.typ], claim.value) {
				c.cwa = append(c.cwa, claim)
			}
		}
	}
	// One artifact is one source, even when OPF and several content members
	// contain the same ISBN. Unknown/post-write-back scans may be displayed but
	// cannot independently corroborate CWA or make edition confidence exact.
	strongFiles := map[string]map[int]bool{}
	// Historical pre-write-back observations are not current file scans. They
	// can supply *limited* corroboration only when a present, verified current
	// scan of the same linked file still contains the same ISBN. This never
	// promotes a post-write-back echo to independent exact evidence.
	historical := map[string]map[string]bool{}
	for _, scan := range snapshot.ArtifactHistory {
		if scan.BookID != snapshot.BookID || scan.CalibreID != snapshot.CalibreID ||
			scan.Outcome != "scanned" || scan.Lineage != "pre_writeback" || !scan.AttestedOriginal {
			continue
		}
		key := scan.Format + "/" + scan.FileName + "/" + scan.FilePath
		if historical[key] == nil {
			historical[key] = map[string]bool{}
		}
		for _, id := range scan.Identifiers {
			historical[key][id.NormalizedValue] = true
		}
	}
	historyMatches := map[int]bool{}
	observedMatches := map[int]bool{}
	observedISBNs := map[string]bool{}
	var conflicting []string
	for _, scan := range snapshot.Artifacts {
		if scan.BookID != snapshot.BookID || scan.CalibreID != snapshot.CalibreID ||
			scan.Historical || scan.Stale || scan.Outcome != "scanned" || len(scan.Identifiers) == 0 {
			continue
		}
		fileKey := fmt.Sprintf("%s/%s/%s", scan.Format, scan.FileName, scan.SHA256)
		for _, id := range scan.Identifiers {
			if id.Status == "conflict" {
				conflicting = append(conflicting, "An artifact ISBN points to a rejected/wrong-work provider record.")
				continue
			}
			matching := []int{}
			for i, c := range candidates {
				if id.NormalizedValue != "" && slices.Contains(c.e.NormalizedIdentifiers["isbn"], id.NormalizedValue) {
					matching = append(matching, i)
				}
			}
			if len(matching) == 0 {
				conflicting = append(conflicting, "An artifact ISBN is not verified for an edition of this work.")
			}
			observedISBNs[id.NormalizedValue] = true
			for _, i := range matching {
				observedMatches[i] = true // contradictory current claims still matter, not independent votes
			}
			if scan.Lineage == "pre_writeback" && scan.AttestedOriginal {
				if strongFiles[fileKey] == nil {
					strongFiles[fileKey] = map[int]bool{}
				}
				for _, i := range matching {
					strongFiles[fileKey][i] = true
				}
			} else if historical[scan.Format+"/"+scan.FileName+"/"+scan.FilePath][id.NormalizedValue] {
				for _, i := range matching {
					historyMatches[i] = true
				}
			}
		}
	}
	var cwaMatches, artifactMatches, retainedMatches []int
	for i := range candidates {
		if len(candidates[i].cwa) > 0 {
			cwaMatches = append(cwaMatches, i)
		}
		for file := range strongFiles {
			if strongFiles[file][i] {
				candidates[i].artifact = append(candidates[i].artifact, file)
			}
		}
		if len(candidates[i].artifact) > 0 {
			artifactMatches = append(artifactMatches, i)
		}
		if historyMatches[i] {
			retainedMatches = append(retainedMatches, i)
		}
		reasons := []string{}
		if len(candidates[i].cwa) > 0 {
			claims := make([]string, 0, len(candidates[i].cwa))
			for _, claim := range candidates[i].cwa {
				claims = append(claims, claim.typ+":"+claim.value)
			}
			reasons = append(reasons, "CWA claims "+strings.Join(claims, ", ")+" (one correlated source)")
		}
		if len(candidates[i].artifact) > 0 {
			reasons = append(reasons, "pre-write-back file ISBN matches this edition")
		}
		if historyMatches[i] {
			reasons = append(reasons, "historical pre-write-back ISBN remains in a current scan of the same file (limited support)")
		}
		result.Candidates = append(result.Candidates, models.CalibreEditionCandidate{
			EditionID: candidates[i].e.EditionID, Provider: candidates[i].e.Provider, Reasons: reasons})
	}
	// Intersect individual normalized type/value claims, not identifier types.
	// A native edition ID and ISBN can narrow a shared ISBN but remain one
	// correlated CWA source (high, never exact). Distinct values of the same
	// type are ambiguous even if a provider happens to assign both to one
	// edition; Calibre has not established which value identifies this file.
	claimIntersection := map[int]bool{}
	seenTypes := map[string]bool{}
	matchedClaims := 0
	for _, claim := range cwaClaims {
		if seenTypes[claim.typ] {
			conflicting = append(conflicting, "Distinct CWA values of the same identifier type cannot identify one owned edition.")
		}
		seenTypes[claim.typ] = true
		matching := []int{}
		for i, c := range candidates {
			if slices.Contains(c.cwa, claim) {
				matching = append(matching, i)
			}
		}
		if len(matching) == 0 {
			conflicting = append(conflicting, "A CWA edition identifier has no verified edition within the canonical work.")
			continue
		}
		if matchedClaims == 0 {
			for _, i := range matching {
				claimIntersection[i] = true
			}
		} else {
			for i := range claimIntersection {
				if !slices.Contains(matching, i) {
					delete(claimIntersection, i)
				}
			}
		}
		matchedClaims++
	}
	claimWinner := -1
	if matchedClaims > 1 {
		switch len(claimIntersection) {
		case 0:
			conflicting = append(conflicting, "CWA edition identifiers disagree within the canonical work.")
		case 1:
			for i := range claimIntersection {
				claimWinner = i
			}
		}
	}
	// Selection presumes the canonical provider's exact-editions list is
	// complete. A bounded/truncated response can hide another edition with the
	// same ISBN; keep the visible candidates but never assert uniqueness.
	complete := false
	for _, lookup := range snapshot.Lookups {
		if lookup.Method == metadata.RawMethodExactEditions && lookup.Provider == candidates[0].e.Provider &&
			lookup.Seed == candidates[0].e.Seed {
			complete = lookup.Outcome == models.CalibreIdentityLookupAnswered
			break
		}
	}
	if !complete {
		result.Confidence = "ambiguous"
		result.Reason = "The canonical provider's exact-editions lookup is incomplete; additional editions may share these identifiers."
		return result
	}
	// Soft metadata may distinguish editions only when one *normalized
	// value* is genuinely shared by multiple candidates. Two different ISBNs
	// with the same type are competing CWA claims, not a shared identifier.
	// Every complete comparable field narrows the same candidate set; zero
	// matches or incompatible fields are conflicts, not missing evidence.
	softWinner := -1
	softReason := ""
	softConflict := false
	if len(cwaClaims) == 1 && len(cwaMatches) > 1 && len(artifactMatches) == 0 && len(retainedMatches) == 0 {
		allowed := slices.Clone(cwaMatches)
		narrow := func(matches []int, complete bool, field, reason string) {
			if !complete {
				return
			}
			before := len(allowed)
			allowed = slices.DeleteFunc(allowed, func(i int) bool { return !slices.Contains(matches, i) })
			if len(matches) == 0 {
				conflicting = append(conflicting, "Owned-book "+field+" does not match any candidate edition sharing the CWA identifier.")
			} else if before > 0 && len(allowed) == 0 {
				conflicting = append(conflicting, "Owned-book edition metadata fields select incompatible candidates.")
			}
			if before > 1 && len(allowed) == 1 {
				softReason = reason
			}
		}
		if publisher := auditText(cb.Publisher); publisher != "" {
			matches := []int{}
			complete := true
			for _, i := range cwaMatches {
				candidatePublisher, _ := candidates[i].e.ProviderMetadata["publisher"].(string)
				if auditText(candidatePublisher) == "" {
					complete = false
					break
				}
				if auditText(candidatePublisher) == publisher {
					matches = append(matches, i)
				}
			}
			narrow(matches, complete, "publisher/imprint", "The same claimed identifier spans editions; owned-book publisher/imprint uniquely distinguishes the provider edition (one correlated CWA source).")
		}
		language := models.NormalizeLanguageCode(cb.Language)
		if language == "" && len(cb.Languages) == 1 {
			language = models.NormalizeLanguageCode(cb.Languages[0])
		}
		if language != "" {
			matches := []int{}
			complete := true
			for _, i := range cwaMatches {
				providerLang, _ := candidates[i].e.ProviderMetadata["language"].(string)
				if models.NormalizeLanguageCode(providerLang) == "" {
					complete = false
					break
				}
				if models.NormalizeLanguageCode(providerLang) == language {
					matches = append(matches, i)
				}
			}
			narrow(matches, complete, "language", "The same claimed ISBN spans editions; owned-book language uniquely distinguishes the provider edition (one correlated CWA source).")
		}
		if auditKnownYear(cb.PublishDate) {
			matches := []int{}
			complete := true
			for _, i := range cwaMatches {
				date, _ := candidates[i].e.ProviderMetadata["publicationDate"].(string)
				if auditYear(date) == "" {
					complete = false
					break
				}
				if auditYear(date) == fmt.Sprintf("%04d", cb.PublishDate.Year()) {
					matches = append(matches, i)
				}
			}
			narrow(matches, complete, "publication year", "The same claimed ISBN spans editions; owned-book publication year distinguishes the provider edition (not the work release year).")
		}
		softConflict = len(allowed) == 0
		if len(allowed) == 1 && !softConflict {
			softWinner = allowed[0]
		}
	}
	if len(conflicting) > 0 || softConflict ||
		(len(observedISBNs) > 1 && len(observedMatches) > 1) ||
		(len(cwaMatches) == 1 && len(observedMatches) > 0 && !observedMatches[cwaMatches[0]]) ||
		(claimWinner >= 0 && len(observedMatches) > 0 && !observedMatches[claimWinner]) ||
		(softWinner >= 0 && len(observedMatches) > 0 && !observedMatches[softWinner]) ||
		(len(cwaMatches) > 1 && len(artifactMatches) == 0 && len(retainedMatches) == 0 && softWinner < 0 && claimWinner < 0) ||
		(len(cwaMatches) > 1 && len(artifactMatches) == 1 && !slices.Contains(cwaMatches, artifactMatches[0])) ||
		(len(cwaMatches) > 1 && len(retainedMatches) == 1 && !slices.Contains(cwaMatches, retainedMatches[0])) ||
		len(artifactMatches) > 1 || len(retainedMatches) > 1 ||
		(len(cwaMatches) == 1 && len(artifactMatches) == 1 && cwaMatches[0] != artifactMatches[0]) ||
		(len(cwaMatches) == 1 && len(retainedMatches) == 1 && cwaMatches[0] != retainedMatches[0]) ||
		(len(artifactMatches) == 1 && len(retainedMatches) == 1 && artifactMatches[0] != retainedMatches[0]) {
		result.Confidence = "ambiguous"
		result.Reason = "Conflicting or multiple edition identifiers remain within the canonical work. " + strings.Join(slices.Compact(conflicting), " ")
		return result
	}
	selected := -1
	if len(artifactMatches) == 1 {
		selected = artifactMatches[0]
		result.Confidence = "exact"
		result.Reason = "A complete pre-write-back artifact ISBN uniquely matches a canonical-work ebook edition; file fields count as one source."
	} else if len(retainedMatches) == 1 {
		selected = retainedMatches[0]
		result.Confidence = "high"
		result.Reason = "A historical pre-write-back observation and the current file agree on an ISBN; changed file bytes prevent an exact physical-edition assertion."
	} else if claimWinner >= 0 {
		selected = claimWinner
		result.Confidence = "high"
		result.Reason = "The CWA native edition ID and ISBN converge on one provider edition; they are one claim (correlated CWA fields), not independent corroboration."
	} else if softWinner >= 0 {
		selected = softWinner
		result.Confidence = "high"
		result.Reason = softReason
	} else if len(cwaMatches) == 1 {
		selected = cwaMatches[0]
		result.Confidence = "high"
		result.Reason = "A CWA identifier uniquely matches a canonical-work ebook edition; correlated CWA fields are one claim, not independent corroboration."
	}
	if selected >= 0 {
		result.EditionID = candidates[selected].e.EditionID
		result.Provider = candidates[selected].e.Provider
		return result
	}
	if len(candidates) > 1 {
		result.Confidence = "ambiguous"
		result.Reason = "Multiple canonical-work editions exist, but no independent or unique edition identifier selects one."
	}
	return result
}

// editionFromEvidence projects an exact canonical provider edition into audit
// comparisons without importing it into Bindery's curated book catalogue.
func editionFromEvidence(snapshot models.CalibreIdentitySnapshot) *models.Edition {
	if snapshot.Edition.EditionID == "" || (snapshot.Edition.Confidence != "exact" && snapshot.Edition.Confidence != "high") {
		return nil
	}
	for _, e := range snapshot.Evidence {
		if e.Status != models.CalibreIdentityRoot || e.Method != metadata.RawMethodExactEditions ||
			e.CanonicalIdentity != snapshot.RootKey || e.EditionID != snapshot.Edition.EditionID || e.Provider != snapshot.Edition.Provider {
			continue
		}
		ed := &models.Edition{ForeignID: e.EditionID, IsEbook: true, Format: "EPUB"}
		if title, ok := e.ProviderMetadata["title"].(string); ok {
			ed.Title = title
		}
		if language, ok := e.ProviderMetadata["language"].(string); ok {
			ed.Language = language
		}
		if date, ok := e.ProviderMetadata["publicationDate"].(string); ok {
			if parsed, err := time.Parse("2006-01-02", date); err == nil {
				ed.PublishDate = &parsed
			}
		}
		return ed
	}
	return nil
}
