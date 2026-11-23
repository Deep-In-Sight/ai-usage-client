package discover

import (
	"reflect"
	"testing"
)

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestCandidatesOrder(t *testing.T) {
	got := Candidates("http://192.0.2.10:8700/", []string{"http://claude-usage.local", "http://192.0.2.20:8700"},
		[]string{"http://192.0.2.10:8700", "bad url", "http://192.0.2.30:8700"})
	eq(t, got, []string{"http://192.0.2.10:8700", "http://claude-usage.local", "http://claude-usage.local:8700",
		"http://192.0.2.20:8700", "http://192.0.2.30:8700"})
}

func TestCandidatesEmptyLastGood(t *testing.T) {
	got := Candidates("", nil, []string{"http://192.0.2.30:8700"})
	eq(t, got, []string{"http://claude-usage.local", "http://claude-usage.local:8700", "http://192.0.2.30:8700"})
	eq(t, MDNSURLs, []string{"http://claude-usage.local", "http://claude-usage.local:8700"})
}
