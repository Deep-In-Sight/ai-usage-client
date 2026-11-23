package discover

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// MulticastAddr is the mDNS group.
var MulticastAddr = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// settleAfterAnswer is how long QueryA keeps listening after the first
// matching answer, for other responders.
const settleAfterAnswer = 300 * time.Millisecond

// maxAddrs bounds how many addresses one lookup may return.
const maxAddrs = 4

// QueryA sends "<name>." A to dst from an ephemeral port (legacy unicast,
// RFC 6762 6.7) and collects A records for exactly that name from answers
// and additional records. Records for other names are ignored: some devices
// answer every query with their own name. No answer is not an error.
func QueryA(ctx context.Context, name string, dst *net.UDPAddr, timeout time.Duration) ([]net.IP, error) {
	fqdn := dns.Fqdn(name)
	m := new(dns.Msg)
	m.SetQuestion(fqdn, dns.TypeA)
	m.RecursionDesired = false
	pkt, err := m.Pack()
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()
	if _, err := conn.WriteToUDP(pkt, dst); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	seen := map[string]bool{}
	var ips []net.IP
	buf := make([]byte, 9000)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		_ = conn.SetReadDeadline(deadline)
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		var r dns.Msg
		// legacy unicast replies repeat the query ID (RFC 6762 6.7)
		if r.Unpack(buf[:n]) != nil || !r.Response || r.Id != m.Id {
			continue
		}
		for _, rr := range append(r.Answer, r.Extra...) {
			a, ok := rr.(*dns.A)
			if !ok || !strings.EqualFold(a.Hdr.Name, fqdn) || !usable(a.A) || len(ips) >= maxAddrs {
				continue
			}
			if s := a.A.String(); !seen[s] {
				seen[s] = true
				ips = append(ips, a.A)
			}
		}
		if len(ips) > 0 {
			if short := time.Now().Add(settleAfterAnswer); short.Before(deadline) {
				deadline = short
			}
		}
	}
	return ips, nil
}

// usable drops addresses no server can have: loopback, unspecified,
// multicast and broadcast.
func usable(ip net.IP) bool {
	ip4 := ip.To4()
	return ip4 != nil && !ip4.IsLoopback() && !ip4.IsUnspecified() && !ip4.IsMulticast() &&
		!ip4.IsLinkLocalMulticast() && !ip4.Equal(net.IPv4bcast)
}
