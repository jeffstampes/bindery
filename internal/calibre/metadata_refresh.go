package calibre

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

const metadataRefreshSetting = "calibre.metadata_refresh_enabled"

var (
	ErrMetadataRefreshDisabled   = errors.New("calibre metadata refresh requires its separate opt-in")
	ErrMetadataRefreshStale      = errors.New("metadata refresh proposal is stale; preview again")
	ErrMetadataRefreshIneligible = errors.New("metadata refresh ownership is ineligible")
)

// MetadataRefreshEligibility is a read-only hint about the persisted ownership
// link. Preview and apply always revalidate live evidence before any lookup/write.
type MetadataRefreshEligibility struct {
	Status      string `json:"status"` // eligible, ineligible, stale
	Reason      string `json:"reason,omitempty"`
	MatchMethod string `json:"matchMethod,omitempty"`
	Confidence  string `json:"confidence,omitempty"`
}

// MetadataRefreshOwnershipError preserves the policy reason for operators while
// allowing API callers to distinguish ineligible ownership from stale state.
type MetadataRefreshOwnershipError struct{ Eligibility MetadataRefreshEligibility }

func (e *MetadataRefreshOwnershipError) Error() string { return e.Eligibility.Reason }
func (e *MetadataRefreshOwnershipError) Unwrap() error { return ErrMetadataRefreshIneligible }

func refreshOwnershipEligibility(ref *models.CalibreWorkCrossReference) MetadataRefreshEligibility {
	if ref == nil || ref.Status != models.CalibreMatchStatusMatched || ref.CalibreID <= 0 || ref.CalibreFingerprint == "" {
		return MetadataRefreshEligibility{Status: "stale", Reason: ErrMetadataRefreshStale.Error()}
	}
	result := MetadataRefreshEligibility{Status: "eligible", MatchMethod: ref.MatchMethod, Confidence: ref.Confidence}
	if ref.Confidence != models.CalibreMatchConfidenceExact && ref.Confidence != models.CalibreMatchConfidenceHigh {
		result.Status = "ineligible"
		result.Reason = "Metadata refresh requires an identifier-confirmed ownership match. "
		if ref.MatchMethod == "fallback_title_author" {
			result.Reason += "This book is currently matched by title and author."
		} else {
			result.Reason += fmt.Sprintf("This book is currently matched by %s (%s confidence).", ref.MatchMethod, ref.Confidence)
		}
	}
	return result
}

// MetadataRefreshEligibility reports the current persisted ownership policy
// without fetching metadata. It is advisory; the preview/apply gates remain authoritative.
func (s *AuthoritativeService) MetadataRefreshEligibility(ctx context.Context, bookID int64) (MetadataRefreshEligibility, error) {
	allowed, err := s.metadataRefreshEnabled(ctx)
	if err != nil {
		return MetadataRefreshEligibility{}, err
	}
	if !allowed {
		return MetadataRefreshEligibility{}, ErrMetadataRefreshDisabled
	}
	ref, err := s.crossRef.GetByBookID(ctx, bookID)
	if err != nil {
		return MetadataRefreshEligibility{}, err
	}
	return refreshOwnershipEligibility(ref), nil
}

// MetadataRefreshField describes one current/fetched value and its eligibility.
// This first slice deliberately writes only title and publisher. Other fields
// and ALL identifiers remain visible but withheld, including unknown CWA IDs.
// #28 is still the only path for adding a work identifier. A fetched OPF is
// evidence, never a set_metadata payload.
type MetadataRefreshField struct {
	Name    string `json:"name"`
	Current string `json:"current"`
	Fetched string `json:"fetched"`
	Status  string `json:"status"` // change, unchanged, withheld, missing
	Reason  string `json:"reason,omitempty"`
}

