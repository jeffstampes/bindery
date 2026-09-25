package calibre

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

var (
	punctuationRe = regexp.MustCompile(`[^\w\s]`)
	multiSpaceRe  = regexp.MustCompile(`\s+`)
)

// MatchResult captures the outcome of matching a Bindery work against a Calibre library.
type MatchResult struct {
	BookID             int64          `json:"bookId"`
	CalibreID          int64          `json:"calibreId"`
	MatchMethod        string         `json:"matchMethod"`
	Confidence         string         `json:"confidence"`
	Status             string         `json:"status"` // "matched", "ambiguous", "unmatched"
	CalibreFingerprint string         `json:"calibreFingerprint,omitempty"`
	CandidateIDs       []int64        `json:"candidateIds,omitempty"`
	MatchedIdentifier  string         `json:"matchedIdentifier,omitempty"`
	MatchDetails       map[string]any `json:"matchDetails,omitempty"`
}

// ToCrossReference creates a models.CalibreWorkCrossReference struct ready for persistence.
// Unmatched results return nil to prevent persisting rows that violate DB constraints.
func (mr *MatchResult) ToCrossReference() *models.CalibreWorkCrossReference {
	if mr == nil || mr.Status == "unmatched" || mr.Status == "" || mr.Status == "none" {
		return nil
	}

	detailsJSON := "{}"
	if len(mr.MatchDetails) > 0 {
		if data, err := json.Marshal(mr.MatchDetails); err == nil {
			detailsJSON = string(data)
		}
	} else if len(mr.CandidateIDs) > 0 {
		detailsMap := map[string]any{"candidates": mr.CandidateIDs}
		if data, err := json.Marshal(detailsMap); err == nil {
			detailsJSON = string(data)
		}
	}

	return &models.CalibreWorkCrossReference{
		BookID:             mr.BookID,
		CalibreID:          mr.CalibreID,
		MatchMethod:        mr.MatchMethod,
		Confidence:         mr.Confidence,
		Status:             mr.Status,
		CalibreFingerprint: mr.CalibreFingerprint,
		MatchDetailsJSON:   detailsJSON,
	}
}

// CalculateFingerprint produces a deterministic SHA256 digest of a CalibreBook's
// key metadata fields. If any metadata field, author, format, or identifier is modified in Calibre,
// the calculated fingerprint changes.
func CalculateFingerprint(b *CalibreBook) string {
	if b == nil {
		return ""
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("id:%d", b.CalibreID))
	parts = append(parts, fmt.Sprintf("title:%s", strings.TrimSpace(b.Title)))
	parts = append(parts, fmt.Sprintf("sort:%s", strings.TrimSpace(b.SortTitle)))
	parts = append(parts, fmt.Sprintf("isbn:%s", strings.TrimSpace(b.ISBN)))
	parts = append(parts, fmt.Sprintf("lang:%s", strings.TrimSpace(b.Language)))

	if b.Series != nil {
		parts = append(parts, fmt.Sprintf("series:%s#%.2f", strings.TrimSpace(b.Series.Name), b.Series.Position))
	}

	// Authors
	var authorParts []string
	for _, a := range b.Authors {
		authorParts = append(authorParts, fmt.Sprintf("%d:%s", a.CalibreID, strings.TrimSpace(a.Name)))
	}
	sort.Strings(authorParts)
	parts = append(parts, fmt.Sprintf("authors:%s", strings.Join(authorParts, "|")))

	// Identifiers
	var idParts []string
	for k, v := range b.Identifiers {
		idParts = append(idParts, fmt.Sprintf("%s=%s", cleanIdentifierType(k), cleanIdentifierValue(v)))
	}
	sort.Strings(idParts)
	parts = append(parts, fmt.Sprintf("ids:%s", strings.Join(idParts, "|")))

	// Formats
	var fmtParts []string
	for _, f := range b.Formats {
		fmtParts = append(fmtParts, fmt.Sprintf("%s:%d", strings.ToUpper(f.Format), f.SizeBytes))
	}
	sort.Strings(fmtParts)
	parts = append(parts, fmt.Sprintf("formats:%s", strings.Join(fmtParts, "|")))

	raw := strings.Join(parts, "\n")
	hash := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(hash[:])
}

// NormalizeTitle returns a cleaned, lowercase, punctuation-stripped representation of a title.
func NormalizeTitle(title string) string {
	title = strings.ToLower(title)
	title = punctuationRe.ReplaceAllString(title, " ")
	title = multiSpaceRe.ReplaceAllString(title, " ")
	return strings.TrimSpace(title)
}

