package reconcile

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// AddrFromIP converts an address to its canonical form, where IPv4-in-IPv6 is plain IPv4.
func AddrFromIP(ip net.IP) (netip.Addr, bool) {
	addr, ok := netip.AddrFromSlice(ip)
	return addr.Unmap(), ok
}

// PrefixFromIPNet converts to the canonical prefix: with the host bits cleared, and IPv4 as
// netip.Is4. This makes prefixes comparable, which net.IPNet is not (4 or 16 byte IPs and masks).
// The family follows the mask, so ::ffff:0:0/96 stays IPv6 while a 16 byte IP with a 32 bit mask is IPv4.
func PrefixFromIPNet(n net.IPNet) (netip.Prefix, bool) {
	addr, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return netip.Prefix{}, false
	}

	ones, bits := n.Mask.Size()
	switch {
	case bits == 0: // non-canonical mask
		return netip.Prefix{}, false
	case bits == 32:
		addr = addr.Unmap()
	case addr.Is4() && bits == 128: // 4 byte IP with an IPv6 sized mask
		ones -= 96
	}

	prefix := netip.PrefixFrom(addr, ones)
	if !prefix.IsValid() {
		return netip.Prefix{}, false
	}
	return prefix.Masked(), true
}

// ToIPNet converts a prefix, IPv4 gets 4 byte IP and mask.
func ToIPNet(p netip.Prefix) net.IPNet {
	return net.IPNet{
		IP:   p.Addr().AsSlice(),
		Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen()),
	}
}

// ParsePrefixes parses a comma-separated list of CIDRs.
func ParsePrefixes(list string) (map[netip.Prefix]struct{}, error) {
	prefixes := make(map[netip.Prefix]struct{})
	for _, entry := range strings.Split(list, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid prefix '%s': %w", entry, err)
		}
		prefixes[prefix.Masked()] = struct{}{}
	}
	return prefixes, nil
}