type MetadataRefreshProposal struct {
	ID                  int64                            `json:"id"`
	BookID              int64                            `json:"bookId"`
	CalibreID           int64                            `json:"calibreId"`
	RootKey             string                           `json:"rootKey"`
	EvidenceKey         string                           `json:"evidenceKey"`
	Edition             models.CalibreEditionResolution  `json:"edition"`
	Ownership           models.CalibreWorkCrossReference `json:"-"`
	LibraryPath         string                           `json:"-"`
	BinaryPath          string                           `json:"-"`
	LookupISBN          string                           `json:"lookupIsbn"`
	CurrentIdentifiers  map[string]string                `json:"currentIdentifiers"`
	ProposedIdentifiers map[string]string                `json:"proposedIdentifiers"`
	Fields              []MetadataRefreshField           `json:"fields"`
	FetchedOPF          string                           `json:"-"`
	FetchedOPFDigest    string                           `json:"fetchedOpfDigest,omitempty"`
	CurrentOPFDigest    string                           `json:"-"`
	CurrentFingerprint  string                           `json:"-"`
	SourceDigest        string                           `json:"sourceDigest"`
	LookupLog           string                           `json:"lookupLog,omitempty"`
	Status              string                           `json:"status"`
	Reason              string                           `json:"reason,omitempty"`
	Version             int                              `json:"version"`
	Fingerprint         string                           `json:"fingerprint"`
}

// refreshEnvelope is persisted whole (including OPFs), whereas the public
// proposal hides raw OPFs and the pre-write snapshot remains in the attempt.
type refreshEnvelope struct {
	Proposal           MetadataRefreshProposal          `json:"proposal"`
	FetchedOPF         string                           `json:"fetchedOpf"`
	CurrentOPFDigest   string                           `json:"currentOpfDigest"`
	CurrentFingerprint string                           `json:"currentFingerprint"`
	Ownership          models.CalibreWorkCrossReference `json:"ownership"`
	LibraryPath        string                           `json:"libraryPath"`
	BinaryPath         string                           `json:"binaryPath"`
}

func refreshDigest(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// Tie approval to the complete rooted evidence and the source work claims
// used by the matcher, not merely its stable root key and edition result.
func refreshSourceDigest(book *models.Book, snapshot *models.CalibreIdentitySnapshot) (string, error) {
	source := struct {
		Provider    string
		ForeignID   string
		Title       string
		Author      *models.Author
		Identifiers []models.BookIdentifier
		Editions    []models.Edition
		Evidence    []models.CalibreIdentityEvidence
		Lookups     []models.CalibreIdentityLookup
		Artifacts   []models.CalibreArtifactScan
		History     []models.CalibreArtifactScan
		Writebacks  []models.CalibreArtifactWriteback
		CheckedAt   time.Time
	}{book.MetadataProvider, book.ForeignID, book.Title, book.Author, book.Identifiers, book.Editions,
		snapshot.Evidence, snapshot.Lookups, snapshot.Artifacts, snapshot.ArtifactHistory, snapshot.Writebacks, snapshot.CheckedAt}
	encoded, err := json.Marshal(source)
	if err != nil {
		return "", fmt.Errorf("fingerprint refresh source evidence: %w", err)
	}
	return refreshDigest(string(encoded)), nil
}

type metadataRefreshCLI interface {
	Fetch(context.Context, string, string) (string, string, error)
	Show(context.Context, string, string, int64) (string, error)
	Set(context.Context, string, string, int64, string, string) error
}

type calibreMetadataCLI struct{}

type boundedOutput struct {
	bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("calibre command output exceeds %d bytes", b.limit)
	}
	return b.Buffer.Write(p)
}

func runCalibreCommand(ctx context.Context, name string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// The executable is the administrator's Calibre binary setting (or its
	// sibling fetch command), never a request-supplied path or shell string.
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: configured Calibre executable, arguments passed without shell expansion
	out, log := &boundedOutput{limit: 256 << 10}, &boundedOutput{limit: 16 << 10}
	cmd.Stdout, cmd.Stderr = out, log
	err := cmd.Run()
	if err != nil {
		return "", log.String(), fmt.Errorf("calibre command %s failed: %w: %s", filepath.Base(name), err, log.String())
	}
	return out.String(), log.String(), nil
}