// ExtractPrimaryTitle strips common subtitle separators (colon, dash, parenthesis)
// and returns the normalized primary title.
func ExtractPrimaryTitle(title string) string {
	raw := title
	if idx := strings.IndexAny(title, ":-("); idx > 0 {
		raw = title[:idx]
	}
	return NormalizeTitle(raw)
}

// NormalizeAuthor returns a cleaned, lowercase, punctuation-stripped representation of an author name.
// It handles "Last, First" formatting by converting to "First Last".
func NormalizeAuthor(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	// Handle "Last, First"
	if strings.Contains(name, ",") {
		parts := strings.SplitN(name, ",", 2)
		if len(parts) == 2 {
			name = strings.TrimSpace(parts[1]) + " " + strings.TrimSpace(parts[0])
		}
	}

	name = strings.ToLower(name)
	name = punctuationRe.ReplaceAllString(name, " ")
	name = multiSpaceRe.ReplaceAllString(name, " ")
	return strings.TrimSpace(name)
}

// AuthorMatches checks if two author names match after normalization (case, punctuation, whitespace, and Last, First conversion).
func AuthorMatches(authorA, authorB string) bool {
	normA := NormalizeAuthor(authorA)
	normB := NormalizeAuthor(authorB)
	if normA == "" || normB == "" {
		return false
	}
	return normA == normB
}

// AuthorTokensMatch checks if two author names match after normalization.
func AuthorTokensMatch(authorA, authorB string) bool {
	return AuthorMatches(authorA, authorB)
}

// ExtractWorkIdentifiers collects all provider identifiers available for a Bindery Book
// grouped by identifier type (e.g. "isbn", "asin", "openlibrary", "openlibrary_edition", "hardcover", "google", "goodreads", "dnb").
func ExtractWorkIdentifiers(book *models.Book) map[string][]string {
	if book == nil {
		return nil
	}

	out := make(map[string][]string)
	add := func(typ, val string) {
		cleanT := cleanIdentifierType(typ)
		cleanV := cleanIdentifierValue(val)
		if cleanT == "" || cleanV == "" {
			return
		}
		// Special handling for ISBN: remove hyphens
		if cleanT == "isbn" {
			cleanV = strings.ReplaceAll(cleanV, "-", "")
		}

		for _, existing := range out[cleanT] {
			if strings.EqualFold(existing, cleanV) {
				return
			}
		}
		out[cleanT] = append(out[cleanT], cleanV)
	}

	// Primary foreign_id & metadataProvider
	if book.ForeignID != "" {
		prov := strings.ToLower(strings.TrimSpace(book.MetadataProvider))
		fid := strings.TrimSpace(book.ForeignID)

		switch {
		case prov == "openlibrary" || (strings.HasPrefix(fid, "OL") && strings.HasSuffix(fid, "W")):
			add("openlibrary", normalizeOpenLibraryIdentifier(fid))
		case prov == "hardcover" || strings.HasPrefix(fid, "hc:"):
			add("hardcover", trimIdentifierPrefix(fid, "hc:"))
		case prov == "googlebooks" || prov == "google" || strings.HasPrefix(fid, "gb:"):
			add("google", trimIdentifierPrefix(fid, "gb:"))
		case prov == "dnb" || strings.HasPrefix(fid, "dnb:"):
			add("dnb", trimIdentifierPrefix(fid, "dnb:"))
		}
	}

	// Book ASIN
	if book.ASIN != "" {
		add("asin", book.ASIN)
	}

	// Book Identifiers join
	for _, id := range book.Identifiers {
		prov := strings.ToLower(strings.TrimSpace(id.Provider))
		val := strings.TrimSpace(id.ForeignID)
		switch prov {
		case "isbn":
			add("isbn", val)
		case "asin":
			add("asin", val)
		case "openlibrary", "openlibrary_work":
			add("openlibrary", normalizeOpenLibraryIdentifier(val))
		case "openlibrary_edition":
			add("openlibrary_edition", normalizeOpenLibraryIdentifier(val))
		case "hardcover":
			add("hardcover", trimIdentifierPrefix(val, "hc:"))
		case "google", "googlebooks":
			add("google", trimIdentifierPrefix(val, "gb:"))
		case "goodreads":
			add("goodreads", val)
		case "dnb":
			add("dnb", trimIdentifierPrefix(val, "dnb:"))
		}
	}

	// Editions
	for _, ed := range book.Editions {
		if ed.ISBN13 != nil {
			add("isbn", *ed.ISBN13)
		}
		if ed.ISBN10 != nil {
			add("isbn", *ed.ISBN10)
		}
		if ed.ASIN != nil {
			add("asin", *ed.ASIN)
		}
		if ed.ForeignID != "" && strings.HasPrefix(ed.ForeignID, "OL") && strings.HasSuffix(ed.ForeignID, "M") {
			add("openlibrary_edition", normalizeOpenLibraryIdentifier(ed.ForeignID))
		}
	}

	// ProviderISBNs
	for _, isbn := range book.ProviderISBNs {
		add("isbn", isbn)
	}

	return out
}

