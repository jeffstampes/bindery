package calibre

import (
	"regexp"
	"slices"
	"strings"

	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/models"
)

var (
	openLibraryWorkIDRe   = regexp.MustCompile(`^OL\d+W$`)
	openLibraryAuthorIDRe = regexp.MustCompile(`^OL\d+A$`)
)

type auditIdentifiers map[string][]models.CalibreAuditEvidence

// The Calibre reader presents a single identifier per type, while Bindery may
// have several edition ISBNs. Only compare types with persisted external
// evidence; never treat an empty provider field as "Calibre has extra data".
func calibreAuditIdentifiers(cb *CalibreBook) auditIdentifiers {
	out := make(auditIdentifiers)
	for rawType, rawValue := range cb.Identifiers {
		if typ := auditIdentifierType(rawType, rawValue); typ != "" && strings.TrimSpace(rawValue) != "" {
			out[typ] = append(out[typ], models.CalibreAuditEvidence{
				Value: rawValue, Source: "calibre.identifiers." + rawType,
			})
		}
	}
	if strings.TrimSpace(cb.ISBN) != "" && len(out["isbn"]) == 0 {
		out["isbn"] = append(out["isbn"], models.CalibreAuditEvidence{
			Value: cb.ISBN, Source: "calibre.identifiers.isbn",
		})
	}
	for _, evidence := range out {
		slices.SortFunc(evidence, auditEvidenceOrder)
	}
	return out
}

func auditIdentifierType(rawType, rawValue string) string {
	switch cleanIdentifierType(rawType) {
	case "isbn", "asin", "hardcover", "goodreads", "dnb":
		return cleanIdentifierType(rawType)
	case "google", "googlebooks":
		return "google"
	case "openlibrary", "ol", "openlibrary_work", "openlibrary_edition":
		id := normalizeOpenLibraryIdentifier(rawValue)
		if openLibraryWorkIDRe.MatchString(id) {
			return "openlibrary"
		}
		if openLibraryEditionIDRe.MatchString(id) {
			return "openlibrary_edition"
		}
	}
	return ""
}

func auditIdentifier(typ, value string) string {
	value = cleanIdentifierValue(value)
	if value == "" {
		return ""
	}
	switch typ {
	case "isbn":
		if canonical := isbnutil.ToISBN13(value); canonical != "" {
			return canonical
		}
		return isbnutil.Normalize(value) // retain malformed data for human review
	case "asin":
		return isbnutil.NormalizeASIN(value)
	case "openlibrary", "openlibrary_edition":
		return normalizeOpenLibraryIdentifier(value)
	case "hardcover":
		return trimIdentifierPrefix(value, "hc:")
	case "google":
		return trimIdentifierPrefix(value, "gb:") // volume IDs may be case-sensitive
	case "dnb":
		return trimIdentifierPrefix(value, "dnb:")
	default:
		return value
	}
}

func (c *auditComparator) compareIdentifiers(calibreIDs auditIdentifiers) {
	providerIDs := externalAuditIdentifiers(c.book)
	for typ, external := range providerIDs {
		canonical := func(s string) string { return auditIdentifier(typ, s) }
		if len(auditNormalized(external, canonical)) == 0 {
			continue
		}
		calibreValues := calibreIDs[typ]
		if len(calibreValues) == 0 {
			calibreValues = []models.CalibreAuditEvidence{{Source: "calibre.identifiers." + typ}}
		}
		kind := models.CalibreAuditIdentifierConflict
		reason := "Calibre and the stored external evidence use different identifiers of this type; editions may differ."
		if len(auditNormalized(calibreValues, canonical)) == 0 {
			kind = models.CalibreAuditIdentifierMissing
			reason = "Calibre has no identifier of this type, but the stored provider record does."
		}
		assessment := models.CalibreAuditNeedsReview
		if typ == "isbn" || typ == "asin" || typ == "openlibrary_edition" ||
			len(auditNormalized(external, canonical)) > 1 {
			assessment = models.CalibreAuditAmbiguous
		}
		c.record(models.CalibreAuditFieldIdentifiers, typ, kind, assessment, reason,
			c.identifierBasis(external), calibreValues, external, canonical)
	}
}

func externalAuditIdentifiers(book *models.Book) auditIdentifiers {
	out := make(auditIdentifiers)
	add := func(rawType, value, source, provider, foreignID string, recordID int64) {
		typ := auditIdentifierType(rawType, value)
		if typ == "" || auditIdentifier(typ, value) == "" {
			return
		}
		out[typ] = append(out[typ], models.CalibreAuditEvidence{
			Value: value, Source: source, Provider: provider, ForeignID: foreignID, RecordID: recordID,
		})
	}
	add(book.MetadataProvider, book.ForeignID, "books.foreign_id", book.MetadataProvider, book.ForeignID, book.ID)
	// A work-level ASIN (or an unattributed identifier row) may be supplied by
	// ABS or an audiobook provider even on a book monitored in both formats.
	// Only an identified ebook edition can attribute an ASIN to the owned ebook.
	for _, id := range book.Identifiers {
		if auditIdentifierType(id.Provider, id.ForeignID) != "asin" {
			add(id.Provider, id.ForeignID, "book_identifiers.foreign_id", id.Provider, book.ForeignID, id.BookID)
		}
	}
	for i := range book.Editions {
		ed := &book.Editions[i]
		if !auditExternalEdition(ed) {
			continue
		}
		provider := auditSourceProvider(ed.ForeignID)
		if ed.ISBN13 != nil {
			add("isbn", *ed.ISBN13, "editions.isbn_13", provider, ed.ForeignID, ed.ID)
		}
		if ed.ISBN10 != nil {
			add("isbn", *ed.ISBN10, "editions.isbn_10", provider, ed.ForeignID, ed.ID)
		}
		if ed.ASIN != nil {
			add("asin", *ed.ASIN, "editions.asin", provider, ed.ForeignID, ed.ID)
		}
		add("openlibrary_edition", ed.ForeignID, "editions.foreign_id", provider, ed.ForeignID, ed.ID)
	}
	return out
}