func (calibreMetadataCLI) Fetch(ctx context.Context, binary, isbn string) (string, string, error) {
	fetch := "fetch-ebook-metadata"
	if filepath.IsAbs(binary) {
		fetch = filepath.Join(filepath.Dir(binary), fetch)
	}
	return runCalibreCommand(ctx, fetch, "--opf", "--verbose", "--isbn", isbn)
}
func (calibreMetadataCLI) Show(ctx context.Context, binary, library string, id int64) (string, error) {
	out, _, err := runCalibreCommand(ctx, binary, "--with-library", library, "show_metadata", "--as-opf", strconv.FormatInt(id, 10))
	return out, err
}
func (calibreMetadataCLI) Set(ctx context.Context, binary, library string, id int64, field, value string) error {
	_, _, err := runCalibreCommand(ctx, binary, "--with-library", library, "set_metadata", strconv.FormatInt(id, 10), "--field", field+":"+value)
	return err
}

func (s *AuthoritativeService) WithMetadataRefresh(repo *db.CalibreMetadataRefreshRepo) *AuthoritativeService {
	s.metadataRefresh = repo
	return s
}

func (s *AuthoritativeService) metadataRefreshEnabled(ctx context.Context) (bool, error) {
	if !s.IsEnabled(ctx) || s.metadataRefresh == nil || s.identity == nil || s.crossRef == nil || s.books == nil || s.editions == nil {
		return false, nil
	}
	setting, err := s.settings.Get(ctx, metadataRefreshSetting)
	if err != nil {
		return false, fmt.Errorf("read metadata refresh setting: %w", err)
	}
	return setting != nil && setting.Value == "true", nil
}

func (s *AuthoritativeService) metadataCLI() metadataRefreshCLI {
	if s.refreshCLI != nil {
		return s.refreshCLI
	}
	return calibreMetadataCLI{}
}

