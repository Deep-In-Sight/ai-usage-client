package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/creds"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/version"
)

const fakeToken = "fake-access-token-0123456789"

type fakeSrc struct {
	calls int
	err   error
}

func (f *fakeSrc) WithToken(ctx context.Context, fn func(string) error) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	return fn(fakeToken)
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

var (
	ctx = context.Background()
	t0  = time.Date(2026, 1, 5, 9, 0, 0, 123456789, time.UTC)
)

func TestFetchSendsHeaders(t *testing.T) {
	body := `{"seven_day":{"utilization":5}}`
	var got http.Header
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, method = r.Header.Clone(), r.Method
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	res, err := New(srv.URL, version.UserAgent(), clock.NewFake(t0)).Fetch(ctx, &fakeSrc{})
	must(t, err)
	eq(t, method, "GET")
	eq(t, got.Get("Authorization"), "Bearer "+fakeToken)
	eq(t, got.Get("anthropic-beta"), "oauth-2025-04-20")
	eq(t, got.Get("Accept"), "application/json")
	eq(t, got.Get("User-Agent"), "claude-usage/0.0.0-dev")
	eq(t, res.HTTPStatus, 200)
	eq(t, string(res.Usage), body)
	eq(t, res.FetchedAt, t0.Truncate(time.Millisecond))
}

func TestUsageURLIsFixed(t *testing.T) {
	eq(t, UsageURL, "https://api.anthropic.com/api/oauth/usage")
}

func TestFetchRefusesRedirect(t *testing.T) {
	var hits int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer other.Close()
	srv := httptest.NewServer(http.RedirectHandler(other.URL, http.StatusFound))
	defer srv.Close()
	r, err := New(srv.URL, "ua", clock.NewFake(t0)).Fetch(ctx, &fakeSrc{})
	must(t, err)
	eq(t, r.HTTPStatus, 302)
	eq(t, atomic.LoadInt32(&hits), int32(0))
	eq(t, r.Usage == nil, true)
}

func TestFetchStatuses(t *testing.T) {
	big := strings.Repeat(" ", 2<<20) + "{}"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/429":
			w.Header().Set("Retry-After", "300")
			w.WriteHeader(429)
		case "/401":
			w.WriteHeader(401)
		case "/html":
			fmt.Fprint(w, "<html>")
		case "/array":
			fmt.Fprint(w, "[1]")
		case "/big":
			fmt.Fprint(w, big)
		}
	}))
	defer srv.Close()
	c := func(p string) Result {
		r, err := New(srv.URL+p, "ua", clock.NewFake(t0)).Fetch(ctx, &fakeSrc{})
		must(t, err)
		return r
	}
	r := c("/429")
	eq(t, r.HTTPStatus, 429)
	eq(t, r.RetryAfter, 300*time.Second)
	eq(t, r.Usage == nil, true)
	eq(t, c("/401").HTTPStatus, 401)
	r = c("/html")
	eq(t, r.HTTPStatus, 200)
	eq(t, r.Usage == nil, true)
	eq(t, c("/array").Usage == nil, true)
	r = c("/big")
	eq(t, r.Usage == nil, true)
	if r.Problem == "" {
		t.Fatal("big body should have a problem")
	}
}

func TestRetryAfterDate(t *testing.T) {
	now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	eq(t, parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now), 90*time.Second)
	eq(t, parseRetryAfter("junk", now), time.Duration(0))
	eq(t, parseRetryAfter("-5", now), time.Duration(0))
}

func TestFetchNoResponseIsStatusZero(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	r, err := New(u, "ua", clock.NewFake(t0)).Fetch(ctx, &fakeSrc{})
	must(t, err)
	eq(t, r.HTTPStatus, 0)
	if r.Problem == "" || strings.Contains(r.Problem, "fake-access-token") {
		t.Fatalf("problem %q", r.Problem)
	}
}

func TestFetchNotAttemptedWithoutLogin(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	src := &fakeSrc{err: creds.ErrExpired}
	_, err := New(srv.URL, "ua", clock.NewFake(t0)).Fetch(ctx, src)
	eq(t, errors.Is(err, creds.ErrExpired), true)
	eq(t, atomic.LoadInt32(&hits), int32(0))
	eq(t, src.calls, 1)
}

func TestBackoff(t *testing.T) {
	eq(t, Backoff(1, 0), 120*time.Second)
	eq(t, Backoff(2, 0), 240*time.Second)
	eq(t, Backoff(3, 0), 480*time.Second)
	eq(t, Backoff(4, 0), 600*time.Second)
	eq(t, Backoff(9, 0), 600*time.Second)
	eq(t, Backoff(1, 300*time.Second), 300*time.Second)
	eq(t, Backoff(1, 3000*time.Second), 600*time.Second)
	eq(t, Backoff(60, 0), 600*time.Second)
}
