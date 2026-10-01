package calibre

import (
	"reflect"
	"strings"
	"testing"
)

func TestRetainedOPFMetadataCoversCalibreFields(t *testing.T) {
	before := fakeOPF("Book One", "Ace Books", refreshISBN,
		`<dc:creator opf:file-as="Author, Alice">Alice Author</dc:creator>`+
			`<dc:language>eng</dc:language><dc:language>fra</dc:language>`+
			`<dc:subject>Science</dc:subject><dc:subject>History</dc:subject>`+
			`<dc:date>2020-02-01</dc:date>`+
			`<meta name="calibre:author_sort" content="Author, Alice"/>`+
			`<meta name="calibre:title_sort" content="Book One"/>`+
			`<meta name="calibre:rating" content="4"/>`+
			`<meta name="calibre:timestamp" content="2026-01-01T00:00:00Z"/>`+
			`<meta name="calibre:series" content="Library"/>`+
			`<meta name="calibre:series_index" content="2"/>`+
			`<meta name="calibre:user_metadata:#binding" content="{&quot;b&quot;:2,&quot;a&quot;:1}"/>`)
	fields, err := retainedOPFMetadata(before)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string][]string{
		"title": {"Book One"}, "publisher": {"Ace Books"}, "authors": {"Alice Author"},
		"author_sort": {"Author, Alice"}, "calibre:author_sort": {"Author, Alice"},
		"calibre:title_sort": {"Book One"}, "calibre:rating": {"4"},
		"calibre:timestamp": {"2026-01-01T00:00:00Z"},
		"calibre:series":    {"Library"}, "calibre:series_index": {"2"},
		"tags": {"History", "Keep subject", "Science"}, "comments": {"Keep description"},
		"languages": {"en", "fr"}, "pubdate": {"2020-02-01"},
		"identifiers":                    {"isbn:" + refreshISBN},
		"calibre:user_metadata:#binding": {`{"a":1,"b":2}`},
	} {
		if !reflect.DeepEqual(fields[key], want) {
			t.Errorf("%s = %v, want %v", key, fields[key], want)
		}
	}
	after := strings.Replace(before, `<dc:language>eng</dc:language><dc:language>fra</dc:language>`, `<dc:language>fr</dc:language><dc:language>EN</dc:language>`, 1)
	after = strings.Replace(after, `<dc:subject>Science</dc:subject><dc:subject>History</dc:subject>`, `<dc:subject>History</dc:subject><dc:subject>Science</dc:subject>`, 1)
	after = strings.Replace(after, `{&quot;b&quot;:2,&quot;a&quot;:1}`, `{&quot;a&quot;:1,&quot;b&quot;:2}`, 1)
	after = strings.Replace(after, `<dc:publisher>Ace Books</dc:publisher>`, `<dc:publisher>Other Press</dc:publisher>`, 1)
	updated, err := retainedOPFMetadata(after)
	if err != nil {
		t.Fatal(err)
	}
	if key := retainedOPFChange(fields, updated, map[string]bool{"publisher": true}); key != "" {
		t.Fatalf("equivalent multivalue/JSON ordering or approved publisher looked changed: %s", key)
	}
	updated["languages"] = []string{"eng"}
	if key := retainedOPFChange(fields, updated, map[string]bool{"publisher": true}); key != "languages" {
		t.Fatalf("missing second language went unnoticed: %s", key)
	}
}
