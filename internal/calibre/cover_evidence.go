package calibre

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OpenOwnedCover returns only the current matched Calibre book's own cover.jpg.
// It is on-demand review evidence, never an input to matching or reconciliation.
// The caller closes the file; absent, unsafe or oversized covers return nil.
func (s *AuthoritativeService) OpenOwnedCover(ctx context.Context, bookID int64) (*os.File, error) {
	snapshot, err := s.IdentitySnapshot(ctx, bookID)
	if err != nil || snapshot == nil || !snapshot.HasOwnedCover {
		return nil, err
	}
	reader, err := OpenReader(s.LibraryPath(ctx))
	if err != nil {
		return nil, fmt.Errorf("open Calibre library for cover: %w", err)
	}
	defer func() { _ = reader.Close() }()
	book, err := reader.GetBook(ctx, snapshot.CalibreID)
	if err != nil || book == nil || book.CoverPath == "" {
		return nil, err
	}
	rootPath := reader.LibraryPath()
	rel, err := filepath.Rel(rootPath, book.CoverPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, nil
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open Calibre cover root: %w", err)
	}
	defer func() { _ = root.Close() }()
	// Even an in-library symlink could borrow another book's cover. Refuse
	// symlinks in every component rather than just relying on root confinement.
	prefix := ""
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		prefix = filepath.Join(prefix, part)
		info, err := root.Lstat(prefix)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, nil
		}
	}
	file, err := root.Open(rel) // refuses symlinks leaving the library
	if err != nil {
		return nil, nil
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 8<<20 {
		_ = file.Close()
		return nil, nil
	}
	return file, nil
}