// Only provider-identified ebook editions count as independent evidence.
// An explicit print/audio format overrides IsEbook, which is occasionally set
// imprecisely; older rows without that flag still qualify by digital format.
func auditExternalEdition(ed *models.Edition) bool {
	if ed == nil {
		return false
	}
	provider := auditSourceProvider(ed.ForeignID)
	if provider == "" || (provider == "openlibrary" &&
		!openLibraryEditionIDRe.MatchString(normalizeOpenLibraryIdentifier(ed.ForeignID))) {
		return false
	}
	format := strings.ToLower(strings.TrimSpace(ed.Format))
	if strings.Contains(format, "audio") || strings.Contains(format, "paperback") ||
		strings.Contains(format, "hardback") || strings.Contains(format, "hardcover") ||
		strings.Contains(format, "mass market") || strings.Contains(format, "print") ||
		strings.Contains(format, "m4b") || strings.Contains(format, "m4a") ||
		strings.Contains(format, "mp3") || strings.Contains(format, "flac") ||
		format == "audible" || format == "ogg" || format == "wav" ||
		format == "cd" || format == "cassette" {
		return false
	}
	if ed.IsEbook || strings.Contains(format, "ebook") || strings.Contains(format, "e-book") ||
		strings.Contains(format, "kindle") {
		return true
	}
	switch format {
	case "epub", "mobi", "pdf", "azw", "azw3", "kepub", "html", "txt", "cbz", "cbr", "djvu":
		return true
	}
	return false
}

// Revalidate the audit's match with independent evidence only; the ownership
// matcher may correctly use a Calibre-imported edition to establish ownership,
// but its ISBN cannot establish an external metadata discrepancy. Unattributed
// ASINs and invalid ISBNs likewise cannot establish an exact ebook match.
func auditMatchEvidence(book *models.Book) *models.Book {
	candidate := *book
	candidate.ASIN = ""
	candidate.ProviderISBNs = nil
	candidate.Identifiers = nil
	for _, id := range book.Identifiers {
		typ := auditIdentifierType(id.Provider, id.ForeignID)
		if typ != "" && typ != "asin" && typ != "isbn" {
			candidate.Identifiers = append(candidate.Identifiers, id)
		}
	}
	candidate.Editions = nil
	for _, edition := range book.Editions {
		if !auditExternalEdition(&edition) {
			continue
		}
		if edition.ISBN13 != nil && isbnutil.ToISBN13(*edition.ISBN13) == "" {
			edition.ISBN13 = nil
		}
		if edition.ISBN10 != nil && isbnutil.ToISBN13(*edition.ISBN10) == "" {
			edition.ISBN10 = nil
		}
		candidate.Editions = append(candidate.Editions, edition)
	}
	if book.IsFieldLocked(models.BookFieldTitle) {
		candidate.Title = "" // A hand-edited title cannot validate a fallback match.
	}
	if book.Author == nil || auditSourceProvider(book.Author.ForeignID) == "" {
		candidate.Author = nil
	}
	return &candidate
}

// Without an edition identifier in common, work-level release dates cannot
// reliably describe the owned copy. Multiple matching provider editions are
// also ambiguous, even if their dates happen to agree.
func auditMatchedEdition(editions []models.Edition, calibreIDs auditIdentifiers) *models.Edition {
	var selected *models.Edition
	for i := range editions {
		ed := &editions[i]
		if !auditExternalEdition(ed) {
			continue
		}
		var matches bool
		for _, typ := range []string{"isbn", "asin", "openlibrary_edition"} {
			norm := func(s string) string { return auditIdentifier(typ, s) }
			if typ == "isbn" {
				// Invalid identifiers may be reported, but cannot identify an
				// edition well enough to compare its title, date or language.
				norm = isbnutil.ToISBN13
			}
			cal := auditNormalized(calibreIDs[typ], norm)
			if len(cal) == 0 {
				continue
			}
			var candidates []models.CalibreAuditEvidence
			switch typ {
			case "isbn":
				if ed.ISBN13 != nil {
					candidates = append(candidates, models.CalibreAuditEvidence{Value: *ed.ISBN13})
				}
				if ed.ISBN10 != nil {
					candidates = append(candidates, models.CalibreAuditEvidence{Value: *ed.ISBN10})
				}
			case "asin":
				if ed.ASIN != nil {
					candidates = append(candidates, models.CalibreAuditEvidence{Value: *ed.ASIN})
				}
			case "openlibrary_edition":
				if openLibraryEditionIDRe.MatchString(normalizeOpenLibraryIdentifier(ed.ForeignID)) {
					candidates = append(candidates, models.CalibreAuditEvidence{Value: ed.ForeignID})
				}
			}
			if auditIntersects(cal, auditNormalized(candidates, norm)) {
				matches = true
				break
			}
		}
		if matches {
			if selected != nil {
				return nil
			}
			selected = ed
		}
	}
	return selected
}
