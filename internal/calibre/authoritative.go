package calibre

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// AuthoritativeService provides the narrow ownership abstraction and reconciliation
// integration for Calibre/CWA authoritative-library mode (#5).
type AuthoritativeService struct {
	settings      *db.SettingsRepo
	crossRef      *db.CalibreCrossReferenceRepo
	books         *db.BookRepo
	editions      *db.EditionRepo
	audits        *db.CalibreAuditRepo
	readerFactory func(string) (AuthoritativeLibrary, error)
	// Serialize long audit snapshots and authoritative reconciliation within
	// the single service instance shared by scheduled/manual runs. Otherwise a
	// slower pass could commit older findings after a newer pass.
	passMu sync.Mutex
}

// NewAuthoritativeService constructs an AuthoritativeService instance.
func NewAuthoritativeService(settings *db.SettingsRepo, crossRef *db.CalibreCrossReferenceRepo, books *db.BookRepo) *AuthoritativeService {
	return &AuthoritativeService{
		settings: settings,
		crossRef: crossRef,
		books:    books,
	}
}

// WithEditions registers an EditionRepo to supply persisted edition data during reconciliation.
func (s *AuthoritativeService) WithEditions(editions *db.EditionRepo) *AuthoritativeService {
	s.editions = editions
	return s
}

// WithReaderFactory registers a custom reader factory function, primarily used for testing
// to supply instrumented or mock AuthoritativeLibrary implementations.
func (s *AuthoritativeService) WithReaderFactory(factory func(string) (AuthoritativeLibrary, error)) *AuthoritativeService {
	s.readerFactory = factory
	return s
}

// IsEnabled reports whether authoritative-library mode is currently enabled
// (`calibre.authoritative_library_enabled = true` AND `calibre.library_path` is non-empty).
func (s *AuthoritativeService) IsEnabled(ctx context.Context) bool {
	if s == nil || s.settings == nil {
		return false
	}
	sEn, _ := s.settings.Get(ctx, "calibre.authoritative_library_enabled")
	if sEn == nil || !strings.EqualFold(sEn.Value, "true") {
		return false
	}
	sPath, _ := s.settings.Get(ctx, "calibre.library_path")
	return sPath != nil && strings.TrimSpace(sPath.Value) != ""
}

// LibraryPath returns the configured Calibre library path if authoritative mode is enabled.
func (s *AuthoritativeService) LibraryPath(ctx context.Context) string {
	if !s.IsEnabled(ctx) {
		return ""
	}
	sPath, _ := s.settings.Get(ctx, "calibre.library_path")
	if sPath == nil {
		return ""
	}
	return strings.TrimSpace(sPath.Value)
}

// IsOwnedForFormat reports whether a specific format (models.MediaTypeEbook or
// models.MediaTypeAudiobook) is satisfied/owned for a book.
// When authoritative mode is enabled, a matched Calibre cross-reference satisfies
// the ebook format (models.MediaTypeEbook). Calibre matches do not satisfy audiobook format.
//
// Legacy untyped FilePath only satisfies a format if media_type matches that format specifically.
// For media_type="both", an untyped FilePath does not satisfy ebook or audiobook independently;
// explicit EbookFilePath / AudiobookFilePath (or Calibre match) are required.
func (s *AuthoritativeService) IsOwnedForFormat(ctx context.Context, book *models.Book, format string) bool {
	if book == nil {
		return false
	}
	switch format {
	case models.MediaTypeEbook:
		if book.EbookFilePath != "" || (book.MediaType == models.MediaTypeEbook && book.FilePath != "") {
			return true
		}
		if !s.IsEnabled(ctx) || s.crossRef == nil {
			return false
		}
		ref, err := s.crossRef.GetByBookID(ctx, book.ID)
		if err != nil || ref == nil {
			return false
		}
		return ref.Status == models.CalibreMatchStatusMatched

	case models.MediaTypeAudiobook:
		return book.AudiobookFilePath != "" || (book.MediaType == models.MediaTypeAudiobook && book.FilePath != "")

	default:
		return book.HasFileForCurrentFormat()
	}
}