// LibraryIndex builds fast in-memory lookup maps for bulk matching across large Calibre libraries (~80,000 books).
type LibraryIndex struct {
	byIdentifier   map[string][]CalibreBook // key: "type:val"
	byNormTitle    map[string][]CalibreBook // key: normTitle
	byPrimaryTitle map[string][]CalibreBook // key: normPrimaryTitle
	byCalibreID    map[int64]CalibreBook
}

// NewLibraryIndex constructs a LibraryIndex from a list of CalibreBook records.
func NewLibraryIndex(books []CalibreBook) *LibraryIndex {
	idx := &LibraryIndex{
		byIdentifier:   make(map[string][]CalibreBook),
		byNormTitle:    make(map[string][]CalibreBook),
		byPrimaryTitle: make(map[string][]CalibreBook),
		byCalibreID:    make(map[int64]CalibreBook, len(books)),
	}

	for _, b := range books {
		idx.byCalibreID[b.CalibreID] = b

		// Index identifiers
		for k, v := range b.Identifiers {
			cleanT := cleanIdentifierType(k)
			cleanV := cleanIdentifierValue(v)
			if cleanT != "" && cleanV != "" {
				if cleanT == "isbn" {
					cleanV = strings.ReplaceAll(cleanV, "-", "")
				}
				key := cleanT + ":" + strings.ToLower(cleanV)
				idx.byIdentifier[key] = append(idx.byIdentifier[key], b)
			}
		}
		if b.ISBN != "" {
			cleanV := strings.ReplaceAll(cleanIdentifierValue(b.ISBN), "-", "")
			if cleanV != "" {
				key := "isbn:" + strings.ToLower(cleanV)
				// Avoid duplicate if already in Identifiers
				already := false
				for _, existing := range idx.byIdentifier[key] {
					if existing.CalibreID == b.CalibreID {
						already = true
						break
					}
				}
				if !already {
					idx.byIdentifier[key] = append(idx.byIdentifier[key], b)
				}
			}
		}

		// Index title
		normT := NormalizeTitle(b.Title)
		if normT != "" {
			idx.byNormTitle[normT] = append(idx.byNormTitle[normT], b)
		}
		primaryT := ExtractPrimaryTitle(b.Title)
		if primaryT != "" {
			idx.byPrimaryTitle[primaryT] = append(idx.byPrimaryTitle[primaryT], b)
		}
	}

	return idx
}

// FindByIdentifier searches the index for books matching (idType, idVal).
func (idx *LibraryIndex) FindByIdentifier(idType, idVal string) []CalibreBook {
	cleanT := cleanIdentifierType(idType)
	cleanV := cleanIdentifierValue(idVal)
	if cleanT == "" || cleanV == "" {
		return nil
	}
	if cleanT == "isbn" {
		cleanV = strings.ReplaceAll(cleanV, "-", "")
	}
	key := cleanT + ":" + strings.ToLower(cleanV)
	return idx.byIdentifier[key]
}

// GetBook returns a CalibreBook by its Calibre ID.
func (idx *LibraryIndex) GetBook(calibreID int64) (*CalibreBook, bool) {
	b, ok := idx.byCalibreID[calibreID]
	if !ok {
		return nil, false
	}
	return &b, true
}

