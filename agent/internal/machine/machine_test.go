package machine

import (
	"net"
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
	if got := derive("aa:bb:cc:dd:ee:01"); got != "cd9784fc94aee685125aad8d31106f32" {
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
	k3, s3 := compute(osSource, interfaces)
	if k3 != k1 || s3 != s1 {
		t.Fatalf("recomputed %q %q, first %q %q", k3, s3, k1, s1)
	}
	if k1 == "" {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("no machine key on a CI runner")
		}
		t.Skip("this host has no machine id and no usable network card")
	}
	if !hex32.MatchString(k1) || (s1 != "os" && s1 != "mac") {
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

func mac(s string) net.HardwareAddr {
	h, err := net.ParseMAC(s)
	if err != nil {
		panic(err)
	}
	return h
}

// The fallback takes a real network card: up, not loopback, a globally
// administered unicast address, not a virtual interface; the lowest index
// wins.
func TestMACFilter(t *testing.T) {
	up := net.FlagUp
	ifs := []iface{
		{Name: "lo", Index: 1, Flags: up | net.FlagLoopback, HardwareAddr: mac("00:00:00:00:00:00")},
		{Name: "docker0", Index: 2, Flags: up, HardwareAddr: mac("00:1b:21:00:00:01")},
		{Name: "wlan0", Index: 3, Flags: up, HardwareAddr: mac("02:1b:21:00:00:02")}, // locally administered (randomised)
		{Name: "eth9", Index: 4, Flags: up, HardwareAddr: mac("01:1b:21:00:00:03")},  // multicast
		{Name: "eth1", Index: 6, Flags: up, HardwareAddr: mac("00:1b:21:00:00:06")},
		{Name: "eth0", Index: 5, Flags: up, HardwareAddr: mac("00:1b:21:00:00:05")},
		{Name: "eth2", Index: 0, Flags: 0, HardwareAddr: mac("00:1b:21:00:00:07")},                                           // down
		{Name: "ib0", Index: 0, Flags: up, HardwareAddr: mac("00:00:00:00:fe:80:00:00:00:00:00:00:00:00:00:00:00:00:00:01")}, // not 6 bytes
		{Name: "veth1a", Index: 0, Flags: up, HardwareAddr: mac("00:1b:21:00:00:08")},
		{Name: "br-1234", Index: 0, Flags: up, HardwareAddr: mac("00:1b:21:00:00:09")},
		{Name: "utun3", Index: 0, Flags: up, HardwareAddr: mac("00:1b:21:00:00:0a")},
		{Name: "vboxnet0", Index: 0, Flags: up, HardwareAddr: mac("00:1b:21:00:00:0b")},
	}
	if got := pickMAC(ifs); got != "00:1b:21:00:00:05" {
		t.Fatalf("picked %q", got)
	}
	if got := pickMAC(ifs[:4]); got != "" {
		t.Fatalf("picked %q from no real card", got)
	}
	// with no OS id the key comes from the card, marked "mac"
	k, s := compute(func() (string, bool) { return "", false }, func() []iface { return ifs })
	if s != "mac" || k != derive("00:1b:21:00:00:05") {
		t.Fatalf("%q %q", k, s)
	}
	if k, s := compute(func() (string, bool) { return "", false }, func() []iface { return nil }); k != "" || s != "" {
		t.Fatalf("%q %q", k, s)
	}
	if k, s := compute(func() (string, bool) { return "0123456789ABCDEF0123456789ABCDEF", true }, func() []iface { return ifs }); s != "os" || k != "45e4c5d86796dfe2ebe31d955cc586a6" {
		t.Fatalf("%q %q", k, s)
	}
}