// refreshState requires the *same* live ownership matcher and rooted work
// evidence as the audit. Neither the lookup OPF nor a Calibre claim can alter
// this identity. Called for both preview and apply, not just a cached link.
func (s *AuthoritativeService) refreshState(ctx context.Context, bookID int64) (*MetadataRefreshProposal, *models.CalibreIdentitySnapshot, *CalibreBook, error) {
	ref, err := s.crossRef.GetByBookID(ctx, bookID)
	if err != nil {
		return nil, nil, nil, err
	}
	eligibility := refreshOwnershipEligibility(ref)
	switch eligibility.Status {
	case "stale":
		return nil, nil, nil, ErrMetadataRefreshStale
	case "ineligible":
		return nil, nil, nil, &MetadataRefreshOwnershipError{Eligibility: eligibility}
	}
	book, err := s.books.GetByID(ctx, bookID)
	if err != nil {
		return nil, nil, nil, err
	}
	if book == nil || book.Excluded || !book.WantsEbook() || !auditExternalBook(book) {
		return nil, nil, nil, ErrMetadataRefreshStale
	}
	book.Identifiers, err = s.books.ListBookIdentifiers(ctx, bookID)
	if err != nil {
		return nil, nil, nil, err
	}
	book.Editions, err = s.editions.ListByBook(ctx, bookID)
	if err != nil {
		return nil, nil, nil, err
	}
	snapshot, err := s.identity.ListByBookID(ctx, bookID)
	if err != nil {
		return nil, nil, nil, err
	}
	if snapshot == nil || snapshot.CalibreID != ref.CalibreID || snapshot.RootKey != identityRootKey(book) ||
		snapshot.CheckedAt.IsZero() || time.Since(snapshot.CheckedAt) > identityFreshFor {
		return nil, nil, nil, ErrMetadataRefreshStale
	}
	library := s.LibraryPath(ctx)
	reader, err := OpenReader(library)
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() { _ = reader.Close() }()
	cb, err := reader.GetBook(ctx, ref.CalibreID)
	if err != nil {
		return nil, nil, nil, err
	}
	// The persisted exact/high link authorizes the workflow; also confirm it
	// still matches under the ownership matcher. The stripped audit match is a
	// separate, anti-circular check of the target, not a write-confidence gate.
	current, err := MatchWork(ctx, book, reader)
	if err != nil {
		return nil, nil, nil, err
	}
	if current.Status != models.CalibreMatchStatusMatched || current.CalibreID != cb.CalibreID ||
		refreshOwnershipEligibility(current.ToCrossReference()).Status != "eligible" {
		return nil, nil, nil, ErrMetadataRefreshStale
	}
	match, err := MatchWork(ctx, auditMatchEvidence(book), reader)
	if err != nil {
		return nil, nil, nil, err
	}
	if match.Status != models.CalibreMatchStatusMatched || match.CalibreID != cb.CalibreID ||
		CalculateFingerprint(cb) != ref.CalibreFingerprint {
		return nil, nil, nil, ErrMetadataRefreshStale
	}
	root := ""
	for _, e := range snapshot.Evidence {
		if e.Status == models.CalibreIdentityRoot && e.Method == metadata.RawMethodExactBook &&
			e.CanonicalIdentity == snapshot.RootKey && e.Provider == identityCanonicalProvider(book.MetadataProvider) &&
			e.ForeignID == book.ForeignID && e.Seed == book.ForeignID && e.EditionID == "" {
			root = e.Key
			break
		}
	}
	if root == "" {
		return nil, nil, nil, ErrMetadataRefreshStale
	}
	if err := s.attachArtifacts(ctx, snapshot); err != nil {
		return nil, nil, nil, err
	}
	snapshot.Edition = ResolveOwnedEdition(*snapshot, cb)
	sourceDigest, err := refreshSourceDigest(book, snapshot)
	if err != nil {
		return nil, nil, nil, err
	}
	binary := "calibredb"
	if setting, err := s.settings.Get(ctx, "calibre.binary_path"); err != nil {
		return nil, nil, nil, err
	} else if setting != nil && setting.Value != "" {
		binary = setting.Value
	}
	return &MetadataRefreshProposal{BookID: bookID, CalibreID: cb.CalibreID,
		RootKey: snapshot.RootKey, EvidenceKey: root, Edition: snapshot.Edition, Ownership: *ref,
		CurrentIdentifiers: cb.Identifiers, ProposedIdentifiers: cb.Identifiers,
		CurrentFingerprint: CalculateFingerprint(cb), SourceDigest: sourceDigest, LibraryPath: library, BinaryPath: binary, Version: 1}, snapshot, cb, nil
}

// lookupISBN uses only exact rooted work ISBNs or a uniquely selected rooted
// edition ISBN. Multiple possible ISBNs are not an implicit selection.
func lookupISBN(p *MetadataRefreshProposal, snapshot *models.CalibreIdentitySnapshot) string {
	var ids []string
	if p.Edition.Confidence == "exact" || (p.Edition.Confidence == "high" && p.Edition.ReasonCode == models.CalibreEditionReasonHistoricFile) {
		for _, e := range snapshot.Evidence {
			if e.Key == p.EvidenceKey || e.Status != models.CalibreIdentityRoot || e.Method != metadata.RawMethodExactEditions ||
				e.CanonicalIdentity != p.RootKey || e.EditionID != p.Edition.EditionID || e.Provider != p.Edition.Provider {
				continue
			}
			ids = append(ids, e.NormalizedIdentifiers["isbn"]...)
		}
	} else {
		for _, e := range snapshot.Evidence {
			if e.Key == p.EvidenceKey {
				ids = append(ids, e.NormalizedIdentifiers["isbn"]...)
			}
		}
	}
	return uniqueCanonicalISBN(ids)
}

// uniqueCanonicalISBN treats checksum-valid ISBN-10 and its ISBN-13 form as
// one rooted identity, without selecting between genuinely different books.
func uniqueCanonicalISBN(ids []string) string {
	canonical := make([]string, 0, len(ids))
	for _, id := range ids {
		if isbn := isbnutil.ToISBN13(id); isbn != "" {
			canonical = append(canonical, isbn)
		}
	}
	slices.Sort(canonical)
	canonical = slices.Compact(canonical)
	if len(canonical) != 1 {
		return ""
	}
	return canonical[0]
}

