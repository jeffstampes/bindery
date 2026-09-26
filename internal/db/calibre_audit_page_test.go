package db

import (
	"context"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestCalibreAuditRepo_ListPageFiltersAndBounds(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	author := &models.Author{Name: "External Author"}
	if err := NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "Not a Calibre import", ForeignID: "OL1W"}
	if err := NewBookRepo(database).Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	repo := NewCalibreAuditRepo(database)
	findings := make([]models.CalibreAuditFinding, 260)
	for i := range findings {
		state := models.CalibreAuditUnresolved
		if i%2 == 1 {
			state = models.CalibreAuditUnmatched
		}
		findings[i] = models.CalibreAuditFinding{
			BookID: book.ID, CalibreID: 5, Field: models.CalibreAuditFieldIdentifiers,
			EvidenceKey: fmt.Sprintf("isbn:%03d", i), FindingType: models.CalibreAuditIdentifierConflict,
			Assessment: models.CalibreAuditAmbiguous, State: state,
			ComparisonFingerprint: fmt.Sprintf("fp-%d", i),
			CalibreEvidence:       []models.CalibreAuditEvidence{{Value: "owned", Source: "calibre"}},
			BinderyEvidence:       []models.CalibreAuditEvidence{{Value: "external", Source: "provider"}},
		}
	}
	if _, err := repo.Apply(ctx, findings); err != nil {
		t.Fatal(err)
	}
	page, total, err := repo.ListPage(ctx, CalibreAuditListOpts{State: models.CalibreAuditUnresolved, FindingType: models.CalibreAuditIdentifierConflict, Assessment: models.CalibreAuditAmbiguous, Limit: 25, Offset: 25})
	if err != nil {
		t.Fatal(err)
	}
	if total != 130 || len(page) != 25 || page[0].BookTitle != book.Title || page[0].ID >= page[24].ID || page[0].CalibreEvidence[0].Value != "owned" {
		t.Fatalf("filtered page: total=%d page=%+v", total, page)
	}
	page, total, err = repo.ListPage(ctx, CalibreAuditListOpts{Limit: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if total != 260 || len(page) > 250 || len(page) == 0 {
		t.Fatalf("unbounded page: %d of %d", len(page), total)
	}
	page, total, err = repo.ListPage(ctx, CalibreAuditListOpts{State: models.CalibreAuditResolved})
	if err != nil || total != 0 || page == nil || len(page) != 0 {
		t.Fatalf("empty page: %v %d %v", err, total, page)
	}
}
