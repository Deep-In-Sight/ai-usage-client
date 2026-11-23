package discover

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// responder answers every query with a record for another name first (like
// a device on the LAN that answers all queries with its own name), then the
// queried one when answer is true.
func responder(t *testing.T, answer bool) *net.UDPAddr {
	t.Helper()
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	must(t, err)
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if !answer {
				continue
			}
			var q dns.Msg
			if q.Unpack(buf[:n]) != nil || len(q.Question) != 1 {
				continue
			}
			r := new(dns.Msg)
			r.SetReply(&q)
			r.Authoritative = true
			other, _ := dns.NewRR("SEC0000.local. 10 IN A 192.0.2.99")
			mine, _ := dns.NewRR(q.Question[0].Name + " 10 IN A 192.0.2.10")
			r.Answer = []dns.RR{other, mine}
			extra, _ := dns.NewRR("SEC0000.local. 10 IN A 192.0.2.98")
			r.Extra = []dns.RR{extra}
			b, _ := r.Pack()
			_, _ = pc.WriteToUDP(b, from)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr)
}

func ipStrings(ips []net.IP) []string {
	out := []string{}
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out
}

func TestQueryAKeepsOnlyQueriedName(t *testing.T) {
	addr := responder(t, true)
	ips, err := QueryA(context.Background(), "claude-usage.local", addr, time.Second)
	must(t, err)
	eq(t, ipStrings(ips), []string{"192.0.2.10"})
	// the name match ignores case
	ips, err = QueryA(context.Background(), "Claude-Usage.LOCAL", addr, time.Second)
	must(t, err)
	eq(t, ipStrings(ips), []string{"192.0.2.10"})
}

func TestQueryAStopsShortlyAfterAnswer(t *testing.T) {
	addr := responder(t, true)
	start := time.Now()
	_, err := QueryA(context.Background(), "claude-usage.local", addr, 3*time.Second)
	must(t, err)
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("took %v after the first answer", d)
	}
}

func TestQueryATimeoutNoAnswer(t *testing.T) {
	addr := responder(t, false)
	start := time.Now()
	ips, err := QueryA(context.Background(), "claude-usage.local", addr, 500*time.Millisecond)
	must(t, err)
	eq(t, len(ips), 0)
	if d := time.Since(start); d < 400*time.Millisecond || d > 2*time.Second {
		t.Fatalf("returned after %v", d)
	}
}

// TestLiveQuery resolves the real name on this LAN once. It runs only with
// CLAUDE_USAGE_LIVE_MDNS=1 and asserts no particular address.
func TestLiveQuery(t *testing.T) {
	if os.Getenv("CLAUDE_USAGE_LIVE_MDNS") != "1" {
		t.Skip("set CLAUDE_USAGE_LIVE_MDNS=1")
	}
	ips, err := QueryA(context.Background(), Name, MulticastAddr, 2*time.Second)
	must(t, err)
	if len(ips) == 0 {
		t.Fatal("no answer")
	}
	t.Logf("%s -> %v", Name, ipStrings(ips))
}
