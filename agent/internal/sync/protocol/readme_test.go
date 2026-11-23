package protocol

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// fieldNames lists the JSON names of every field a sync request can carry,
// as dotted paths ("client.version", "results[].fetched_at").
func fieldNames(t reflect.Type, prefix string) []string {
	if t == reflect.TypeOf(json.RawMessage{}) {
		return []string{prefix}
	}
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		if t.Kind() == reflect.Slice {
			prefix += "[]"
		}
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t == reflect.TypeOf(Time{}) {
		return []string{prefix}
	}
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		p := name
		if prefix != "" {
			p = prefix + "." + name
		}
		out = append(out, fieldNames(f.Type, p)...)
	}
	return out
}

// TestReadmeListsEveryField fails when a field is added to the sync request
// without the README's "What it sends" list being updated.
func TestReadmeListsEveryField(t *testing.T) {
	b, err := os.ReadFile("../../../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	names := fieldNames(reflect.TypeOf(Request{}), "")
	if len(names) < 20 {
		t.Fatalf("only %d fields found: %v", len(names), names)
	}
	for _, n := range names {
		if !strings.Contains(readme, "`"+n+"`") {
			t.Errorf("README.md does not list the sync field `%s`", n)
		}
	}
}
