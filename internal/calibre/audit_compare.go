package calibre

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

type auditFieldKey struct{ field, key string }

type auditOutcome struct {
	finding   models.CalibreAuditFinding
	different bool
}

type auditComparator struct {
	book     *models.Book
	calibre  *CalibreBook
	ref      *models.CalibreWorkCrossReference
	outcomes map[auditFieldKey]auditOutcome
}

// auditExternalBook is intentionally stricter than the matcher: a default
// metadata_provider of "openlibrary" does not turn a local or imported book
// with a synthetic foreign_id into independent external evidence.
func auditExternalBook(book *models.Book) bool {
	id := strings.TrimSpace(book.ForeignID)
	provider := strings.ToLower(strings.TrimSpace(book.MetadataProvider))
	switch provider {
	case "openlibrary", "ol":
		return openLibraryWorkIDRe.MatchString(normalizeOpenLibraryIdentifier(id))
	case "hardcover":
		return strings.HasPrefix(id, "hc:") && len(id) > 3
	case "googlebooks", "google":
		return strings.HasPrefix(id, "gb:") && len(id) > 3
	case "dnb":
		return strings.HasPrefix(id, "dnb:") && len(id) > 4
	default:
		return false
	}
}

func compareAuditBook(book *models.Book, cb *CalibreBook, ref *models.CalibreWorkCrossReference, series []db.BookSeriesMembership) map[auditFieldKey]auditOutcome {
	c := &auditComparator{book: book, calibre: cb, ref: ref, outcomes: make(map[auditFieldKey]auditOutcome)}
	calibreIDs := calibreAuditIdentifiers(cb)
	c.compareIdentifiers(calibreIDs)
	matchedEdition := auditMatchedEdition(book.Editions, calibreIDs)
	c.compareTitle(matchedEdition)
	c.compareAuthors()
	c.compareSeries(series)
	c.compareLanguage(matchedEdition)
	c.compareDate(matchedEdition)
	return c.outcomes
}

type auditNormalizer func(string) string

// record compares sets of present, normalized values. Missing provider values
// mean "no evidence", not a discrepancy; missing Calibre values are comparable
// when independent provider evidence exists. One intersecting author/title/ID
// (or one matching series membership) is enough to avoid a noisy finding.
func (c *auditComparator) record(field, key, kind, assessment, reason, basis string,
	calibreValues, providerValues []models.CalibreAuditEvidence, normalize auditNormalizer,
) {
	calibreValues = slices.Clone(calibreValues)
	providerValues = slices.Clone(providerValues)
	slices.SortFunc(calibreValues, auditEvidenceOrder)
	slices.SortFunc(providerValues, auditEvidenceOrder)
	providerNorm := auditNormalized(providerValues, normalize)
	if len(providerNorm) == 0 {
		return
	}
	calibreNorm := auditNormalized(calibreValues, normalize)
	different := !auditIntersects(calibreNorm, providerNorm)
	fingerprint := auditFingerprint(c.book.ID, c.calibre.CalibreID, field, key, basis,
		c.ref.MatchMethod, c.ref.Confidence, calibreNorm, providerNorm)
	c.outcomes[auditFieldKey{field, key}] = auditOutcome{
		finding: models.CalibreAuditFinding{
			BookID: c.book.ID, CalibreID: c.calibre.CalibreID, Field: field, EvidenceKey: key,
			FindingType: kind, Assessment: assessment, CalibreEvidence: calibreValues,
			BinderyEvidence: providerValues, MatchMethod: c.ref.MatchMethod,
			MatchConfidence: c.ref.Confidence, Reason: reason,
			ComparisonFingerprint: fingerprint, State: models.CalibreAuditUnresolved,
		},
		different: different,
	}
}

func auditEvidenceOrder(a, b models.CalibreAuditEvidence) int {
	return cmp.Or(
		cmp.Compare(a.Source, b.Source), cmp.Compare(a.Provider, b.Provider),
		cmp.Compare(a.ForeignID, b.ForeignID), cmp.Compare(a.RecordID, b.RecordID),
		cmp.Compare(a.Value, b.Value),
	)
}

