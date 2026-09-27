package calibre

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// ErrArtifactNotReady means the active ownership or canonical provider root
// cannot be revalidated; rescan after a successful reconciliation/discovery.
var ErrArtifactNotReady = errors.New("artifact scan requires a current canonical work match")

// WithArtifactEvidence enables explicit read-only scans of owned EPUB files.
// It does not use Calibre's metadata.db ISBN as a proxy for file provenance.
func (s *AuthoritativeService) WithArtifactEvidence(repo *db.CalibreArtifactRepo) *AuthoritativeService {
	s.artifacts = repo
	return s
}

// ScanArtifacts is an admin-triggered, per-work operation. It reads at most
// eight formats and never runs as part of a full-library audit or background
// refresh. An incomplete scan is marked partial, not negative evidence.
func (s *AuthoritativeService) ScanArtifacts(ctx context.Context, bookID int64) (*models.CalibreIdentitySnapshot, error) {
	return s.ScanArtifactsAttested(ctx, bookID, false)
}

// ScanArtifactsAttested records an explicit operator assertion that the ebook
// was original/unmodified before any Calibre-to-file metadata write-back. The
// default scan never makes this assertion; known earlier events reject it.
func (s *AuthoritativeService) ScanArtifactsAttested(ctx context.Context, bookID int64, original bool) (*models.CalibreIdentitySnapshot, error) {
	if !s.IsEnabled(ctx) || s.artifacts == nil {
		return nil, ErrAuthoritativeDisabled
	}
	s.passMu.Lock()
	defer s.passMu.Unlock()
	snapshot, err := s.IdentitySnapshot(ctx, bookID)
	if err != nil || snapshot == nil {
		return snapshot, err
	}
	rooted := false
	for _, evidence := range snapshot.Evidence {
		if evidence.Status == models.CalibreIdentityRoot && evidence.EditionID == "" && evidence.Method == metadata.RawMethodExactBook {
			rooted = true
		}
	}
	if !rooted {
		return nil, fmt.Errorf("%w: canonical provider work has not been resolved", ErrArtifactNotReady)
	}
	root := s.LibraryPath(ctx)
	reader, err := OpenReader(root)
	if err != nil {
		return nil, fmt.Errorf("open owned library for artifact scan: %w", err)
	}
	defer func() { _ = reader.Close() }()
	cb, err := reader.GetBook(ctx, snapshot.CalibreID)
	if err != nil {
		return nil, fmt.Errorf("read linked Calibre book: %w", err)
	}
	book, err := s.books.GetByID(ctx, bookID)
	if err != nil {
		return nil, fmt.Errorf("read canonical book: %w", err)
	}
	if book == nil || !auditExternalBook(book) || identityRootKey(book) != snapshot.RootKey {
		return nil, fmt.Errorf("%w: canonical work changed since discovery", ErrArtifactNotReady)
	}
	book.Identifiers, err = s.books.ListBookIdentifiers(ctx, bookID)
	if err != nil {
		return nil, fmt.Errorf("read identifiers for ownership revalidation: %w", err)
	}
	if s.editions != nil {
		book.Editions, err = s.editions.ListByBook(ctx, bookID)
		if err != nil {
			return nil, fmt.Errorf("read editions for ownership revalidation: %w", err)
		}
	}
	match := NewLibraryIndex([]CalibreBook{*cb}).MatchWork(auditMatchEvidence(book))
	if match.Status != models.CalibreMatchStatusMatched || match.CalibreID != cb.CalibreID {
		return nil, fmt.Errorf("%w: owned book no longer matches canonical work; reconcile first", ErrArtifactNotReady)
	}
	if len(cb.Formats) > artifactMaxFormats {
		return nil, fmt.Errorf("owned book exceeds eight-format scan limit")
	}
	if original {
		events, err := s.artifacts.ListWritebacks(ctx, bookID, cb.CalibreID)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 {
			return nil, fmt.Errorf("%w: an earlier Calibre-to-file write-back is recorded", ErrArtifactNotReady)
		}
	}
	scans := make([]models.CalibreArtifactScan, 0, len(cb.Formats))
	for _, format := range cb.Formats {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		scan, err := scanOwnedArtifact(ctx, root, cb, format)
		if err != nil {
			return nil, fmt.Errorf("scan %s for Calibre book %d: %w", format.Format, cb.CalibreID, err)
		}
		scan.BookID = bookID
		scan.AttestedOriginal = original && scan.Outcome == "scanned"
		scans = append(scans, scan)
	}
	if err := s.artifacts.ReplaceForBook(ctx, bookID, cb.CalibreID, scans); err != nil {
		return nil, fmt.Errorf("persist artifact observations: %w", err)
	}
	return s.IdentitySnapshot(ctx, bookID)
}

