package main

import (
	"net"
	"net/netip"
	"slices"

	"git.ruekov.eu/ruakij/routingtabletowg/lib/reconcile"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// routeDst returns the destination of a route, the default route has none.
func routeDst(route netlink.Route) (netip.Prefix, bool) {
	if route.Dst != nil {
		return reconcile.PrefixFromIPNet(*route.Dst)
	}

	// The family comes from the route, as a default route may have no gateway to tell it
	switch route.Family {
	case unix.AF_INET:
		return netip.PrefixFrom(netip.IPv4Unspecified(), 0), true
	case unix.AF_INET6:
		return netip.PrefixFrom(netip.IPv6Unspecified(), 0), true
	}
	return netip.Prefix{}, false
}

// toRoutes converts the routes that can be translated, which are the ones with a
// gateway and a destination that can be written to WireGuard. The destinations of
// the latter are returned as skipped.
func toRoutes(list []netlink.Route) (routes []reconcile.Route, skipped []netip.Prefix) {
	routes = make([]reconcile.Route, 0, len(list))
	for _, route := range list {
		gw, ok := reconcile.AddrFromIP(route.Gw)
		if !ok {
			continue
		}
		dst, ok := routeDst(route)
		if !ok {
			continue
		}
		if !writable(reconcile.ToIPNet(dst)) {
			skipped = append(skipped, dst)
			continue
		}
		routes = append(routes, reconcile.Route{
			Dst:    dst,
			Gw:     gw,
			Metric: route.Priority,
			Table:  route.Table,
		})
	}
	return routes, skipped
}

func toPeers(device *wgtypes.Device) []reconcile.Peer {
	peers := make([]reconcile.Peer, 0, len(device.Peers))
	for _, peer := range device.Peers {
		allowed := make([]netip.Prefix, 0, len(peer.AllowedIPs))
		for _, ipNet := range peer.AllowedIPs {
			if prefix, ok := reconcile.PrefixFromIPNet(ipNet); ok {
				allowed = append(allowed, prefix)
			}
		}
		peers = append(peers, reconcile.Peer{Key: peer.PublicKey, AllowedIPs: allowed})
	}
	return peers
}

func toIPNets(prefixes []netip.Prefix) []net.IPNet {
	ipNets := make([]net.IPNet, len(prefixes))
	for i, prefix := range prefixes {
		ipNets[i] = reconcile.ToIPNet(prefix)
	}
	return ipNets
}

// withoutPrefixes returns the list without the entries equal to one of the prefixes.
// The others are returned as they are, so nothing gets rewritten into another form.
func withoutPrefixes(list []net.IPNet, drop []netip.Prefix) []net.IPNet {
	return slices.DeleteFunc(slices.Clone(list), func(n net.IPNet) bool {
		prefix, ok := reconcile.PrefixFromIPNet(n)
		return ok && slices.Contains(drop, prefix)
	})
}

// writable tells if wgctrl sends the entry to the kernel as it is. wgctrl takes
// every IP with a 4 byte form for IPv4, so an IPv4-mapped IPv6 entry such as
// ::ffff:10.0.0.0/104 goes out as IPv4 with a mask too long for it and is rejected.
func writable(n net.IPNet) bool {
	_, bits := n.Mask.Size()
	return bits != 128 || n.IP.To4() == nil
}

func prefixString(n net.IPNet) string {
	if prefix, ok := reconcile.PrefixFromIPNet(n); ok {
		return prefix.String()
	}
	return n.String()
}