// IsOwned reports whether all monitored formats for a Bindery work are satisfied/owned.
// If media_type is "ebook", checks ebook ownership.
// If media_type is "audiobook", checks audiobook ownership.
// If media_type is "both", returns true only if BOTH ebook and audiobook formats are satisfied.
func (s *AuthoritativeService) IsOwned(ctx context.Context, book *models.Book) bool {
	if book == nil {
		return false
	}
	wantsEbook := book.WantsEbook()
	wantsAudiobook := book.WantsAudiobook()

	if !wantsEbook && !wantsAudiobook {
		return book.HasFileForCurrentFormat()
	}

	if wantsEbook && !s.IsOwnedForFormat(ctx, book, models.MediaTypeEbook) {
		return false
	}
	if wantsAudiobook && !s.IsOwnedForFormat(ctx, book, models.MediaTypeAudiobook) {
		return false
	}
	return true
}

// EffectiveStatus returns the acquisition status for a book.
// If authoritative-library mode is enabled and all monitored formats for the book are satisfied,
// it returns models.BookStatusImported (owned/satisfied) unless skipped.
// Otherwise it returns book.Status.
func (s *AuthoritativeService) EffectiveStatus(ctx context.Context, book *models.Book) string {
	if book == nil {
		return ""
	}
	if book.Status == models.BookStatusSkipped {
		return models.BookStatusSkipped
	}
	if s.IsOwned(ctx, book) {
		return models.BookStatusImported
	}
	return book.Status
}

// FilterWantedBooks takes a list of wanted books and removes any book whose
// monitored formats are all satisfied under authoritative-library mode / disk files.
// If authoritative-library mode is disabled, it returns the input unchanged.
func (s *AuthoritativeService) FilterWantedBooks(ctx context.Context, books []models.Book) []models.Book {
	if len(books) == 0 || !s.IsEnabled(ctx) || s.crossRef == nil {
		return books
	}
	matchedMap, err := s.crossRef.GetMatchedMap(ctx)
	if err != nil {
		slog.Warn("authoritative mode: failed to query matched map", "error", err)
		return books
	}

	out := make([]models.Book, 0, len(books))
	for _, b := range books {
		wantsEbook := b.WantsEbook()
		wantsAudiobook := b.WantsAudiobook()

		ebookSatisfied := !wantsEbook
		if wantsEbook {
			if b.EbookFilePath != "" || (b.MediaType == models.MediaTypeEbook && b.FilePath != "") {
				ebookSatisfied = true
			} else if ref, ok := matchedMap[b.ID]; ok && ref.Status == models.CalibreMatchStatusMatched {
				ebookSatisfied = true
			}
		}

		audiobookSatisfied := !wantsAudiobook
		if wantsAudiobook {
			if b.AudiobookFilePath != "" || (b.MediaType == models.MediaTypeAudiobook && b.FilePath != "") {
				audiobookSatisfied = true
			}
		}

		if ebookSatisfied && audiobookSatisfied {
			continue // Suppress acquisition: work is fully satisfied
		}
		out = append(out, b)
	}
	return out
}

// ReconcileResult captures summary statistics of an authoritative reconciliation pass.
type ReconcileResult struct {
	TotalCalibreBooks int          `json:"totalCalibreBooks"`
	TotalBinderyBooks int          `json:"totalBinderyBooks"`
	Matched           int          `json:"matched"`
	Revalidated       int          `json:"revalidated"`
	Stale             int          `json:"stale"`
	Unmatched         int          `json:"unmatched"`
	Audit             *AuditResult `json:"audit,omitempty"`
}