// RecordArtifactWriteback records an operator-reported external metadata
// embedding event for the active link. It never performs Polish Books or writes
// either Calibre metadata.db or ebook bytes.
func (s *AuthoritativeService) RecordArtifactWriteback(ctx context.Context, bookID int64, writtenAt time.Time, source string) (*models.CalibreIdentitySnapshot, error) {
	if !s.IsEnabled(ctx) || s.artifacts == nil {
		return nil, ErrAuthoritativeDisabled
	}
	s.passMu.Lock()
	defer s.passMu.Unlock()
	snapshot, err := s.IdentitySnapshot(ctx, bookID)
	if err != nil || snapshot == nil {
		return snapshot, err
	}
	reader, err := OpenReader(s.LibraryPath(ctx))
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	cb, err := reader.GetBook(ctx, snapshot.CalibreID)
	if err != nil {
		return nil, err
	}
	book, err := s.books.GetByID(ctx, bookID)
	if err != nil {
		return nil, err
	}
	if book == nil || !auditExternalBook(book) || identityRootKey(book) != snapshot.RootKey {
		return nil, ErrArtifactNotReady
	}
	book.Identifiers, err = s.books.ListBookIdentifiers(ctx, bookID)
	if err != nil {
		return nil, err
	}
	if s.editions != nil {
		book.Editions, err = s.editions.ListByBook(ctx, bookID)
		if err != nil {
			return nil, err
		}
	}
	match := NewLibraryIndex([]CalibreBook{*cb}).MatchWork(auditMatchEvidence(book))
	if match.Status != models.CalibreMatchStatusMatched || match.CalibreID != cb.CalibreID {
		return nil, ErrArtifactNotReady
	}
	if err := s.artifacts.RecordWriteback(ctx, models.CalibreArtifactWriteback{
		BookID: bookID, CalibreID: cb.CalibreID, WrittenAt: writtenAt, Source: source,
	}); err != nil {
		return nil, err
	}
	return s.IdentitySnapshot(ctx, bookID)
}