// MatchWork matches a single Bindery Book against the pre-built LibraryIndex.
func (idx *LibraryIndex) MatchWork(book *models.Book) *MatchResult {
	if book == nil {
		return &MatchResult{
			Status:      "unmatched",
			Confidence:  "none",
			MatchMethod: "none",
		}
	}

	// 1. Try identifier matching
	workIDs := ExtractWorkIdentifiers(book)
	matchedCandidates := make(map[int64]CalibreBook)
	matchedTypes := make(map[string]string) // calibreID -> "type:val"

	// Priority order for evaluation details
	idPriority := []string{"isbn", "asin", "openlibrary", "openlibrary_edition", "hardcover", "google", "goodreads", "dnb"}

	for _, typ := range idPriority {
		vals := workIDs[typ]
		for _, val := range vals {
			found := idx.FindByIdentifier(typ, val)
			for _, cb := range found {
				if _, ok := matchedCandidates[cb.CalibreID]; !ok {
					matchedCandidates[cb.CalibreID] = cb
					matchedTypes[fmt.Sprintf("%d", cb.CalibreID)] = typ + ":" + val
				}
			}
		}
	}

	// Evaluate identifier results
	if len(matchedCandidates) == 1 {
		var cb CalibreBook
		var cid int64
		for id, b := range matchedCandidates {
			cid = id
			cb = b
		}
		idMatch := matchedTypes[fmt.Sprintf("%d", cid)]
		idType := strings.Split(idMatch, ":")[0]

		return &MatchResult{
			BookID:             book.ID,
			CalibreID:          cid,
			MatchMethod:        "identifier:" + idType,
			Confidence:         models.CalibreMatchConfidenceExact,
			Status:             models.CalibreMatchStatusMatched,
			CalibreFingerprint: CalculateFingerprint(&cb),
			MatchedIdentifier:  idMatch,
			MatchDetails: map[string]any{
				"matched_identifier": idMatch,
			},
		}
	} else if len(matchedCandidates) > 1 {
		var candIDs []int64
		for id := range matchedCandidates {
			candIDs = append(candIDs, id)
		}
		sort.Slice(candIDs, func(i, j int) bool { return candIDs[i] < candIDs[j] })

		return &MatchResult{
			BookID:       book.ID,
			CalibreID:    0,
			MatchMethod:  "identifier:ambiguous",
			Confidence:   models.CalibreMatchConfidenceAmbiguous,
			Status:       models.CalibreMatchStatusAmbiguous,
			CandidateIDs: candIDs,
			MatchDetails: map[string]any{
				"ambiguous_reason": "multiple calibre books matched work identifiers",
				"candidates":       candIDs,
			},
		}
	}

	// 2. Conservative fallback matching (Title + Author)
	binderyNormTitle := NormalizeTitle(book.Title)
	binderyPrimaryTitle := ExtractPrimaryTitle(book.Title)

	var binderyAuthorName string
	if book.Author != nil {
		binderyAuthorName = book.Author.Name
	}

	if binderyNormTitle == "" || binderyAuthorName == "" {
		return &MatchResult{
			BookID:      book.ID,
			CalibreID:   0,
			MatchMethod: "none",
			Confidence:  "none",
			Status:      "unmatched",
		}
	}

	// Search candidates matching normalized title or primary title
	candidateMap := make(map[int64]CalibreBook)
	addCandidates := func(titleKey string) {
		if titleKey == "" {
			return
		}
		for _, cb := range idx.byNormTitle[titleKey] {
			candidateMap[cb.CalibreID] = cb
		}
		for _, cb := range idx.byPrimaryTitle[titleKey] {
			candidateMap[cb.CalibreID] = cb
		}
	}

	addCandidates(binderyNormTitle)
	addCandidates(binderyPrimaryTitle)

	// Filter candidates by author match
	var fallbackCandidates []CalibreBook
	for _, cb := range candidateMap {
		authorMatch := false
		for _, ca := range cb.Authors {
			if AuthorMatches(binderyAuthorName, ca.Name) {
				authorMatch = true
				break
			}
		}
		if authorMatch {
			fallbackCandidates = append(fallbackCandidates, cb)
		}
	}

	if len(fallbackCandidates) == 1 {
		cb := fallbackCandidates[0]
		return &MatchResult{
			BookID:             book.ID,
			CalibreID:          cb.CalibreID,
			MatchMethod:        "fallback_title_author",
			Confidence:         models.CalibreMatchConfidenceMedium,
			Status:             models.CalibreMatchStatusMatched,
			CalibreFingerprint: CalculateFingerprint(&cb),
		}
	} else if len(fallbackCandidates) > 1 {
		var candIDs []int64
		for _, cb := range fallbackCandidates {
			candIDs = append(candIDs, cb.CalibreID)
		}
		sort.Slice(candIDs, func(i, j int) bool { return candIDs[i] < candIDs[j] })

		return &MatchResult{
			BookID:       book.ID,
			CalibreID:    0,
			MatchMethod:  "fallback_title_author",
			Confidence:   models.CalibreMatchConfidenceAmbiguous,
			Status:       models.CalibreMatchStatusAmbiguous,
			CandidateIDs: candIDs,
			MatchDetails: map[string]any{
				"ambiguous_reason": "multiple calibre books matched conservative title/author fallback",
				"candidates":       candIDs,
			},
		}
	}

	return &MatchResult{
		BookID:      book.ID,
		CalibreID:   0,
		MatchMethod: "none",
		Confidence:  "none",
		Status:      "unmatched",
	}
}

