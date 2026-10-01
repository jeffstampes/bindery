package calibre

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

const refreshISBN = "9780306406157"

type refreshFakeCLI struct {
	conn            *sql.DB
	fetched         string
	fetchErr        error
	failField       string
	ignoreField     string
	calls           []string
	fetches         int
	showExtra       string
	unexpectedExtra string
}

func fakeOPF(title, publisher, isbn, extra string) string {
	return fmt.Sprintf(`<package xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf"><metadata><dc:title>%s</dc:title><dc:publisher>%s</dc:publisher><dc:identifier opf:scheme="ISBN">%s</dc:identifier><dc:subject>Keep subject</dc:subject><dc:description>Keep description</dc:description>%s</metadata></package>`, title, publisher, isbn, extra)
}
func (f *refreshFakeCLI) Show(ctx context.Context, _, _ string, id int64) (string, error) {
	var title, publisher, isbn string
	err := f.conn.QueryRowContext(ctx, `SELECT b.title, p.name, i.val FROM books b
		JOIN books_publishers_link l ON l.book = b.id JOIN publishers p ON p.id = l.publisher
		JOIN identifiers i ON i.book = b.id AND i.type = 'isbn' WHERE b.id = ?`, id).Scan(&title, &publisher, &isbn)
	if err != nil {
		return "", err
	}
	return fakeOPF(title, publisher, isbn, f.showExtra), nil
}
func (f *refreshFakeCLI) Fetch(_ context.Context, _, isbn string) (string, string, error) {
	f.fetches++
	if isbn != refreshISBN {
		return "", "", fmt.Errorf("wrong lookup ISBN: %s", isbn)
	}
	return f.fetched, "Google answered; Open Library timed out", f.fetchErr
}
func (f *refreshFakeCLI) Set(ctx context.Context, _, _ string, id int64, field, value string) error {
	f.calls = append(f.calls, field+":"+value)
	if f.unexpectedExtra != "" {
		f.showExtra = f.unexpectedExtra
	}
	if field == f.failField {
		return errors.New("simulated Calibre command failure")
	}
	if field == f.ignoreField {
		return nil
	}
	switch field {
	case "title":
		_, err := f.conn.ExecContext(ctx, `UPDATE books SET title = ? WHERE id = ?`, value, id)
		return err
	case "publisher":
		_, err := f.conn.ExecContext(ctx, `UPDATE publishers SET name = ? WHERE id =
			(SELECT publisher FROM books_publishers_link WHERE book = ?)`, value, id)
		return err
	default:
		return fmt.Errorf("unapproved field %q", field)
	}
}