// A file changed on disk is stale and cannot corroborate any edition.
func (s *AuthoritativeService) attachArtifacts(ctx context.Context, snapshot *models.CalibreIdentitySnapshot) error {
	if s.artifacts == nil {
		return nil
	}
	scans, err := s.artifacts.ListByBookID(ctx, snapshot.BookID, snapshot.CalibreID)
	if err != nil {
		return err
	}
	rootPath, err := filepath.Abs(s.LibraryPath(ctx))
	if err != nil {
		return fmt.Errorf("resolve library root for artifact check: %w", err)
	}
	events, err := s.artifacts.ListWritebacks(ctx, snapshot.BookID, snapshot.CalibreID)
	if err != nil {
		return err
	}
	history, err := s.artifacts.ListHistoryByBookID(ctx, snapshot.BookID, snapshot.CalibreID)
	if err != nil {
		return err
	}
	for i := range history {
		classifyArtifactLineage(&history[i], events)
		history[i].Stale = true // historical observations do not identify the current file
	}
	snapshot.ArtifactHistory, snapshot.Writebacks = history, events
	var current *CalibreBook
	if len(scans) > 0 {
		reader, openErr := OpenReader(rootPath)
		if openErr == nil {
			current, openErr = reader.GetBook(ctx, snapshot.CalibreID)
			_ = reader.Close()
		}
		if openErr != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	assessArtifactScans(snapshot, scans, current, rootPath, events)
	return nil
}

// assessArtifactScans also serves bulk audit from its existing Calibre snapshot;
// it never opens a Calibre DB handle per work. If filesystem verification fails,
// the scan is stale and cannot select an edition.
func assessArtifactScans(snapshot *models.CalibreIdentitySnapshot, scans []models.CalibreArtifactScan,
	current *CalibreBook, rootPath string, events []models.CalibreArtifactWriteback,
) {
	var confined *os.Root
	if len(scans) > 0 {
		confined, _ = os.OpenRoot(rootPath)
		if confined != nil {
			defer func() { _ = confined.Close() }()
		}
	}
	for i := range scans {
		scan := &scans[i]
		classifyArtifactLineage(scan, events)
		if scan.BookID != snapshot.BookID || scan.CalibreID != snapshot.CalibreID {
			continue
		}
		present := false
		if current != nil {
			for _, format := range current.Formats {
				if scan.Format == format.Format && scan.FileName == format.FileName {
					rel, relErr := filepath.Rel(rootPath, format.AbsolutePath)
					present = relErr == nil && rel == scan.FilePath
				}
			}
		}
		writtenSinceScan := false
		for _, event := range events {
			if event.BookID == scan.BookID && event.CalibreID == scan.CalibreID && event.WrittenAt.After(scan.ScannedAt) {
				writtenSinceScan = true
				break
			}
		}
		if !present || scan.FilePath == "" || confined == nil || writtenSinceScan {
			// A reported write-back after the scan invalidates its claim
			// about the *current* file even when size/mtime did not change.
			scan.Stale = true
		} else if info, statErr := confined.Stat(scan.FilePath); statErr != nil || !info.Mode().IsRegular() || info.Size() != scan.SizeBytes || !info.ModTime().UTC().Equal(scan.ModifiedAt) {
			scan.Stale = true
		}
		for j := range scan.Identifiers {
			id := &scan.Identifiers[j]
			id.Status, id.EditionConfidence, id.EditionIDs = "unverified", "unresolved", nil
			if scan.Stale || scan.Outcome != "scanned" {
				continue
			}
			conflict := false
			for _, e := range snapshot.Evidence {
				if !slices.Contains(e.NormalizedIdentifiers["isbn"], id.NormalizedValue) {
					continue
				}
				switch e.Status {
				case models.CalibreIdentityRoot, models.CalibreIdentityCorroborated:
					id.Status = "matches_work"
					if e.Status == models.CalibreIdentityRoot && e.EditionID != "" && !slices.Contains(id.EditionIDs, e.EditionID) {
						id.EditionIDs = append(id.EditionIDs, e.EditionID)
					}
				case models.CalibreIdentityConflict:
					conflict = true
				}
			}
			if conflict {
				id.Status = "conflict"
				id.EditionIDs = nil
				continue
			}
			if id.Status == "matches_work" && len(id.EditionIDs) == 1 {
				id.EditionConfidence = "candidate" // never an automatic edition selection
			}
		}
		if scan.Outcome == "scanned" && !scan.Stale {
			// Multiple ISBNs from the same file are one correlated source.
			// Conflicting or divergent edition candidates make the physical
			// edition unresolved, even if one ISBN matched a rooted edition.
			var edition string
			ambiguous := false
			for _, id := range scan.Identifiers {
				if id.Status == "conflict" || id.Status != "matches_work" || len(id.EditionIDs) != 1 {
					ambiguous = true
					break
				}
				if edition != "" && edition != id.EditionIDs[0] {
					ambiguous = true
					break
				}
				edition = id.EditionIDs[0]
			}
			if ambiguous {
				for j := range scan.Identifiers {
					scan.Identifiers[j].EditionConfidence = "unresolved"
				}
			}
		}
	}
	snapshot.Artifacts = scans
}