// Reconcile performs a full reconciliation pass between Bindery works and Calibre books
// in authoritative-library mode. It matches Bindery works against Calibre's metadata.db
// read-only and upserts calibre_work_cross_references.
//
// Crucially, it NEVER imports new catalogue rows or Book entries into Bindery (Invariant 4).
func (s *AuthoritativeService) Reconcile(ctx context.Context) (*ReconcileResult, error) {
	if !s.IsEnabled(ctx) {
		return nil, ErrAuthoritativeDisabled
	}
	if s.audits != nil {
		s.passMu.Lock()
		defer s.passMu.Unlock()
	}
	libPath := s.LibraryPath(ctx)
	if libPath == "" {
		return nil, ErrAuthoritativeDisabled
	}

	factory := s.readerFactory
	if factory == nil {
		factory = OpenAuthoritativeReader
	}
	reader, err := factory(libPath)
	if err != nil {
		return nil, fmt.Errorf("open authoritative reader (%s): %w", libPath, err)
	}
	defer func() { _ = reader.Close() }()

	calibreBooks, err := reader.AllBooks(ctx)
	if err != nil {
		return nil, fmt.Errorf("read calibre books: %w", err)
	}

	if s.books == nil {
		return nil, fmt.Errorf("nil book repo")
	}
	binderyBooks, err := s.books.ListIncludingExcluded(ctx)
	if err != nil {
		return nil, fmt.Errorf("list bindery books: %w", err)
	}

	res := &ReconcileResult{
		TotalCalibreBooks: len(calibreBooks),
		TotalBinderyBooks: len(binderyBooks),
	}

	if len(binderyBooks) == 0 {
		// A now-empty Bindery catalogue still needs to retire old findings
		// and any Bindery-owned tags left on Calibre books.
		if s.audits != nil {
			res.Audit, err = s.auditSnapshot(ctx, calibreBooks, binderyBooks, nil, nil, NewLibraryIndex(calibreBooks))
			if err != nil {
				return res, fmt.Errorf("reconciliation completed but calibre metadata audit failed: %w", err)
			}
		}
		return res, nil
	}

	// Bulk-load all book identifiers and editions to avoid per-book N+1 DB queries (#5)
	idMap, err := s.books.ListAllBookIdentifiers(ctx)
	if err != nil {
		return nil, fmt.Errorf("bulk load book identifiers: %w", err)
	}
	var edMap map[int64][]models.Edition
	if s.editions != nil {
		edMap, err = s.editions.ListAllEditions(ctx)
		if err != nil {
			return nil, fmt.Errorf("bulk load editions: %w", err)
		}
	}

	// Build in-memory index over Calibre library
	idx := NewLibraryIndex(calibreBooks)

	// Load existing cross-references
	existingList, err := s.crossRef.ListByStatus(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list existing cross references: %w", err)
	}
	existingMap := make(map[int64]models.CalibreWorkCrossReference, len(existingList))
	for _, ref := range existingList {
		existingMap[ref.BookID] = ref
	}

	for i := range binderyBooks {
		b := &binderyBooks[i]

		// Attach pre-loaded bulk identifiers and editions
		if ids, ok := idMap[b.ID]; ok {
			b.Identifiers = ids
		}
		if eds, ok := edMap[b.ID]; ok {
			b.Editions = eds
		}

		if existingRef, ok := existingMap[b.ID]; ok {
			// Revalidate existing reference using single in-memory snapshot index
			updated, stale, err := RevalidateCrossReferenceWithIndex(ctx, &existingRef, b, idx)
			if err != nil {
				slog.Warn("authoritative reconcile: revalidation failed", "book_id", b.ID, "error", err)
				continue
			}
			if stale {
				res.Stale++
			} else {
				res.Revalidated++
			}
			if err := s.crossRef.UpsertCrossReference(ctx, updated); err != nil {
				slog.Warn("authoritative reconcile: upsert cross-reference failed", "book_id", b.ID, "error", err)
			}
		} else {
			// Perform new match
			matchRes := idx.MatchWork(b)
			ref := matchRes.ToCrossReference()
			if ref != nil {
				if ref.Status == models.CalibreMatchStatusMatched {
					res.Matched++
				}
				if err := s.crossRef.UpsertCrossReference(ctx, ref); err != nil {
					slog.Warn("authoritative reconcile: upsert cross-reference failed", "book_id", b.ID, "error", err)
				}
			} else {
				res.Unmatched++
			}
		}
	}

	if s.audits != nil {
		if s.editions == nil {
			return res, fmt.Errorf("calibre metadata audit requires an edition repository")
		}
		res.Audit, err = s.auditSnapshot(ctx, calibreBooks, binderyBooks, idMap, edMap, idx)
		if err != nil {
			return res, fmt.Errorf("reconciliation completed but calibre metadata audit failed: %w", err)
		}
	}
	return res, nil
}
