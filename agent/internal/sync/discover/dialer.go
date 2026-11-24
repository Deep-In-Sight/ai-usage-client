package discover

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/sync/protocol"
)

// LookupFunc resolves a .local name.
type LookupFunc func(ctx context.Context, name string) ([]net.IP, error)

const (
	defaultDialTimeout = 5 * time.Second
	cacheFor           = 60 * time.Second
	mdnsTimeout        = 2 * time.Second
)

var dialTimeout = defaultDialTimeout

type cached struct {
	ips []net.IP
	at  time.Time
}

// NewDialer resolves hosts ending in .local with lookupLocal (answers kept
// for 60 s, each address tried in turn) and dials everything else normally.
func NewDialer(lookupLocal LookupFunc) protocol.DialFunc {
	var mu sync.Mutex
	cache := map[string]cached{}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := &net.Dialer{Timeout: dialTimeout}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		name := strings.ToLower(strings.TrimSuffix(host, "."))
		if !strings.HasSuffix(name, ".local") {
			return d.DialContext(ctx, network, addr)
		}
		mu.Lock()
		c, ok := cache[name]
		mu.Unlock()
		if !ok || time.Since(c.at) > cacheFor {
			ips, err := lookupLocal(ctx, name)
			if err != nil {
				return nil, fmt.Errorf("mdns: %s: %v", name, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("mdns: %s: no answer", name)
			}
			c = cached{ips: ips, at: time.Now()}
			mu.Lock()
			cache[name] = c
			mu.Unlock()
		}
		var errs []error
		for _, ip := range c.ips {
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			errs = append(errs, err)
		}
		mu.Lock()
		delete(cache, name) // every address failed: ask again next time
		mu.Unlock()
		return nil, errors.Join(errs...)
	}
}

// PlatformDialer is the dialer the agent uses for the company server. On
// Linux it resolves .local names itself; elsewhere the OS resolver handles
// them and nil (the default dialer) is returned.
func PlatformDialer() protocol.DialFunc {
	if runtime.GOOS != "linux" {
		return nil
	}
	return NewDialer(func(ctx context.Context, name string) ([]net.IP, error) {
		return QueryA(ctx, name, MulticastAddr, mdnsTimeout)
	})
}