// opfFields reads only display fields and a checksum-valid ISBN. Unknown XML
// content is never round-tripped into the write; it stays in the frozen raw OPF.
func opfFields(raw string) (map[string]string, []string, error) {
	fields := make(map[string]string)
	var isbns []string
	decoder := xml.NewDecoder(strings.NewReader(raw))
	var element string
	var identifierScheme string
	for {
		tok, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("decode Calibre OPF: %w", err)
		}
		switch v := tok.(type) {
		case xml.StartElement:
			element, identifierScheme = v.Name.Local, ""
			if element == "meta" {
				name, value := "", ""
				for _, a := range v.Attr {
					if a.Name.Local == "name" {
						name = a.Value
					}
					if a.Name.Local == "content" {
						value = a.Value
					}
				}
				if name == "calibre:series" {
					fields["series"] = value
				}
				if name == "calibre:series_index" {
					fields["series_index"] = value
				}
				// Custom column declarations are part of the stored metadata
				// record. They are never writable here and must survive a CLI edit.
				if strings.HasPrefix(name, "calibre:user_metadata:") {
					fields[name] = value
				}
			}
			if element == "identifier" {
				for _, a := range v.Attr {
					if a.Name.Local == "scheme" {
						identifierScheme = a.Value
					}
				}
			}
		case xml.CharData:
			value := strings.TrimSpace(string(v))
			if value == "" {
				continue
			}
			switch element {
			case "title", "creator", "publisher", "date", "language", "subject", "description":
				key := map[string]string{"creator": "authors", "date": "pubdate", "language": "languages", "subject": "tags", "description": "comments"}[element]
				if key == "" {
					key = element
				}
				if fields[key] != "" && (key == "authors" || key == "tags") {
					fields[key] += ", " + value
				} else if fields[key] == "" {
					fields[key] = value
				}
			case "identifier":
				fields["identifier:"+strings.ToLower(identifierScheme)+":"+value] = value
				if strings.EqualFold(identifierScheme, "ISBN") {
					if isbn := isbnutil.ToISBN13(value); isbn != "" {
						isbns = append(isbns, isbn)
					}
				}
			}
		case xml.EndElement:
			element, identifierScheme = "", ""
		}
	}
	return fields, isbns, nil
}

var refreshFieldNames = []string{"title", "authors", "publisher", "pubdate", "languages", "series", "series_index", "tags", "comments", "cover"}

func refreshFields(p *MetadataRefreshProposal, snapshot *models.CalibreIdentitySnapshot, current, fetched map[string]string) {
	var rootTitle, editionTitle string
	for _, e := range snapshot.Evidence {
		if e.Key == p.EvidenceKey {
			rootTitle, _ = e.ProviderMetadata["title"].(string)
		}
		if e.Status == models.CalibreIdentityRoot && e.Method == metadata.RawMethodExactEditions && e.EditionID == p.Edition.EditionID && e.Provider == p.Edition.Provider {
			editionTitle, _ = e.ProviderMetadata["title"].(string)
		}
	}
	editionEligible := p.Edition.Confidence == "exact" || (p.Edition.Confidence == "high" && p.Edition.ReasonCode == models.CalibreEditionReasonHistoricFile)
	for _, name := range refreshFieldNames {
		field := MetadataRefreshField{Name: name, Current: current[name], Fetched: fetched[name], Status: "withheld", Reason: "Not writable in the first conservative refresh slice."}
		if field.Fetched == "" {
			field.Status, field.Reason = "missing", "The metadata provider omitted this field; existing metadata stays unchanged."
		}
		if field.Fetched != "" && field.Fetched == field.Current {
			field.Status, field.Reason = "unchanged", ""
		}
		if name == "title" && field.Fetched != "" && field.Fetched != field.Current {
			if (rootTitle != "" && textutil.FoldForTitleMatch(field.Fetched) == textutil.FoldForTitleMatch(rootTitle)) ||
				(editionEligible && editionTitle != "" && textutil.FoldForTitleMatch(field.Fetched) == textutil.FoldForTitleMatch(editionTitle)) {
				field.Status, field.Reason = "change", ""
			} else {
				field.Reason = "Fetched title does not match the established work or eligible owned edition."
			}
		}
		if name == "publisher" && field.Fetched != "" && field.Fetched != field.Current {
			if editionEligible {
				field.Status, field.Reason = "change", ""
			} else {
				field.Reason = "Publisher needs an eligible owned-edition resolution."
			}
		}
		p.Fields = append(p.Fields, field)
	}
}