func auditNormalized(values []models.CalibreAuditEvidence, normalize auditNormalizer) []string {
	var out []string
	for _, v := range values {
		if value := normalize(v.Value); value != "" {
			out = append(out, value)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func auditIntersects(a, b []string) bool {
	for _, value := range a {
		if _, found := slices.BinarySearch(b, value); found {
			return true
		}
	}
	return false
}

// The fingerprint includes canonical values, match provenance and relevant
// external record identities. Raw punctuation, database row IDs and access
// timestamps do not invalidate a human's ignore. JSON prevents delimiter
// ambiguities.
func auditFingerprint(bookID, calibreID int64, field, key, basis, method, confidence string, cal, provider []string) string {
	payload, _ := json.Marshal(struct {
		Version    int      `json:"v"`
		BookID     int64    `json:"b"`
		CalibreID  int64    `json:"c"`
		Field      string   `json:"f"`
		Key        string   `json:"k"`
		Basis      string   `json:"s"`
		Method     string   `json:"m"`
		Confidence string   `json:"q"`
		Calibre    []string `json:"cal"`
		Provider   []string `json:"ext"`
	}{2, bookID, calibreID, field, key, basis, method, confidence, cal, provider})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (c *auditComparator) bookBasis() string {
	return auditSourceIdentity(c.book.ForeignID)
}

func auditSourceIdentity(foreignID string) string {
	provider := auditSourceProvider(foreignID)
	typ := auditIdentifierType(provider, foreignID)
	if typ != "" {
		return typ + ":" + auditIdentifier(typ, foreignID)
	}
	return provider + ":" + strings.TrimSpace(foreignID)
}

func (c *auditComparator) identifierBasis(external []models.CalibreAuditEvidence) string {
	identities := make(map[string]struct{})
	for _, e := range external {
		if strings.HasPrefix(e.Source, "editions.") && e.ForeignID != "" {
			identities[auditSourceIdentity(e.ForeignID)] = struct{}{}
		}
	}
	ids := make([]string, 0, len(identities))
	for id := range identities {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return c.bookBasis() + "/editions:" + strings.Join(ids, ",")
}

func (c *auditComparator) compareTitle(matchedEdition *models.Edition) {
	var provider []models.CalibreAuditEvidence
	basis := c.bookBasis()
	assessment := models.CalibreAuditAmbiguous // work vs owned edition
	if matchedEdition != nil && auditText(matchedEdition.Title) != "" &&
		!strings.EqualFold(strings.TrimSpace(matchedEdition.Title), "Unknown Edition") {
		provider = append(provider, auditEditionEvidence(matchedEdition.Title, "editions.title", matchedEdition))
		basis += "/edition:" + auditSourceIdentity(matchedEdition.ForeignID)
		assessment = models.CalibreAuditNeedsReview
	} else if !c.book.IsFieldLocked(models.BookFieldTitle) {
		provider = append(provider, auditWorkEvidence(c.book.Title, "books.title", c.book))
		if c.book.OriginalTitle != "" {
			provider = append(provider, auditWorkEvidence(c.book.OriginalTitle, "books.original_title", c.book))
		}
	}
	cal := []models.CalibreAuditEvidence{{Value: c.calibre.Title, Source: "calibre.books.title"}}
	c.record(models.CalibreAuditFieldTitle, "", models.CalibreAuditTitleDifference, assessment,
		"The owned title differs from the stored external work or uniquely matched edition title; translations and subtitles need human review.",
		basis, cal, provider, auditText)
}

func (c *auditComparator) compareAuthors() {
	if c.book.Author == nil || auditAuthor(c.book.Author.Name) == "" {
		return
	}
	provider := auditSourceProvider(c.book.Author.ForeignID)
	if provider == "" {
		return // The stored author is not independently attributed to a provider.
	}
	var cal []models.CalibreAuditEvidence
	for _, author := range c.calibre.Authors {
		cal = append(cal, models.CalibreAuditEvidence{Value: author.Name, Source: "calibre.authors.name", RecordID: author.CalibreID})
	}
	if len(cal) == 0 {
		cal = []models.CalibreAuditEvidence{{Source: "calibre.authors.name"}}
	}
	providerEvidence := []models.CalibreAuditEvidence{{Value: c.book.Author.Name, Source: "authors.name",
		Provider: provider, ForeignID: c.book.Author.ForeignID, RecordID: c.book.Author.ID}}
	c.record(models.CalibreAuditFieldAuthors, "", models.CalibreAuditAuthorDifference, models.CalibreAuditAmbiguous,
		"The stored provider author does not match any owned-book author; co-authors and work-level attribution require review.",
		c.bookBasis()+"/author:"+auditSourceIdentity(c.book.Author.ForeignID), cal, providerEvidence, auditAuthor)
}

func (c *auditComparator) compareSeries(memberships []db.BookSeriesMembership) {
	var provider []models.CalibreAuditEvidence
	var relevant []db.BookSeriesMembership
	for _, m := range memberships {
		id := strings.ToLower(strings.TrimSpace(m.SeriesForeignID))
		if (!strings.HasPrefix(id, "ol-series:") && !strings.HasPrefix(id, "hc-series:")) ||
			auditText(m.SeriesTitle) == "" {
			continue
		}
		provider = append(provider, models.CalibreAuditEvidence{Value: m.SeriesTitle, Source: "series.title via series_books",
			Provider: auditSourceProvider(m.SeriesForeignID), ForeignID: m.SeriesForeignID, RecordID: m.SeriesID})
		relevant = append(relevant, m)
	}
	if len(provider) == 0 {
		return
	}
	var cal []models.CalibreAuditEvidence
	if c.calibre.Series != nil {
		cal = append(cal, models.CalibreAuditEvidence{Value: c.calibre.Series.Name, Source: "calibre.series.name"})
	} else {
		cal = append(cal, models.CalibreAuditEvidence{Source: "calibre.series.name"})
	}
	var seriesIDs []string
	for _, m := range relevant {
		seriesIDs = append(seriesIDs, m.SeriesForeignID)
	}
	slices.Sort(seriesIDs)
	c.record(models.CalibreAuditFieldSeries, "", models.CalibreAuditSeriesDifference, models.CalibreAuditAmbiguous,
		"The owned series is absent from the provider-linked series memberships; a work can belong to several series.",
		c.bookBasis()+"/series:"+strings.Join(seriesIDs, ","), cal, provider, auditText)

	if c.calibre.Series == nil || auditText(c.calibre.Series.Name) == "" {
		return // No common membership: a position comparison is not meaningful.
	}
	var same []db.BookSeriesMembership
	for _, m := range relevant {
		if auditText(m.SeriesTitle) == auditText(c.calibre.Series.Name) {
			same = append(same, m)
		}
	}
	if len(same) != 1 || auditPosition(same[0].Position) == "" {
		return // Multiple provider series with one name, or no known index.
	}
	position := same[0]
	calPosition := []models.CalibreAuditEvidence{{Value: strconv.FormatFloat(c.calibre.Series.Position, 'f', -1, 64),
		Source: "calibre.books.series_index"}}
	providerPosition := []models.CalibreAuditEvidence{{Value: position.Position, Source: "series_books.position_in_series",
		Provider: auditSourceProvider(position.SeriesForeignID), ForeignID: position.SeriesForeignID, RecordID: position.SeriesID}}
	c.record(models.CalibreAuditFieldPosition, position.SeriesForeignID, models.CalibreAuditPositionDifference,
		models.CalibreAuditNeedsReview,
		"The same named series has a different numeric index; omnibus and fractional ordering may need human review.",
		c.bookBasis()+"/series:"+position.SeriesForeignID, calPosition, providerPosition, auditPosition)
}

func (c *auditComparator) compareLanguage(matchedEdition *models.Edition) {
	var provider []models.CalibreAuditEvidence
	basis := c.bookBasis()
	assessment := models.CalibreAuditAmbiguous
	if matchedEdition != nil && strings.TrimSpace(matchedEdition.Language) != "" {
		provider = append(provider, auditEditionEvidence(matchedEdition.Language, "editions.language", matchedEdition))
		basis += "/edition:" + auditSourceIdentity(matchedEdition.ForeignID)
		assessment = models.CalibreAuditNeedsReview
	} else if !c.book.IsFieldLocked(models.BookFieldLanguage) {
		provider = append(provider, auditWorkEvidence(c.book.Language, "books.language", c.book))
	}
	languages := c.calibre.Languages
	if len(languages) == 0 {
		languages = []string{c.calibre.Language}
	}
	cal := make([]models.CalibreAuditEvidence, 0, len(languages))
	for _, language := range languages {
		cal = append(cal, models.CalibreAuditEvidence{Value: language, Source: "calibre.languages.lang_code"})
	}
	c.record(models.CalibreAuditFieldLanguage, "", models.CalibreAuditLanguageDifference, assessment,
		"The normalized language of the owned copy differs from stored provider evidence; work-level language may cover other editions.",
		basis, cal, provider, models.NormalizeLanguageCode)
}

func (c *auditComparator) compareDate(matchedEdition *models.Edition) {
	// A work release date may predate an owned edition by years. Only a
	// uniquely identifier-matched, externally sourced edition is comparable.
	if matchedEdition == nil || !auditKnownYear(matchedEdition.PublishDate) {
		return
	}
	provider := []models.CalibreAuditEvidence{auditEditionEvidence(matchedEdition.PublishDate.Format("2006-01-02"),
		"editions.publish_date", matchedEdition)}
	cal := []models.CalibreAuditEvidence{{Source: "calibre.books.pubdate"}}
	if auditKnownYear(c.calibre.PublishDate) {
		cal[0].Value = c.calibre.PublishDate.Format("2006-01-02")
	}
	c.record(models.CalibreAuditFieldPubDate, "", models.CalibreAuditPubDateDifference,
		models.CalibreAuditNeedsReview,
		"The publication years differ for a uniquely identifier-matched edition; printings and incomplete dates need review.",
		c.bookBasis()+"/edition:"+auditSourceIdentity(matchedEdition.ForeignID), cal, provider, auditYear)
}

func auditWorkEvidence(value, source string, book *models.Book) models.CalibreAuditEvidence {
	return models.CalibreAuditEvidence{Value: value, Source: source, Provider: book.MetadataProvider,
		ForeignID: book.ForeignID, RecordID: book.ID}
}

func auditEditionEvidence(value, source string, edition *models.Edition) models.CalibreAuditEvidence {
	return models.CalibreAuditEvidence{Value: value, Source: source, Provider: auditSourceProvider(edition.ForeignID),
		ForeignID: edition.ForeignID, RecordID: edition.ID}
}

// auditSourceProvider identifies the external record that supplied evidence;
// a book can carry an OpenLibrary edition or a Hardcover series even when its
// primary metadata provider is different. Unknown/synthetic IDs prove no
// external attribution and must not be used for author comparisons.
func auditSourceProvider(foreignID string) string {
	id := strings.TrimSpace(foreignID)
	lower := strings.ToLower(id)
	olID := normalizeOpenLibraryIdentifier(id)
	switch {
	case openLibraryWorkIDRe.MatchString(olID), openLibraryEditionIDRe.MatchString(olID),
		openLibraryAuthorIDRe.MatchString(olID), strings.HasPrefix(lower, "ol-series:"):
		return "openlibrary"
	case strings.HasPrefix(lower, "hc:"), strings.HasPrefix(lower, "hc-series:"):
		return "hardcover"
	case strings.HasPrefix(lower, "gb:"):
		return "googlebooks"
	case strings.HasPrefix(lower, "dnb:"):
		return "dnb"
	default:
		return ""
	}
}

func auditText(s string) string { return textutil.FoldForSearch(s) }

func auditAuthor(s string) string {
	if strings.Count(s, ",") == 1 {
		parts := strings.SplitN(s, ",", 2)
		if strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
			s = parts[1] + " " + parts[0]
		}
	}
	return auditText(s)
}

// Calibre indexes are numeric; 1, 1.0 and 01.00 are identical. Hundredths
// are the visible granularity in the UI; smaller float artifacts are not a
// material disagreement, while a .01 change is still surfaced.
func auditPosition(raw string) string {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return ""
	}
	return strconv.FormatFloat(math.Round(value*100)/100, 'f', 2, 64)
}

func auditKnownYear(value *time.Time) bool { return value != nil && value.Year() >= 1000 }

func auditYear(raw string) string {
	if len(raw) < 4 {
		return ""
	}
	if year, err := strconv.Atoi(raw[:4]); err == nil && year >= 1000 {
		return fmt.Sprintf("%04d", year)
	}
	return ""
}