func refreshFixture(t *testing.T, exact bool) (auditTestFixture, *refreshFakeCLI, *db.CalibreMetadataRefreshRepo) {
	t.Helper()
	f := newAuditTestFixture(t)
	ctx := context.Background()
	conn, err := sql.Open("sqlite", filepath.Join(f.root, metadataDB))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(ctx, `INSERT INTO identifiers(book,type,val) VALUES (1,'custom_legacy','retain-me')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReader(f.root)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := reader.GetBook(ctx, 1)
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	isbn := refreshISBN
	raw := metadata.RawBookDiscovery{CanonicalProvider: "openlibrary", CanonicalForeignID: "OL100W",
		Observations: []metadata.RawBookObservation{
			{Provider: "openlibrary", Method: metadata.RawMethodExactBook, Seed: "OL100W", Outcome: metadata.RawOutcomeFound,
				Book: &models.Book{ForeignID: "OL100W", Title: "Provider Work Title", ProviderISBNs: []string{isbn}, Author: &models.Author{Name: "Alice Author"}}},
			{Provider: "openlibrary", Method: metadata.RawMethodExactEditions, Seed: "OL100W", Outcome: metadata.RawOutcomeFound,
				Editions: []models.Edition{{ForeignID: "OL40M", Title: "Provider Edition Title", ISBN13: &isbn, Publisher: "Other Press", Format: "EPUB", IsEbook: true}}},
		}}
	identity := db.NewCalibreIdentityRepo(f.database)
	if err := identity.ReplaceBatch(ctx, []models.CalibreIdentitySnapshot{buildIdentitySnapshot(f.book, cb, raw)}); err != nil {
		t.Fatal(err)
	}
	artifacts := db.NewCalibreArtifactRepo(f.database)
	if exact {
		path := filepath.Join(f.root, "Alice Author/Book One (1)/bookone.epub")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		scan := models.CalibreArtifactScan{BookID: f.book.ID, CalibreID: 1, FilePath: "Alice Author/Book One (1)/bookone.epub", Format: "EPUB", FileName: "bookone",
			SizeBytes: info.Size(), ModifiedAt: info.ModTime().UTC(), ScannedAt: time.Now().UTC(), Method: "epub_opf", SHA256: "a", Outcome: "scanned", AttestedOriginal: true,
			Identifiers: []models.CalibreArtifactIdentifier{{NormalizedValue: isbn, Source: "epub_opf"}}}
		if err := artifacts.ReplaceForBook(ctx, f.book.ID, 1, []models.CalibreArtifactScan{scan}); err != nil {
			t.Fatal(err)
		}
	}
	fake := &refreshFakeCLI{conn: conn, fetched: fakeOPF("Provider Work Title", "Other Press", isbn, `<dc:creator>Unapproved author</dc:creator>`)}
	repo := db.NewCalibreMetadataRefreshRepo(f.database)
	f.svc.WithIdentityEvidence(identity, nil).WithArtifactEvidence(artifacts).WithMetadataRefresh(repo)
	f.svc.refreshCLI = fake
	if err := f.settings.Set(ctx, metadataRefreshSetting, "true"); err != nil {
		t.Fatal(err)
	}
	return f, fake, repo
}

func refreshField(p *MetadataRefreshProposal, name string) MetadataRefreshField {
	for _, field := range p.Fields {
		if field.Name == name {
			return field
		}
	}
	return MetadataRefreshField{}
}

func TestMetadataRefreshRequiresSeparateOptIn(t *testing.T) {
	f, cli, _ := refreshFixture(t, false)
	ctx := context.Background()
	if err := f.settings.Set(ctx, metadataRefreshSetting, "false"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PreviewMetadataRefresh(ctx, f.book.ID); !errors.Is(err, ErrMetadataRefreshDisabled) {
		t.Fatalf("preview without opt-in: %v", err)
	}
	if _, err := f.svc.ApplyMetadataRefresh(ctx, 1, 7, "fingerprint"); !errors.Is(err, ErrMetadataRefreshDisabled) {
		t.Fatalf("apply without opt-in: %v", err)
	}
	if cli.fetches != 0 || len(cli.calls) != 0 {
		t.Fatalf("disabled feature invoked Calibre: %+v", cli)
	}
}

func TestMetadataRefreshPreviewAndApplyExact(t *testing.T) {
	f, cli, repo := refreshFixture(t, true)
	ctx := context.Background()
	bookPath := filepath.Join(f.root, "Alice Author/Book One (1)/bookone.epub")
	beforeBytes, err := os.ReadFile(bookPath)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.svc.PreviewMetadataRefresh(ctx, f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "ready" || p.Edition.Confidence != "exact" || p.LookupISBN != refreshISBN || p.FetchedOPFDigest == "" || p.ID == 0 || p.Fingerprint == "" ||
		refreshField(p, "title").Status != "change" || refreshField(p, "publisher").Status != "change" ||
		refreshField(p, "authors").Status != "withheld" || refreshField(p, "pubdate").Status != "missing" ||
		p.CurrentIdentifiers["custom_legacy"] != "retain-me" || p.ProposedIdentifiers["custom_legacy"] != "retain-me" {
		t.Fatalf("unsafe/incomplete exact proposal: %+v", p)
	}
	stored, err := repo.Get(ctx, p.ID)
	if err != nil || stored == nil || !strings.Contains(stored.JSON, "Unapproved author") || !strings.Contains(stored.JSON, p.FetchedOPFDigest) {
		t.Fatalf("fetched OPF not retained: %+v %v", stored, err)
	}
	attempt, err := f.svc.ApplyMetadataRefresh(ctx, p.ID, 7, p.Fingerprint)
	if err != nil || attempt.Outcome != "applied" || len(cli.calls) != 2 {
		t.Fatalf("apply exact: %+v calls=%v err=%v", attempt, cli.calls, err)
	}
	if items, err := repo.ListAttempts(ctx, f.book.ID); err != nil || len(items) != 1 || items[0].ActorUserID != 7 || !strings.Contains(items[0].PreWriteOPF, "Book One") || items[0].Verification == "" {
		t.Fatalf("incomplete attempt: %+v %v", items, err)
	}
	reader, err := OpenReader(f.root)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := reader.GetBook(ctx, 1)
	_ = reader.Close()
	if err != nil || cb.Title != "Provider Work Title" || cb.Publisher != "Other Press" || cb.Identifiers["custom_legacy"] != "retain-me" {
		t.Fatalf("unapproved state changed: %+v %v", cb, err)
	}
	afterBytes, err := os.ReadFile(bookPath)
	if err != nil || string(afterBytes) != string(beforeBytes) {
		t.Fatalf("ebook bytes changed: %v", err)
	}
	if _, err := f.svc.ApplyMetadataRefresh(ctx, p.ID, 7, p.Fingerprint); err == nil {
		t.Fatal("proposal reused without a new preview")
	}
	if cli.fetches != 1 {
		t.Fatalf("apply refetched provider: %d", cli.fetches)
	}
}

func TestMetadataRefreshWorkAndHighPolicy(t *testing.T) {
	f, _, _ := refreshFixture(t, false)
	p, err := f.svc.PreviewMetadataRefresh(context.Background(), f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Edition.Confidence != "high" || p.Edition.ReasonCode != models.CalibreEditionReasonCalibreIDs || refreshField(p, "title").Status != "change" || refreshField(p, "publisher").Status != "withheld" {
		t.Fatalf("CWA-claim high must not authorize edition field: %+v", p)
	}
	for _, edition := range []models.CalibreEditionResolution{
		{Confidence: "high", ReasonCode: models.CalibreEditionReasonHistoricFile, EditionID: "OL40M"},
		{Confidence: "ambiguous", EditionID: ""}, {Confidence: "unresolved", EditionID: ""},
	} {
		policy := &MetadataRefreshProposal{Edition: edition, EvidenceKey: "work"}
		root := &models.CalibreIdentitySnapshot{Evidence: []models.CalibreIdentityEvidence{{Key: "work", ProviderMetadata: map[string]any{"title": "Provider Work Title"}}, {Status: models.CalibreIdentityRoot, Method: metadata.RawMethodExactEditions, EditionID: "OL40M", ProviderMetadata: map[string]any{"title": "Provider Edition Title"}}}}
		refreshFields(policy, root, map[string]string{"publisher": "Ace Books", "title": "Book One"}, map[string]string{"publisher": "Other Press", "title": "Provider Work Title"})
		want := "withheld"
		if edition.ReasonCode == models.CalibreEditionReasonHistoricFile {
			want = "change"
		}
		if got := refreshField(policy, "publisher").Status; got != want {
			t.Fatalf("edition %s/%s publisher %s, want %s", edition.Confidence, edition.ReasonCode, got, want)
		}
	}
}

func TestMetadataRefreshEquivalentISBNLookup(t *testing.T) {
	for _, tc := range []struct {
		name, fetched, extra, wantStatus string
		rooted                           []string
		editionEvidence                  bool
	}{
		{"rooted ISBN-10", "0-306-40615-2", "", "ready", []string{"0-306-40615-2"}, false},
		{"equivalent edition and fetched pairs", "0-306-40615-2", `<dc:identifier opf:scheme="ISBN">` + refreshISBN + `</dc:identifier>`, "ready", []string{"0-306-40615-2", refreshISBN}, true},
		{"different rooted ISBNs remain ineligible", refreshISBN, "", "ineligible", []string{"0-306-40615-2", "9781861972712"}, false},
		{"different fetched ISBNs remain ineligible", "0-306-40615-2", `<dc:identifier opf:scheme="ISBN">9781861972712</dc:identifier>`, "ineligible", []string{"0-306-40615-2", refreshISBN}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cli, _ := refreshFixture(t, tc.editionEvidence)
			ctx := context.Background()
			encoded, err := json.Marshal(map[string][]string{"isbn": tc.rooted})
			if err != nil {
				t.Fatal(err)
			}
			editionID := ""
			method := metadata.RawMethodExactBook
			if tc.editionEvidence {
				editionID, method = "OL40M", metadata.RawMethodExactEditions
			}
			res, err := f.database.ExecContext(ctx, `UPDATE calibre_identity_evidence SET normalized_identifiers_json = ?
				WHERE book_id = ? AND edition_id = ? AND method = ?`, string(encoded), f.book.ID, editionID, method)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("update rooted evidence: %d, %v", n, err)
			}
			cli.fetched = fakeOPF("Provider Work Title", "Other Press", tc.fetched, tc.extra)
			p, err := f.svc.PreviewMetadataRefresh(ctx, f.book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if p.Status != tc.wantStatus {
				t.Fatalf("status=%s; edition=%+v lookup=%s fields=%+v", p.Status, p.Edition, p.LookupISBN, p.Fields)
			}
			if tc.wantStatus != "ready" {
				if cli.fetches != 1 && tc.editionEvidence || cli.fetches != 0 && !tc.editionEvidence || refreshField(p, "publisher").Status == "change" {
					t.Fatalf("competing identifiers permitted write: %+v (fetches %d)", p, cli.fetches)
				}
				return
			}
			publisher := "withheld"
			if tc.editionEvidence {
				publisher = "change"
			}
			if p.LookupISBN != refreshISBN || refreshField(p, "publisher").Status != publisher || cli.fetches != 1 {
				t.Fatalf("equivalent ISBNs did not produce eligible proposal: %+v (fetches %d)", p, cli.fetches)
			}
			attempt, err := f.svc.ApplyMetadataRefresh(ctx, p.ID, 7, p.Fingerprint)
			if err != nil || attempt == nil || attempt.Outcome != "applied" || cli.fetches != 1 {
				t.Fatalf("equivalent ISBN approval: %+v, %v", attempt, err)
			}
		})
	}
}

func TestMetadataRefreshHistoricalFileHighPreviewAndApply(t *testing.T) {
	f, cli, repo := refreshFixture(t, false)
	ctx := context.Background()
	path := filepath.Join(f.root, "Alice Author/Book One (1)/bookone.epub")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := db.NewCalibreArtifactRepo(f.database)
	old := models.CalibreArtifactScan{
		BookID: f.book.ID, CalibreID: 1, FilePath: "Alice Author/Book One (1)/bookone.epub", Format: "EPUB", FileName: "bookone",
		SizeBytes: info.Size(), ModifiedAt: info.ModTime().UTC(), ScannedAt: time.Now().UTC().Add(-4 * time.Hour),
		Method: "epub_opf", SHA256: "original", Outcome: "scanned", AttestedOriginal: true,
		Identifiers: []models.CalibreArtifactIdentifier{{NormalizedValue: refreshISBN, Source: "epub_opf"}},
	}
	if err := artifacts.ReplaceForBook(ctx, f.book.ID, 1, []models.CalibreArtifactScan{old}); err != nil {
		t.Fatal(err)
	}
	if err := artifacts.RecordWriteback(ctx, models.CalibreArtifactWriteback{
		BookID: f.book.ID, CalibreID: 1, WrittenAt: time.Now().UTC().Add(-3 * time.Hour), Source: "calibre_to_file",
	}); err != nil {
		t.Fatal(err)
	}
	current := old
	current.SHA256, current.ScannedAt, current.AttestedOriginal = "changed-file", time.Now().UTC().Add(-2*time.Hour), false
	if err := artifacts.ReplaceForBook(ctx, f.book.ID, 1, []models.CalibreArtifactScan{current}); err != nil {
		t.Fatal(err)
	}
	p, err := f.svc.PreviewMetadataRefresh(ctx, f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Edition.Confidence != "high" || p.Edition.ReasonCode != models.CalibreEditionReasonHistoricFile ||
		p.Edition.EditionID != "OL40M" || p.Status != "ready" || p.LookupISBN != refreshISBN ||
		refreshField(p, "publisher").Status != "change" {
		t.Fatalf("historical file evidence did not yield eligible publisher proposal: %+v", p)
	}
	attempt, err := f.svc.ApplyMetadataRefresh(ctx, p.ID, 7, p.Fingerprint)
	if err != nil || attempt == nil || attempt.Outcome != "applied" || cli.fetches != 1 {
		t.Fatalf("high historical-file apply: %+v, %v; fetches=%d", attempt, err, cli.fetches)
	}
	reader, err := OpenReader(f.root)
	if err != nil {
		t.Fatal(err)
	}
	book, readErr := reader.GetBook(ctx, 1)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("reread owned book: %v %v", readErr, closeErr)
	}
	if book.Publisher != "Other Press" || book.Identifiers["custom_legacy"] != "retain-me" {
		t.Fatalf("high-confidence write or identifier preservation failed: %+v", book)
	}
	attempts, err := repo.ListAttempts(ctx, f.book.ID)
	if err != nil || len(attempts) != 1 || attempts[0].Outcome != "applied" {
		t.Fatalf("high-confidence attempt not persisted: %+v, %v", attempts, err)
	}
}

func TestMetadataRefreshLookupOutcomesAndIdentityMismatch(t *testing.T) {
	for _, tc := range []struct {
		name, fetched string
		err           error
		status        string
	}{
		{"no result", "", nil, "no_result"},
		{"plugin error", "", errors.New("plugin timed out"), "lookup_failed"},
		{"wrong ISBN", fakeOPF("Other Work", "Other Press", "9781861972712", ""), nil, "ineligible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cli, _ := refreshFixture(t, false)
			cli.fetched, cli.fetchErr = tc.fetched, tc.err
			p, err := f.svc.PreviewMetadataRefresh(context.Background(), f.book.ID)
			if err != nil || p.Status != tc.status || p.ID == 0 || len(cli.calls) != 0 {
				t.Fatalf("lookup outcome: %+v %v", p, err)
			}
			if refreshField(p, "title").Current != "Book One" || p.CurrentOPFDigest == "" {
				t.Fatalf("current metadata was omitted from failed lookup: %+v", p)
			}
			if tc.status != "ready" && refreshField(p, "title").Status == "change" {
				t.Fatalf("ineligible lookup offered a write: %+v", p)
			}
			if _, err := f.svc.ApplyMetadataRefresh(context.Background(), p.ID, 1, p.Fingerprint); !errors.Is(err, ErrMetadataRefreshStale) {
				t.Fatalf("ineligible applied: %v", err)
			}
		})
	}
}

func TestMetadataRefreshRejectsStaleSources(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(context.Context, auditTestFixture, *refreshFakeCLI) error
	}{
		{"ownership", func(ctx context.Context, f auditTestFixture, _ *refreshFakeCLI) error {
			_, err := f.database.ExecContext(ctx, `UPDATE calibre_work_cross_references SET status = 'stale' WHERE book_id = ?`, f.book.ID)
			return err
		}},
		{"work identity", func(ctx context.Context, f auditTestFixture, _ *refreshFakeCLI) error {
			_, err := f.database.ExecContext(ctx, `UPDATE books SET foreign_id = 'OL999W' WHERE id = ?`, f.book.ID)
			return err
		}},
		{"rooted provider evidence", func(ctx context.Context, f auditTestFixture, _ *refreshFakeCLI) error {
			_, err := f.database.ExecContext(ctx, `UPDATE calibre_identity_evidence SET provider_metadata_json = '{"title":"Changed provider title"}'
				WHERE book_id = ? AND method = ? AND status = ?`, f.book.ID, metadata.RawMethodExactBook, models.CalibreIdentityRoot)
			return err
		}},
		{"Bindery work title", func(ctx context.Context, f auditTestFixture, _ *refreshFakeCLI) error {
			_, err := f.database.ExecContext(ctx, `UPDATE books SET title = 'Changed Bindery title' WHERE id = ?`, f.book.ID)
			return err
		}},
		{"Calibre title", func(ctx context.Context, _ auditTestFixture, cli *refreshFakeCLI) error {
			_, err := cli.conn.ExecContext(ctx, `UPDATE books SET title = 'edited since preview' WHERE id = 1`)
			return err
		}},
		{"Calibre publisher", func(ctx context.Context, _ auditTestFixture, cli *refreshFakeCLI) error {
			_, err := cli.conn.ExecContext(ctx, `UPDATE publishers SET name = 'edited since preview' WHERE id = 1`)
			return err
		}},
		{"OPF custom field", func(_ context.Context, _ auditTestFixture, cli *refreshFakeCLI) error {
			cli.showExtra = `<meta name="custom" content="changed"/>`
			return nil
		}},
		{"competing match", func(ctx context.Context, _ auditTestFixture, cli *refreshFakeCLI) error {
			_, err := cli.conn.ExecContext(ctx, `INSERT INTO identifiers(book,type,val) VALUES (2,'isbn',?)`, refreshISBN)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cli, repo := refreshFixture(t, false)
			ctx := context.Background()
			p, err := f.svc.PreviewMetadataRefresh(ctx, f.book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.edit(ctx, f, cli); err != nil {
				t.Fatal(err)
			}
			attempt, err := f.svc.ApplyMetadataRefresh(ctx, p.ID, 1, p.Fingerprint)
			if !errors.Is(err, ErrMetadataRefreshStale) || attempt == nil || attempt.Outcome != "rejected" || len(cli.calls) != 0 {
				t.Fatalf("stale apply: %+v %v calls=%v", attempt, err, cli.calls)
			}
			if items, err := repo.ListAttempts(ctx, f.book.ID); err != nil || len(items) != 1 || items[0].Outcome != "rejected" {
				t.Fatalf("stale attempt: %+v %v", items, err)
			}
			if cli.fetches != 1 {
				t.Fatal("apply silently refetched a changed proposal")
			}
		})
	}
}

func TestMetadataRefreshDetectsUnapprovedPostWriteMetadata(t *testing.T) {
	for _, tc := range []struct{ name, before, after, field string }{
		{"publisher-only author sort", `<dc:creator opf:file-as="Author, Alice">Alice Author</dc:creator>`, `<dc:creator opf:file-as="Other, Alice">Alice Author</dc:creator>`, "author_sort"},
		{"publisher-only title sort", `<meta name="calibre:title_sort" content="Book One"/>`, `<meta name="calibre:title_sort" content="One, Book"/>`, "calibre:title_sort"},
		{"publisher-only rating", `<meta name="calibre:rating" content="4"/>`, `<meta name="calibre:rating" content="6"/>`, "calibre:rating"},
		{"publisher-only timestamp", `<meta name="calibre:timestamp" content="2026-01-01T00:00:00Z"/>`, `<meta name="calibre:timestamp" content="2026-01-02T00:00:00Z"/>`, "calibre:timestamp"},
		{"publisher-only second language removed", `<dc:language>eng</dc:language><dc:language>fra</dc:language>`, `<dc:language>eng</dc:language>`, "languages"},
		{"publisher-only second language replaced", `<dc:language>eng</dc:language><dc:language>fra</dc:language>`, `<dc:language>eng</dc:language><dc:language>deu</dc:language>`, "languages"},
		{"changed custom column", `<meta name="calibre:user_metadata:#binding" content="original"/>`, `<meta name="calibre:user_metadata:#binding" content="unapproved"/>`, "calibre:user_metadata:#binding"},
		{"changed large numeric custom value", `<meta name="calibre:user_metadata:#binding" content="{&quot;#value#&quot;:9007199254740992}"/>`, `<meta name="calibre:user_metadata:#binding" content="{&quot;#value#&quot;:9007199254740993}"/>`, "calibre:user_metadata:#binding"},
		{"after-only custom column", "", `<meta name="calibre:user_metadata:#binding" content="unapproved"/>`, "calibre:user_metadata:#binding"},
		{"after-only language", "", `<dc:language>eng</dc:language>`, "languages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cli, repo := refreshFixture(t, true)
			ctx := context.Background()
			cli.showExtra = tc.before
			if strings.HasPrefix(tc.name, "publisher-only") {
				cli.fetched = fakeOPF("Book One", "Other Press", refreshISBN, "")
			}
			p, err := f.svc.PreviewMetadataRefresh(ctx, f.book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(tc.name, "publisher-only") && (refreshField(p, "title").Status != "unchanged" || refreshField(p, "publisher").Status != "change") {
				t.Fatalf("expected publisher-only approved change: %+v", p.Fields)
			}
			cli.unexpectedExtra = tc.after
			attempt, err := f.svc.ApplyMetadataRefresh(ctx, p.ID, 1, p.Fingerprint)
			if err == nil || attempt == nil || attempt.Outcome != "verification_failed" || !strings.Contains(attempt.Error, tc.field) {
				t.Fatalf("unapproved %s change went unnoticed: %+v %v", tc.field, attempt, err)
			}
			items, err := repo.ListAttempts(ctx, f.book.ID)
			if err != nil || len(items) != 1 || items[0].Outcome != "verification_failed" {
				t.Fatalf("verification failure not recorded: %+v %v", items, err)
			}
		})
	}
}

func TestMetadataRefreshRetainsUnorderedLanguages(t *testing.T) {
	f, cli, _ := refreshFixture(t, true)
	ctx := context.Background()
	cli.fetched = fakeOPF("Book One", "Other Press", refreshISBN, "")
	cli.showExtra = `<dc:language>eng</dc:language><dc:language>fra</dc:language>`
	p, err := f.svc.PreviewMetadataRefresh(ctx, f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshField(p, "publisher").Status != "change" || refreshField(p, "title").Status != "unchanged" {
		t.Fatalf("not a publisher-only preview: %+v", p.Fields)
	}
	cli.unexpectedExtra = `<dc:language>fra</dc:language><dc:language>eng</dc:language>`
	attempt, err := f.svc.ApplyMetadataRefresh(ctx, p.ID, 1, p.Fingerprint)
	if err != nil || attempt == nil || attempt.Outcome != "applied" {
		t.Fatalf("reordered languages should not fail verification: %+v, %v", attempt, err)
	}
}

func TestMetadataRefreshPartialAndVerificationFailures(t *testing.T) {
	for _, tc := range []struct{ name, fail, ignore, outcome string }{
		{"second command fails", "publisher", "", "partial"},
		{"first command may have written", "title", "", "partial"},
		{"verification fails", "", "publisher", "verification_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cli, repo := refreshFixture(t, true)
			ctx := context.Background()
			p, err := f.svc.PreviewMetadataRefresh(ctx, f.book.ID)
			if err != nil {
				t.Fatal(err)
			}
			cli.failField, cli.ignoreField = tc.fail, tc.ignore
			attempt, err := f.svc.ApplyMetadataRefresh(ctx, p.ID, 1, p.Fingerprint)
			if err == nil || attempt == nil || attempt.Outcome != tc.outcome {
				t.Fatalf("partial command: %+v %v", attempt, err)
			}
			if items, err := repo.ListAttempts(ctx, f.book.ID); err != nil || len(items) != 1 || items[0].PreWriteOPF == "" || items[0].Outcome != tc.outcome {
				t.Fatalf("attempt provenance: %+v %v", items, err)
			}
			if _, err := f.svc.ApplyMetadataRefresh(ctx, p.ID, 1, p.Fingerprint); err == nil {
				t.Fatal("unsafe retry of partial proposal")
			}
			if cli.fetches != 1 {
				t.Fatal("retry silently fetched different metadata")
			}
		})
	}
}
