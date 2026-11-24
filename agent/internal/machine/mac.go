package machine

import (
	"net"
	"strings"
)

// iface is what the fallback needs of a network interface.
type iface struct {
	Name         string
	Index        int
	Flags        net.Flags
	HardwareAddr net.HardwareAddr
}

func interfaces() []iface {
	list, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]iface, 0, len(list))
	for _, i := range list {
		out = append(out, iface{Name: i.Name, Index: i.Index, Flags: i.Flags, HardwareAddr: i.HardwareAddr})
	}
	return out
}

// virtual are name prefixes of interfaces that are not a PC's own card.
var virtual = []string{"docker", "veth", "br-", "virbr", "vmnet", "vboxnet", "utun", "tun", "tap", "wg", "zt"}

// pickMAC is the address ("aa:bb:…") of the first real card by index: up,
// not loopback, 6 bytes, globally administered (bit 0x02 of the first byte
// clear: not a randomised or virtual address) and unicast (bit 0x01 clear),
// not a virtual interface. "" when there is none.
func pickMAC(ifs []iface) string {
	best, bestIndex := "", 0
	for _, i := range ifs {
		h := i.HardwareAddr
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || len(h) != 6 || h[0]&0x03 != 0 {
			continue
		}
		if isVirtual(i.Name) {
			continue
		}
		if best == "" || i.Index < bestIndex {
			best, bestIndex = h.String(), i.Index
		}
	}
	return best
}

func isVirtual(name string) bool {
	n := strings.ToLower(name)
	for _, p := range virtual {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}
