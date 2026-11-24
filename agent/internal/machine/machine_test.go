package machine

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The expected value was computed once with a separate program using only
// crypto/hmac (and cross-checked with openssl dgst -sha256 -hmac).
func TestDeriveKnownValue(t *testing.T) {
	if got := derive("0123456789abcdef0123456789abcdef"); got != "45e4c5d86796dfe2ebe31d955cc586a6" {
		t.Fatalf("derive = %s", got)
	}
}

func TestDeriveNormalises(t *testing.T) {
	if derive(" ABC ") != derive("abc") || derive("abc") != "8d1ec9b23181d3e4ceca30c35b81d1a5" {
		t.Fatal("case or spaces change the key")
	}
	if derive("") != "" || derive("  ") != "" {
		t.Fatal("an empty source gives a key")
	}
}

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Key is the same on every call; on the CI runners (Linux, Windows, macOS)
// the OS source is there, so the key comes from it.
func TestKeyIsStable(t *testing.T) {
	k1, s1 := Key()
	k2, s2 := Key()
	if k1 != k2 || s1 != s2 {
		t.Fatal("two calls differ")
	}
	// computed afresh, not only remembered
	k3, s3 := compute(osSource)
	if k3 != k1 || s3 != s1 {
		t.Fatalf("recomputed %q %q, first %q %q", k3, s3, k1, s1)
	}
	if k1 == "" {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("no machine key on a CI runner")
		}
		t.Skip("this host has no OS machine id")
	}
	if !hex32.MatchString(k1) || s1 != "os" {
		t.Fatalf("key %q source %q", k1, s1)
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" && s1 != "os" {
		t.Fatalf("the runner's OS machine id was not read (source %q)", s1)
	}
	t.Logf("source %s", s1)
}

// The key never holds the machine id it came from.
func TestRawValueNeverInKey(t *testing.T) {
	raw, ok := osSource()
	if !ok {
		t.Skip("no OS machine id here")
	}
	k, _ := Key()
	norm := strings.ToLower(strings.TrimSpace(raw))
	if k == norm || strings.Contains(norm, k) || strings.Contains(k, norm) || strings.Contains(k, strings.ReplaceAll(norm, "-", "")[:8]) {
		t.Fatal("the key shows the machine id")
	}
}

// Without an OS machine identifier there is no key (review I1): a key made
// from a network card's address could be turned back into the address.
func TestNoKeyWithoutAnOSIdentifier(t *testing.T) {
	if k, s := compute(func() (string, bool) { return "", false }); k != "" || s != "" {
		t.Fatalf("%q %q", k, s)
	}
	if k, s := compute(func() (string, bool) { return "   ", true }); k != "" || s != "" {
		t.Fatalf("blank identifier: %q %q", k, s)
	}
	if k, s := compute(func() (string, bool) { return "0123456789ABCDEF0123456789ABCDEF", true }); s != "os" || k != "45e4c5d86796dfe2ebe31d955cc586a6" {
		t.Fatalf("%q %q", k, s)
	}
}

// The package reads no network interface at all.
func TestNoNetworkCardCode(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "net.Interfaces") || strings.Contains(string(b), "HardwareAddr") {
			t.Fatalf("%s reads network cards", e.Name())
		}
	}
}
