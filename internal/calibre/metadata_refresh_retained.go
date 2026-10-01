package calibre

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"

	"github.com/vavallee/bindery/internal/isbnutil"
)

// retainedOPFMetadata is the post-write contract, separate from opfFields'
// intentionally small preview projection. Calibre's set_metadata --field can
// reapply the entire Metadata object: preserve every represented editable
// field, not just those that the UI offers for approval.
func retainedOPFMetadata(raw string) (map[string][]string, error) {
	fields := make(map[string][]string)
	decoder := xml.NewDecoder(strings.NewReader(raw))
	inMetadata := false
	var item xml.StartElement
	var text strings.Builder
	add := func(key, value string) { fields[key] = append(fields[key], value) }
	for {
		tok, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode stored Calibre OPF: %w", err)
		}
		switch v := tok.(type) {
		case xml.StartElement:
			if v.Name.Local == "metadata" && !inMetadata {
				inMetadata = true
				continue
			}
			if inMetadata {
				item = v
				text.Reset()
			}
		case xml.CharData:
			if inMetadata {
				text.Write(v)
			}
		case xml.EndElement:
			if !inMetadata {
				continue
			}
			if v.Name.Local == "metadata" {
				inMetadata = false
				continue
			}
			if v.Name != item.Name {
				continue
			}
			value := strings.TrimSpace(text.String())
			switch v.Name.Local {
			case "title", "publisher":
				add(v.Name.Local, value)
			case "creator":
				add("authors", value)
				// The creator's sort name is separate from both its display
				// name and calibre:author_sort; neither is an approved write.
				if sortName, ok := refreshOPFAttribute(item.Attr, "file-as"); ok {
					add("author_sort", sortName)
				}
				role, ok := refreshOPFAttribute(item.Attr, "role")
				if !ok || role == "" {
					role = "aut"
				}
				add("creator:role", strings.ToLower(role))
			case "contributor":
				role, _ := refreshOPFAttribute(item.Attr, "role")
				add("contributors", strings.ToLower(role)+":"+value)
			case "date":
				key := "pubdate"
				if event, ok := refreshOPFAttribute(item.Attr, "event"); ok && event != "" && event != "publication" {
					key = "date:" + strings.ToLower(event)
				}
				add(key, value)
			case "language":
				add("languages", NormalizeLanguageForCalibre(value))
			case "subject":
				add("tags", value)
			case "description":
				add("comments", value)
			case "identifier":
				scheme, _ := refreshOPFAttribute(item.Attr, "scheme")
				scheme = strings.ToLower(scheme)
				if scheme == "isbn" {
					if isbn := isbnutil.ToISBN13(value); isbn != "" {
						value = isbn
					}
				}
				add("identifiers", scheme+":"+value)
			case "meta":
				name, _ := refreshOPFAttribute(item.Attr, "name")
				if name == "" {
					name, _ = refreshOPFAttribute(item.Attr, "property")
				}
				if !strings.HasPrefix(name, "calibre:") {
					break // Ignore presentation/OPF bookkeeping, not editable metadata.
				}
				if content, ok := refreshOPFAttribute(item.Attr, "content"); ok {
					value = content
				}
				if strings.HasPrefix(name, "calibre:user_metadata:") {
					value = canonicalOPFCustomValue(value)
				}
				add(name, value)
			}
		}
	}
	// The order of subjects, languages and identifier declarations in OPF
	// is not a change to Calibre's stored values. Author order is meaningful.
	for _, key := range []string{"tags", "languages", "identifiers"} {
		if values, ok := fields[key]; ok {
			slices.Sort(values)
			fields[key] = slices.Compact(values)
		}
	}
	return fields, nil
}

func canonicalOPFCustomValue(value string) string {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber() // Do not collapse distinct large numeric custom values via float64.
	var decoded, extra any
	if decoder.Decode(&decoded) == nil && errors.Is(decoder.Decode(&extra), io.EOF) {
		if encoded, err := json.Marshal(decoded); err == nil {
			return string(encoded) // Object key order is not metadata.
		}
	}
	return value
}

func refreshOPFAttribute(attrs []xml.Attr, name string) (string, bool) {
	for _, attr := range attrs {
		if attr.Name.Local == name {
			return attr.Value, true
		}
	}
	return "", false
}

func retainedOPFChange(before, after map[string][]string, approved map[string]bool) string {
	keys := make([]string, 0, len(before)+len(after))
	for key := range before {
		keys = append(keys, key)
	}
	for key := range after {
		if _, exists := before[key]; !exists {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		if approved[key] {
			continue
		}
		old, hadOld := before[key]
		newValue, hasNew := after[key]
		if hadOld != hasNew || !reflect.DeepEqual(old, newValue) {
			return key
		}
	}
	return ""
}