func (s *AuthoritativeService) PreviewMetadataRefresh(ctx context.Context, bookID int64) (*MetadataRefreshProposal, error) {
	s.passMu.Lock()
	defer s.passMu.Unlock()
	allowed, err := s.metadataRefreshEnabled(ctx)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrMetadataRefreshDisabled
	}
	p, snapshot, _, err := s.refreshState(ctx, bookID)
	if err != nil {
		return nil, err
	}
	p.LookupISBN = lookupISBN(p, snapshot)
	p.Status = "ineligible"
	if p.LookupISBN == "" {
		p.Reason = "No unique checksum-valid ISBN from rooted work or eligible owned-edition evidence."
	}
	cli := s.metadataCLI()
	before, showErr := cli.Show(ctx, p.BinaryPath, p.LibraryPath, p.CalibreID)
	if showErr != nil {
		return nil, showErr
	}
	if len(before) > 256<<10 || before == "" {
		return nil, fmt.Errorf("invalid calibre pre-preview OPF")
	}
	p.CurrentOPFDigest = refreshDigest(before)
	current, _, parseErr := opfFields(before)
	if parseErr != nil {
		return nil, parseErr
	}
	if p.LookupISBN != "" {
		fetched, log, fetchErr := cli.Fetch(ctx, p.BinaryPath, p.LookupISBN)
		p.LookupLog = log
		switch {
		case fetchErr != nil:
			p.Status, p.Reason = "lookup_failed", fetchErr.Error()
		case strings.TrimSpace(fetched) == "":
			p.Status, p.Reason = "no_result", "Calibre metadata sources returned no result."
		case len(fetched) > 256<<10:
			p.Status, p.Reason = "lookup_failed", "Fetched OPF exceeds 256 KiB."
		default:
			fields, ids, parseErr := opfFields(fetched)
			if parseErr != nil {
				p.Status, p.Reason = "lookup_failed", parseErr.Error()
			} else {
				p.FetchedOPF, p.FetchedOPFDigest = fetched, refreshDigest(fetched)
				if uniqueCanonicalISBN(ids) != p.LookupISBN {
					p.Status, p.Reason = "ineligible", "Fetched metadata does not identify the rooted lookup ISBN; it cannot redirect Bindery identity."
				} else {
					p.Status = "ready"
				}
				refreshFields(p, snapshot, current, fields)
			}
		}
	}
	if len(p.Fields) == 0 {
		refreshFields(p, snapshot, current, nil)
	}
	if p.Status != "ready" {
		for i := range p.Fields {
			if p.Fields[i].Status == "change" {
				p.Fields[i].Status = "withheld"
				p.Fields[i].Reason = "Lookup result is not eligible for approval."
			}
		}
	}
	// Re-read live state after network IO: a long lookup cannot freeze the CWA
	// book, the Bindery work, or a competing ownership candidate.
	fresh, freshSnapshot, _, err := s.refreshState(ctx, bookID)
	if err != nil || fresh == nil || fresh.RootKey != p.RootKey || fresh.EvidenceKey != p.EvidenceKey ||
		fresh.SourceDigest != p.SourceDigest || !reflect.DeepEqual(fresh.Ownership, p.Ownership) ||
		lookupISBN(fresh, freshSnapshot) != p.LookupISBN || fresh.CurrentFingerprint != p.CurrentFingerprint ||
		!reflect.DeepEqual(fresh.Edition, p.Edition) || fresh.LibraryPath != p.LibraryPath || fresh.BinaryPath != p.BinaryPath {
		return nil, ErrMetadataRefreshStale
	}
	if p.CurrentOPFDigest != "" {
		current, err := cli.Show(ctx, p.BinaryPath, p.LibraryPath, p.CalibreID)
		if err != nil || refreshDigest(current) != p.CurrentOPFDigest {
			return nil, ErrMetadataRefreshStale
		}
	}
	envelope := refreshEnvelope{Proposal: *p, FetchedOPF: p.FetchedOPF, CurrentOPFDigest: p.CurrentOPFDigest,
		CurrentFingerprint: p.CurrentFingerprint, Ownership: p.Ownership, LibraryPath: p.LibraryPath, BinaryPath: p.BinaryPath}
	// No IDs or fingerprints inside the hash; the immutable record binds all
	// identity, ownership, fetched bytes, current OPF and exact change set.
	envelope.Proposal.Fingerprint = ""
	envelope.Proposal.ID = 0
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	p.Fingerprint = refreshDigest(string(encoded))
	envelope.Proposal.Fingerprint = p.Fingerprint
	encoded, err = json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	record := &db.CalibreMetadataRefreshRecord{BookID: p.BookID, CalibreID: p.CalibreID, Fingerprint: p.Fingerprint, Status: p.Status, JSON: string(encoded)}
	if err := s.metadataRefresh.Create(ctx, record); err != nil {
		return nil, err
	}
	p.ID = record.ID
	return p, nil
}