// MatchWork evaluates a Bindery Book against an AuthoritativeLibrary.
func MatchWork(ctx context.Context, book *models.Book, authLib AuthoritativeLibrary) (*MatchResult, error) {
	if book == nil {
		return &MatchResult{Status: "unmatched", Confidence: "none", MatchMethod: "none"}, nil
	}
	if authLib == nil {
		return nil, errors.New("nil authoritative library")
	}

	// For single-work match, we can query by identifier first
	workIDs := ExtractWorkIdentifiers(book)
	matchedCandidates := make(map[int64]CalibreBook)
	matchedTypes := make(map[string]string)

	idPriority := []string{"isbn", "asin", "openlibrary", "openlibrary_edition", "hardcover", "google", "goodreads", "dnb"}

	for _, typ := range idPriority {
		vals := workIDs[typ]
		for _, val := range vals {
			found, err := authLib.FindByIdentifier(ctx, typ, val)
			if err != nil {
				return nil, fmt.Errorf("find by identifier (%s=%s): %w", typ, val, err)
			}
			for _, cb := range found {
				if _, ok := matchedCandidates[cb.CalibreID]; !ok {
					matchedCandidates[cb.CalibreID] = cb
					matchedTypes[fmt.Sprintf("%d", cb.CalibreID)] = typ + ":" + val
				}
			}
		}
	}

	if len(matchedCandidates) == 1 {
		var cb CalibreBook
		var cid int64
		for id, b := range matchedCandidates {
			cid = id
			cb = b
		}
		idMatch := matchedTypes[fmt.Sprintf("%d", cid)]
		idType := strings.Split(idMatch, ":")[0]

		return &MatchResult{
			BookID:             book.ID,
			CalibreID:          cid,
			MatchMethod:        "identifier:" + idType,
			Confidence:         models.CalibreMatchConfidenceExact,
			Status:             models.CalibreMatchStatusMatched,
			CalibreFingerprint: CalculateFingerprint(&cb),
			MatchedIdentifier:  idMatch,
			MatchDetails: map[string]any{
				"matched_identifier": idMatch,
			},
		}, nil
	} else if len(matchedCandidates) > 1 {
		var candIDs []int64
		for id := range matchedCandidates {
			candIDs = append(candIDs, id)
		}
		sort.Slice(candIDs, func(i, j int) bool { return candIDs[i] < candIDs[j] })

		return &MatchResult{
			BookID:       book.ID,
			CalibreID:    0,
			MatchMethod:  "identifier:ambiguous",
			Confidence:   models.CalibreMatchConfidenceAmbiguous,
			Status:       models.CalibreMatchStatusAmbiguous,
			CandidateIDs: candIDs,
			MatchDetails: map[string]any{
				"ambiguous_reason": "multiple calibre books matched work identifiers",
				"candidates":       candIDs,
			},
		}, nil
	}

	// Conservative fallback matching requires checking library books
	allBooks, err := authLib.AllBooks(ctx)
	if err != nil {
		return nil, fmt.Errorf("load all books for fallback match: %w", err)
	}

	idx := NewLibraryIndex(allBooks)
	return idx.MatchWork(book), nil
}

