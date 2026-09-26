package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCalibreTagWriter_OnlyChangesOwnedTagLinks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	conn, err := sql.Open("sqlite", filepath.Join(root, "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, query := range []string{
		`PRAGMA application_id = 0x63616c69`,
		`CREATE TABLE books (id INTEGER PRIMARY KEY, title TEXT, pubdate TEXT, comments TEXT, last_modified TEXT)`,
		`CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE, link TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE books_tags_link (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, tag INTEGER NOT NULL, UNIQUE(book, tag))`,
		`INSERT INTO books (id, title, pubdate, comments, last_modified) VALUES (1, 'Curated', '2020', 'Owner notes', 'yesterday'), (2, 'Another', '2019', 'Other notes', 'today')`,
		`INSERT INTO tags (id, name) VALUES (1, 'Favorite'), (2, 'Unread')`,
		`INSERT INTO books_tags_link (book, tag) VALUES (1, 1), (1, 2), (2, 1)`,
	} {
		if _, err := conn.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	writer := NewCalibreTagWriter(root)
	check := func(desired map[int64]bool, wantChanges int, wantTags string) {
		t.Helper()
		n, err := writer.ReconcileMismatch(ctx, desired)
		if err != nil || n != wantChanges {
			t.Fatalf("reconcile = %d, %v; want %d", n, err, wantChanges)
		}
		rows, err := conn.QueryContext(ctx, `SELECT l.book, t.name FROM books_tags_link l JOIN tags t ON t.id=l.tag ORDER BY l.book, t.name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var id int64
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				t.Fatal(err)
			}
			got = append(got, strings.Repeat("#", int(id))+name)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		if strings.Join(got, ",") != wantTags {
			t.Fatalf("links = %v, want %s", got, wantTags)
		}
		var title, pubdate, comments, modified string
		if err := conn.QueryRowContext(ctx, `SELECT title, pubdate, comments, last_modified FROM books WHERE id=1`).Scan(&title, &pubdate, &comments, &modified); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual([]string{title, pubdate, comments, modified}, []string{"Curated", "2020", "Owner notes", "yesterday"}) {
			t.Fatalf("curated metadata changed: %q %q %q %q", title, pubdate, comments, modified)
		}
	}
	check(map[int64]bool{1: false}, 0, "#Favorite,#Unread,##Favorite")
	check(map[int64]bool{1: true}, 1, "#BinderyMismatch,#Favorite,#Unread,##Favorite")
	check(map[int64]bool{1: true}, 0, "#BinderyMismatch,#Favorite,#Unread,##Favorite")
	check(nil, 1, "#Favorite,#Unread,##Favorite")
	check(nil, 0, "#Favorite,#Unread,##Favorite")
	// A deleted book cannot acquire a link (or even create an unused tag).
	if n, err := writer.ReconcileMismatch(ctx, map[int64]bool{99: true}); err != nil || n != 0 {
		t.Fatalf("deleted book tagged: %d %v", n, err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA application_id = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ReconcileMismatch(ctx, map[int64]bool{1: true}); err == nil {
		t.Fatal("non-Calibre database accepted a tag write")
	}
	var tagRows int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM tags WHERE name = ?`, CalibreMismatchTag).Scan(&tagRows); err != nil || tagRows != 1 {
		t.Fatalf("unexpected tag rows after failed writer: %d %v", tagRows, err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA application_id = 0x63616c69`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `CREATE TRIGGER fail_second_tag BEFORE INSERT ON books_tags_link
		WHEN NEW.book = 2 BEGIN SELECT RAISE(ABORT, 'second book rejected'); END`); err != nil {
		t.Fatal(err)
	}
	if n, err := writer.ReconcileMismatch(ctx, map[int64]bool{1: true, 2: true}); err == nil || n != 0 {
		t.Fatalf("failed batch = %d, %v; want neither book committed", n, err)
	}
	check(nil, 0, "#Favorite,#Unread,##Favorite")
	if _, err := conn.ExecContext(ctx, `DROP TRIGGER fail_second_tag`); err != nil {
		t.Fatal(err)
	}
	check(map[int64]bool{1: true, 2: true}, 2, "#BinderyMismatch,#Favorite,#Unread,##BinderyMismatch,##Favorite")
	check(nil, 2, "#Favorite,#Unread,##Favorite")
	// Fail closed rather than create metadata.db in an invalid library.
	if _, err := NewCalibreTagWriter(t.TempDir()).ReconcileMismatch(ctx, map[int64]bool{1: true}); err == nil {
		t.Fatal("missing library unexpectedly accepted a tag write")
	}
}

func TestCalibreTagWriter_BatchesConvergeAndRetry(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	conn, err := sql.Open("sqlite", filepath.Join(root, "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	const total = 2*calibreTagBatchSize + 1
	for _, query := range []string{
		`PRAGMA application_id = 0x63616c69`,
		`CREATE TABLE books (id INTEGER PRIMARY KEY, title TEXT)`,
		`CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE, link TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE books_tags_link (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, tag INTEGER NOT NULL, UNIQUE(book, tag))`,
		`INSERT INTO tags (id, name) VALUES (1, 'Favorite')`,
	} {
		if _, err := conn.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.ExecContext(ctx, `WITH RECURSIVE ids(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM ids WHERE n < ?)
		INSERT INTO books (id, title) SELECT n, 'Curated' FROM ids`, total); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO books_tags_link (book, tag) SELECT id, 1 FROM books`); err != nil {
		t.Fatal(err)
	}
	writer := NewCalibreTagWriter(root)
	count := func(query string, want int) {
		t.Helper()
		var got int
		if err := conn.QueryRowContext(ctx, query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got %d, %v; want %d", query, got, err, want)
		}
	}
	// An all-deleted desired set must not create an unused tag.
	if n, err := writer.ReconcileMismatch(ctx, map[int64]bool{999999: true}); err != nil || n != 0 {
		t.Fatalf("nonexistent book: %d %v", n, err)
	}
	count(`SELECT COUNT(*) FROM tags`, 1)
	desired := make(map[int64]bool, total+1)
	for id := int64(1); id <= total; id++ {
		desired[id] = true
	}
	desired[999999] = true // invalid ID mixed into an otherwise valid batch

	if _, err := conn.ExecContext(ctx, `CREATE TRIGGER fail_add_batch BEFORE INSERT ON books_tags_link
		WHEN NEW.book = 257 BEGIN SELECT RAISE(ABORT, 'add batch rejected'); END`); err != nil {
		t.Fatal(err)
	}
	if n, err := writer.ReconcileMismatch(ctx, desired); err == nil || n != calibreTagBatchSize {
		t.Fatalf("partial add: %d %v; want first batch committed", n, err)
	}
	count(`SELECT COUNT(*) FROM books_tags_link WHERE tag IN (SELECT id FROM tags WHERE name = 'BinderyMismatch')`, calibreTagBatchSize)
	count(`SELECT COUNT(*) FROM tags`, 2)
	if _, err := conn.ExecContext(ctx, `DROP TRIGGER fail_add_batch`); err != nil {
		t.Fatal(err)
	}
	if n, err := writer.ReconcileMismatch(ctx, desired); err != nil || n != total-calibreTagBatchSize {
		t.Fatalf("add retry: %d %v", n, err)
	}
	count(`SELECT COUNT(*) FROM books_tags_link WHERE tag IN (SELECT id FROM tags WHERE name = 'BinderyMismatch')`, total)
	if n, err := writer.ReconcileMismatch(ctx, desired); err != nil || n != 0 {
		t.Fatalf("idempotent add: %d %v", n, err)
	}

	if _, err := conn.ExecContext(ctx, `CREATE TRIGGER fail_remove_batch BEFORE DELETE ON books_tags_link
		WHEN OLD.book = 257 AND OLD.tag IN (SELECT id FROM tags WHERE name = 'BinderyMismatch')
		BEGIN SELECT RAISE(ABORT, 'remove batch rejected'); END`); err != nil {
		t.Fatal(err)
	}
	keepLast := map[int64]bool{total: true}
	if n, err := writer.ReconcileMismatch(ctx, keepLast); err == nil || n != calibreTagBatchSize {
		t.Fatalf("partial removal: %d %v; want first batch committed", n, err)
	}
	count(`SELECT COUNT(*) FROM books_tags_link WHERE tag IN (SELECT id FROM tags WHERE name = 'BinderyMismatch')`, total-calibreTagBatchSize)
	if _, err := conn.ExecContext(ctx, `DROP TRIGGER fail_remove_batch`); err != nil {
		t.Fatal(err)
	}
	if n, err := writer.ReconcileMismatch(ctx, keepLast); err != nil || n != total-calibreTagBatchSize-1 {
		t.Fatalf("remove retry: %d %v", n, err)
	}
	count(`SELECT COUNT(*) FROM books_tags_link WHERE tag IN (SELECT id FROM tags WHERE name = 'BinderyMismatch')`, 1)
	count(`SELECT COUNT(*) FROM books_tags_link WHERE book = (SELECT MAX(id) FROM books) AND tag IN (SELECT id FROM tags WHERE name = 'BinderyMismatch')`, 1)
	if n, err := writer.ReconcileMismatch(ctx, nil); err != nil || n != 1 {
		t.Fatalf("remove final desired book: %d %v", n, err)
	}
	if n, err := writer.ReconcileMismatch(ctx, nil); err != nil || n != 0 {
		t.Fatalf("idempotent removal: %d %v", n, err)
	}
	count(`SELECT COUNT(*) FROM books_tags_link WHERE tag IN (SELECT id FROM tags WHERE name = 'BinderyMismatch')`, 0)
	count(`SELECT COUNT(*) FROM books_tags_link WHERE tag = 1`, total)
	count(`SELECT COUNT(*) FROM books WHERE title = 'Curated'`, total)
	count(`SELECT COUNT(*) FROM tags`, 2)
}
