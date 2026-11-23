package autostart

import (
	"context"
	"reflect"
	"testing"
)

type fakeRun struct {
	calls [][]string
	reply func(args []string) (string, error)
}

func (f *fakeRun) run(_ context.Context, name string, args ...string) (string, error) {
	c := append([]string{name}, args...)
	f.calls = append(f.calls, c)
	if f.reply != nil {
		return f.reply(c)
	}
	return "", nil
}

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

var bg = context.Background()
