package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestTimeFormat(t *testing.T) {
	b, err := json.Marshal(Time{time.Date(2026, 1, 5, 9, 0, 0, 123456789, time.UTC)})
	must(t, err)
	eq(t, string(b), `"2026-01-05T09:00:00.123Z"`)
	loc := time.FixedZone("X", 9*3600)
	b, _ = json.Marshal(Time{time.Date(2026, 1, 5, 18, 0, 0, 0, loc)})
	eq(t, string(b), `"2026-01-05T09:00:00.000Z"`)

	var tm Time
	must(t, json.Unmarshal([]byte(`"2026-01-08T21:00:00.000000+00:00"`), &tm))
	eq(t, tm.UTC(), time.Date(2026, 1, 8, 21, 0, 0, 0, time.UTC))
	if json.Unmarshal([]byte(`null`), &tm) == nil {
		t.Fatal("null Time should fail")
	}

	b, _ = json.Marshal(Result{AccountUUID: "acc-1", FetchedAt: Time{time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)}, HTTPStatus: 0})
	if !strings.Contains(string(b), `"usage":null`) {
		t.Fatal(string(b))
	}
	b, _ = json.Marshal(Request{Schema: 1})
	if !strings.Contains(string(b), `"results":[]`) || !strings.Contains(string(b), `"account":null`) {
		t.Fatal(string(b))
	}
	if !strings.Contains(string(b), `"token_expires_at":null`) || !strings.Contains(string(b), `"hostname":null`) {
		t.Fatal(string(b))
	}
}

func TestOpt(t *testing.T) {
	b, _ := json.Marshal(struct {
		A Opt `json:"a"`
		B Opt `json:"b"`
	}{"x", ""})
	eq(t, string(b), `{"a":"x","b":null}`)
	var v struct {
		A Opt `json:"a"`
		B Opt `json:"b"`
	}
	must(t, json.Unmarshal([]byte(`{"a":null,"b":"y"}`), &v))
	eq(t, v.A, Opt(""))
	eq(t, v.B, Opt("y"))
}