// RevalidateCrossReference checks whether a persisted cross-reference is still valid.
// It returns the updated cross-reference, a boolean indicating if it is stale, and any error.
func RevalidateCrossReference(ctx context.Context, ref *models.CalibreWorkCrossReference, book *models.Book, authLib AuthoritativeLibrary) (*models.CalibreWorkCrossReference, bool, error) {
	if ref == nil {
		return nil, false, errors.New("nil cross reference")
	}
	if authLib == nil {
		return nil, false, errors.New("nil authoritative library")
	}

	updated := *ref

	// Persisted ambiguous results must not look up Calibre ID 0. Rerun matching instead.
	if ref.Status == models.CalibreMatchStatusAmbiguous || ref.CalibreID == 0 {
		if book == nil {
			return &updated, false, nil
		}
		matchRes, err := MatchWork(ctx, book, authLib)
		if err != nil {
			return nil, false, fmt.Errorf("re-match work for ambiguous reference: %w", err)
		}

		switch matchRes.Status {
		case models.CalibreMatchStatusMatched:
			newRef := matchRes.ToCrossReference()
			if newRef == nil {
				updated.Status = models.CalibreMatchStatusStale
				updated.MatchDetailsJSON = `{"stale_reason":"revalidation produced invalid match"}`
				return &updated, true, nil
			}
			updated.CalibreID = newRef.CalibreID
			updated.MatchMethod = newRef.MatchMethod
			updated.Confidence = newRef.Confidence
			updated.Status = models.CalibreMatchStatusMatched
			updated.CalibreFingerprint = newRef.CalibreFingerprint
			updated.MatchDetailsJSON = newRef.MatchDetailsJSON
			return &updated, false, nil

		case models.CalibreMatchStatusAmbiguous:
			newRef := matchRes.ToCrossReference()
			detailsJSON := "{}"
			matchMethod := matchRes.MatchMethod
			if newRef != nil {
				detailsJSON = newRef.MatchDetailsJSON
				matchMethod = newRef.MatchMethod
			}
			updated.MatchMethod = matchMethod
			updated.MatchDetailsJSON = detailsJSON
			return &updated, false, nil

		default: // "unmatched"
			updated.Status = models.CalibreMatchStatusStale
			updated.MatchDetailsJSON = `{"stale_reason":"no longer matches calibre library"}`
			return &updated, true, nil
		}
	}

	// 1. Check if Calibre book still exists (for matched cross-references with CalibreID > 0)
	cb, err := authLib.GetBook(ctx, ref.CalibreID)
	if err != nil {
		if errors.Is(err, ErrBookNotFound) {
			updated.Status = models.CalibreMatchStatusStale
			updated.MatchDetailsJSON = `{"stale_reason":"calibre book no longer exists"}`
			return &updated, true, nil
		}
		return nil, false, fmt.Errorf("get calibre book %d: %w", ref.CalibreID, err)
	}

	// 2. Compute current fingerprint
	currentFP := CalculateFingerprint(cb)
	if currentFP == ref.CalibreFingerprint {
		// No material change
		return &updated, false, nil
	}

	// 3. Metadata changed in Calibre — re-run matcher if book is available
	if book != nil {
		matchRes, err := MatchWork(ctx, book, authLib)
		if err == nil {
			if matchRes.Status == models.CalibreMatchStatusMatched && matchRes.CalibreID == ref.CalibreID {
				// Still matches the same Calibre book! Update fingerprint and details.
				updated.CalibreFingerprint = currentFP
				updated.MatchMethod = matchRes.MatchMethod
				updated.Confidence = matchRes.Confidence
				updated.Status = models.CalibreMatchStatusMatched
				updated.MatchDetailsJSON = matchRes.ToCrossReference().MatchDetailsJSON
				return &updated, false, nil
			} else if matchRes.Status == models.CalibreMatchStatusMatched && matchRes.CalibreID != ref.CalibreID {
				// Matches a different Calibre book
				updated.CalibreID = matchRes.CalibreID
				updated.CalibreFingerprint = matchRes.CalibreFingerprint
				updated.MatchMethod = matchRes.MatchMethod
				updated.Confidence = matchRes.Confidence
				updated.Status = models.CalibreMatchStatusMatched
				updated.MatchDetailsJSON = matchRes.ToCrossReference().MatchDetailsJSON
				return &updated, false, nil
			} else if matchRes.Status == models.CalibreMatchStatusAmbiguous {
				// Now ambiguous
				updated.CalibreID = 0
				updated.CalibreFingerprint = ""
				updated.MatchMethod = matchRes.MatchMethod
				updated.Confidence = matchRes.Confidence
				updated.Status = models.CalibreMatchStatusAmbiguous
				updated.MatchDetailsJSON = matchRes.ToCrossReference().MatchDetailsJSON
				return &updated, false, nil
			}
		}
	}

	// Matched differently or no longer matches
	updated.Status = models.CalibreMatchStatusStale
	updated.MatchDetailsJSON = fmt.Sprintf(`{"stale_reason":"calibre metadata changed","previous_fingerprint":%q,"current_fingerprint":%q}`, ref.CalibreFingerprint, currentFP)
	return &updated, true, nil
}