func (s *AuthoritativeService) ApplyMetadataRefresh(ctx context.Context, id, actor int64, fingerprint string) (*db.CalibreMetadataRefreshAttempt, error) {
	s.passMu.Lock()
	defer s.passMu.Unlock()
	allowed, err := s.metadataRefreshEnabled(ctx)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrMetadataRefreshDisabled
	}
	record, err := s.metadataRefresh.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if record == nil || record.Status != "ready" || fingerprint == "" || record.Fingerprint != fingerprint {
		return nil, ErrMetadataRefreshStale
	}
	var envelope refreshEnvelope
	if err := json.Unmarshal([]byte(record.JSON), &envelope); err != nil {
		return nil, err
	}
	p := envelope.Proposal
	p.Fingerprint, p.ID = "", 0
	envelope.Proposal = p
	encoded, err := json.Marshal(envelope)
	if err != nil || refreshDigest(string(encoded)) != fingerprint {
		return nil, ErrMetadataRefreshStale
	}
	p.Fingerprint = fingerprint
	p.LibraryPath, p.BinaryPath = envelope.LibraryPath, envelope.BinaryPath
	p.CurrentFingerprint, p.CurrentOPFDigest = envelope.CurrentFingerprint, envelope.CurrentOPFDigest
	p.Ownership = envelope.Ownership
	if p.BookID != record.BookID || p.CalibreID != record.CalibreID || p.Version != 1 || envelope.FetchedOPF == "" ||
		refreshDigest(envelope.FetchedOPF) != p.FetchedOPFDigest {
		return nil, ErrMetadataRefreshStale
	}
	changes := make([]MetadataRefreshField, 0, 2)
	for _, f := range p.Fields {
		if f.Status == "change" {
			if f.Name != "title" && f.Name != "publisher" {
				return nil, ErrMetadataRefreshStale
			}
			changes = append(changes, f)
		}
	}
	if len(changes) == 0 {
		return nil, ErrMetadataRefreshStale
	}
	attempt := &db.CalibreMetadataRefreshAttempt{ProposalID: id, ActorUserID: actor, BookID: p.BookID, CalibreID: p.CalibreID}
	if err := s.metadataRefresh.Begin(ctx, attempt); err != nil {
		return nil, fmt.Errorf("proposal already attempted or attempt store unavailable: %w", err)
	}
	finish := func(outcome string, reason error, verified string) (*db.CalibreMetadataRefreshAttempt, error) {
		message := ""
		if reason != nil {
			message = reason.Error()
		}
		// An HTTP disconnect after an external write must not leave a pending
		// intent with no recorded outcome. Bound the detached audit write.
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := s.metadataRefresh.Finish(recordCtx, attempt.ID, outcome, message, verified); err != nil {
			return attempt, errors.Join(reason, err)
		}
		attempt.Outcome, attempt.Error, attempt.Verification = outcome, message, verified
		return attempt, reason
	}
	fresh, snapshot, _, err := s.refreshState(ctx, p.BookID)
	if err != nil || fresh == nil || fresh.RootKey != p.RootKey || fresh.EvidenceKey != p.EvidenceKey ||
		fresh.SourceDigest != p.SourceDigest || fresh.CurrentFingerprint != envelope.CurrentFingerprint || !reflect.DeepEqual(fresh.Edition, p.Edition) ||
		!reflect.DeepEqual(fresh.Ownership, envelope.Ownership) || fresh.LibraryPath != envelope.LibraryPath || fresh.BinaryPath != envelope.BinaryPath ||
		lookupISBN(fresh, snapshot) != p.LookupISBN {
		return finish("rejected", ErrMetadataRefreshStale, "")
	}
	cli := s.metadataCLI()
	before, err := cli.Show(ctx, p.BinaryPath, p.LibraryPath, p.CalibreID)
	if err != nil {
		return finish("failed", err, "")
	}
	if before == "" || refreshDigest(before) != envelope.CurrentOPFDigest {
		return finish("rejected", ErrMetadataRefreshStale, "")
	}
	if err := s.metadataRefresh.Capture(ctx, attempt.ID, before); err != nil {
		return finish("failed", err, "No Calibre metadata update was requested.")
	}
	for _, f := range changes {
		if err := cli.Set(ctx, p.BinaryPath, p.LibraryPath, p.CalibreID, f.Name, f.Fetched); err != nil {
			// Even the first command can have committed before returning an
			// error. The state is indeterminate until an operator inspects it.
			return finish("partial", err, "Inspect the exact Calibre book; create a new preview only after recovery.")
		}
	}
	// A successful CLI exit alone is never a successful metadata refresh.
	reader, err := OpenReader(p.LibraryPath)
	if err != nil {
		return finish("verification_failed", err, "Calibre write completed but reread failed.")
	}
	cb, readErr := reader.GetBook(ctx, p.CalibreID)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return finish("verification_failed", errors.Join(readErr, closeErr), "Calibre write completed but reread failed.")
	}
	for _, f := range changes {
		actual := cb.Title
		if f.Name == "publisher" {
			actual = cb.Publisher
		}
		if actual != f.Fetched {
			return finish("verification_failed", fmt.Errorf("%s verification mismatch", f.Name), "Calibre did not retain the approved value.")
		}
	}
	if !reflect.DeepEqual(cb.Identifiers, p.CurrentIdentifiers) {
		return finish("verification_failed", errors.New("stored identifiers changed"), "A non-approved identifier changed during refresh.")
	}
	beforeFields, err := retainedOPFMetadata(before)
	if err != nil {
		return finish("verification_failed", err, "Cannot compare the original stored metadata record.")
	}
	after, err := cli.Show(ctx, p.BinaryPath, p.LibraryPath, p.CalibreID)
	if err != nil {
		return finish("verification_failed", err, "Calibre write completed but OPF reread failed.")
	}
	afterFields, err := retainedOPFMetadata(after)
	if err != nil {
		return finish("verification_failed", err, "Cannot read the post-write stored metadata record.")
	}
	approved := make(map[string]bool, len(changes))
	for _, change := range changes {
		approved[change.Name] = true
	}
	if key := retainedOPFChange(beforeFields, afterFields, approved); key != "" {
		return finish("verification_failed", fmt.Errorf("unapproved %s field changed", key), "Stored metadata outside the approved preview changed.")
	}
	return finish("applied", nil, "Approved fields and retained metadata verified by rereading the Calibre book and stored metadata record.")
}

func (s *AuthoritativeService) MetadataRefreshAttempts(ctx context.Context, bookID int64) ([]db.CalibreMetadataRefreshAttempt, error) {
	allowed, err := s.metadataRefreshEnabled(ctx)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrMetadataRefreshDisabled
	}
	return s.metadataRefresh.ListAttempts(ctx, bookID)
}
