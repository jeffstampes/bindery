package calibre

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/models"
)

const (
	artifactMaxFormats    = 8
	artifactMaxFileBytes  = 64 << 20
	artifactMaxEntryBytes = 2 << 20
	artifactMaxTextBytes  = 16 << 20
	artifactMaxEntries    = 256
	artifactMaxCandidates = 32
)

var artifactISBNLabel = regexp.MustCompile(`(?i)\bISBN(?:-1[03])?\s*[:#]?\s*([0-9Xx][0-9Xx\s\-]{8,22}[0-9Xx])`)
var artifactTags = regexp.MustCompile(`<[^>]*>`)

// scanOwnedArtifact reads an actual Calibre-owned EPUB, never metadata.db's
// identifiers or an unverified client-supplied path. Limits bound decompression
// and explicitly mark incomplete scans partial instead of asserting absence.
func scanOwnedArtifact(ctx context.Context, libraryRoot string, cb *CalibreBook, format CalibreFormat) (models.CalibreArtifactScan, error) {
	s := models.CalibreArtifactScan{CalibreID: cb.CalibreID, Format: format.Format, FileName: format.FileName,
		Method: "bindery_epub_v1", ScannedAt: time.Now().UTC(), Identifiers: []models.CalibreArtifactIdentifier{}}
	root, err := filepath.Abs(libraryRoot)
	if err != nil {
		return s, fmt.Errorf("resolve library root: %w", err)
	}
	rel, err := filepath.Rel(root, format.AbsolutePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return s, fmt.Errorf("owned file escapes Calibre library")
	}
	s.FilePath = rel
	confined, err := os.OpenRoot(root)
	if err != nil {
		return s, fmt.Errorf("open library root: %w", err)
	}
	defer func() { _ = confined.Close() }()
	file, err := confined.Open(rel)
	if err != nil {
		return s, fmt.Errorf("open owned file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return s, fmt.Errorf("stat owned file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return s, fmt.Errorf("owned file is not regular")
	}
	s.SizeBytes, s.ModifiedAt = info.Size(), info.ModTime().UTC()
	if s.Format != "EPUB" {
		s.Method, s.Outcome = "unsupported_format", "unsupported"
		return s, nil
	}
	if s.SizeBytes > artifactMaxFileBytes {
		s.Outcome, s.Error = "partial", "file exceeds 64 MiB scan limit"
		return s, nil
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(file, artifactMaxFileBytes+1))
	if err != nil {
		return s, fmt.Errorf("hash owned file: %w", err)
	}
	if n != s.SizeBytes {
		s.Outcome, s.Error = "partial", "owned file changed while scanning"
		return s, nil
	}
	s.SHA256 = hex.EncodeToString(h.Sum(nil))
	s.CorrelationGroup = "sha256:" + s.SHA256
	archive, err := zip.NewReader(file, s.SizeBytes)
	if err != nil {
		s.Outcome, s.Error = "failed", "EPUB is not a readable ZIP archive"
		return s, nil
	}
	seen := make(map[string]bool)
	var total int64
	for i, member := range archive.File {
		if err := ctx.Err(); err != nil {
			return s, err
		}
		if i >= artifactMaxEntries || total >= artifactMaxTextBytes || len(s.Identifiers) >= artifactMaxCandidates {
			s.Outcome, s.Error = "partial", "EPUB scan limit reached; additional identifiers may exist"
			return s, nil
		}
		name := strings.ToLower(member.Name)
		source := ""
		switch {
		case strings.HasSuffix(name, ".opf"):
			source = "epub_opf"
		case strings.HasSuffix(name, ".xhtml"), strings.HasSuffix(name, ".html"), strings.HasSuffix(name, ".htm"):
			source = "epub_content"
		default:
			continue
		}
		if member.UncompressedSize64 > artifactMaxEntryBytes {
			s.Outcome, s.Error = "partial", "EPUB member exceeds 2 MiB scan limit"
			return s, nil
		}
		r, err := member.Open()
		if err != nil {
			s.Outcome, s.Error = "partial", "cannot open EPUB member"
			return s, nil
		}
		content, readErr := io.ReadAll(io.LimitReader(r, artifactMaxEntryBytes+1))
		closeErr := r.Close()
		if readErr != nil || closeErr != nil || len(content) > artifactMaxEntryBytes {
			s.Outcome, s.Error = "partial", "cannot fully read EPUB member"
			return s, nil
		}
		total += int64(len(content))
		add := func(raw string) {
			value := isbnutil.ToISBN13(raw)
			if value == "" || seen[source+":"+value] || len(s.Identifiers) >= artifactMaxCandidates {
				return
			}
			seen[source+":"+value] = true
			s.Identifiers = append(s.Identifiers, models.CalibreArtifactIdentifier{
				ObservedValue: strings.TrimSpace(raw), NormalizedValue: value, Source: source,
				Location: member.Name, EditionConfidence: "unresolved", Status: "unverified",
			})
		}
		if source == "epub_opf" {
			decoder := xml.NewDecoder(strings.NewReader(string(content)))
			for {
				tok, err := decoder.Token()
				if err != nil {
					if !errors.Is(err, io.EOF) {
						s.Outcome, s.Error = "partial", "cannot fully parse EPUB package metadata"
					}
					break
				}
				if element, ok := tok.(xml.StartElement); ok && element.Name.Local == "identifier" {
					var text string
					if err := decoder.DecodeElement(&text, &element); err != nil {
						s.Outcome, s.Error = "partial", "cannot fully parse EPUB identifiers"
						break
					}
					raw := strings.TrimSpace(text)
					raw = strings.TrimPrefix(strings.ToLower(raw), "urn:isbn:")
					add(raw)
				}
			}
		} else {
			text := artifactTags.ReplaceAllString(string(content), " ")
			for _, match := range artifactISBNLabel.FindAllStringSubmatch(text, artifactMaxCandidates) {
				// A label can precede an ISBN-10 or -13; do not accept a
				// checksum-invalid token or a bare number in unrelated prose.
				for _, field := range strings.FieldsFunc(match[1], func(r rune) bool { return r == '\n' || r == '\r' }) {
					add(field)
				}
			}
		}
		if s.Outcome == "partial" {
			return s, nil
		}
	}
	end, err := file.Stat()
	if err != nil || end.Size() != s.SizeBytes || !end.ModTime().UTC().Equal(s.ModifiedAt) {
		s.Outcome, s.Error = "partial", "owned file changed while scanning"
		return s, nil
	}
	s.Outcome = "scanned"
	return s, nil
}
