package main

import (
	"net"
	"net/netip"
	"slices"
	"testing"

	"git.ruekov.eu/ruakij/routingtabletowg/lib/reconcile"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestToRoutes(t *testing.T) {
	_, v4Net, _ := net.ParseCIDR("203.0.113.0/24")
	_, v6Net, _ := net.ParseCIDR("2001:db8::/32")

	list := []netlink.Route{
		{Dst: v4Net, Gw: net.ParseIP("10.0.0.1"), Priority: 5, Table: 254, Family: unix.AF_INET},
		{Dst: v6Net, Gw: net.ParseIP("fd00::1"), Priority: 1024, Family: unix.AF_INET6},
		// default routes have no Dst, the family comes from the route and not from the gateway
		{Gw: net.ParseIP("10.0.0.1"), Family: unix.AF_INET},
		{Gw: net.ParseIP("fd00::1"), Family: unix.AF_INET6},
		// gateway as 16 bytes
		{Gw: net.ParseIP("10.0.0.2").To16(), Family: unix.AF_INET},
		// no gateway
		{Dst: v4Net, Family: unix.AF_INET},
		{Family: unix.AF_INET},
		// no destination to tell
		{Gw: net.ParseIP("10.0.0.1")},
	}

	got, skipped := toRoutes(list)
	if len(skipped) != 0 {
		t.Errorf("skipped %v", skipped)
	}
	want := []reconcile.Route{
		{Dst: netip.MustParsePrefix("203.0.113.0/24"), Gw: netip.MustParseAddr("10.0.0.1"), Metric: 5, Table: 254},
		{Dst: netip.MustParsePrefix("2001:db8::/32"), Gw: netip.MustParseAddr("fd00::1"), Metric: 1024},
		{Dst: netip.MustParsePrefix("0.0.0.0/0"), Gw: netip.MustParseAddr("10.0.0.1")},
		{Dst: netip.MustParsePrefix("::/0"), Gw: netip.MustParseAddr("fd00::1")},
		{Dst: netip.MustParsePrefix("0.0.0.0/0"), Gw: netip.MustParseAddr("10.0.0.2")},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

// wgctrl would send an IPv4-mapped IPv6 destination as IPv4 with a mask too long for it,
// which the kernel rejects for the whole call.
func TestToRoutesSkipsUnwritableDestinations(t *testing.T) {
	_, mapped, _ := net.ParseCIDR("::ffff:10.0.0.0/104")
	_, mappedRange, _ := net.ParseCIDR("::ffff:0:0/96")
	_, short, _ := net.ParseCIDR("::ffff:0:0/80") // nothing of the mapped form is left in it
	_, plain, _ := net.ParseCIDR("2001:db8::/32")

	list := []netlink.Route{
		{Dst: mapped, Gw: net.ParseIP("fd00::1"), Family: unix.AF_INET6},
		{Dst: mappedRange, Gw: net.ParseIP("fd00::1"), Family: unix.AF_INET6},
		{Dst: short, Gw: net.ParseIP("fd00::1"), Family: unix.AF_INET6},
		{Dst: plain, Gw: net.ParseIP("fd00::1"), Family: unix.AF_INET6},
		// without gateway it is not translated anyway, so there is nothing to report
		{Dst: mapped, Family: unix.AF_INET6},
	}

	routes, skipped := toRoutes(list)

	if len(routes) != 2 || routes[0].Dst != netip.MustParsePrefix("::/80") || routes[1].Dst != netip.MustParsePrefix("2001:db8::/32") {
		t.Errorf("routes = %v", routes)
	}
	wantSkipped := []netip.Prefix{netip.MustParsePrefix("::ffff:10.0.0.0/104"), netip.MustParsePrefix("::ffff:0.0.0.0/96")}
	if !slices.Equal(skipped, wantSkipped) {
		t.Errorf("skipped = %v, want %v", skipped, wantSkipped)
	}
}

// wgctrl reports IPv4 prefixes with 4 byte IPs and masks, so they must equal the synthesized default route.
func TestToPeersMatchesDefaultRoute(t *testing.T) {
	device := &wgtypes.Device{Peers: []wgtypes.Peer{{
		PublicKey: wgtypes.Key{1},
		AllowedIPs: []net.IPNet{
			{IP: net.IP{0, 0, 0, 0}, Mask: net.CIDRMask(0, 32)},
			{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
		},
	}}}

	peers := toPeers(device)
	want := []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}
	if len(peers) != 1 || !slices.Equal(peers[0].AllowedIPs, want) {
		t.Errorf("got %v, want %v", peers, want)
	}

	routes, _ := toRoutes([]netlink.Route{{Gw: net.ParseIP("10.0.0.1"), Family: unix.AF_INET}})
	if len(routes) != 1 || !slices.Contains(peers[0].AllowedIPs, routes[0].Dst) {
		t.Errorf("default route %v not found in %v", routes, peers[0].AllowedIPs)
	}
}

// A prefix that is IPv6 for WireGuard must not turn into an IPv4 one on the way back.
func TestRemovalKeepsOtherEntriesAsTheyAre(t *testing.T) {
	mapped := net.IPNet{IP: net.ParseIP("::ffff:0:0"), Mask: net.CIDRMask(96, 128)}
	v4Default := net.IPNet{IP: net.IP{0, 0, 0, 0}, Mask: net.CIDRMask(0, 32)}
	other := net.IPNet{IP: net.IP{10, 1, 2, 0}, Mask: net.CIDRMask(24, 32)}
	list := []net.IPNet{mapped, v4Default, other}

	device := &wgtypes.Device{Peers: []wgtypes.Peer{{PublicKey: wgtypes.Key{1}, AllowedIPs: list}}}
	wantPrefixes := []netip.Prefix{netip.MustParsePrefix("::ffff:0.0.0.0/96"), netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("10.1.2.0/24")}
	if got := toPeers(device)[0].AllowedIPs; !slices.Equal(got, wantPrefixes) {
		t.Fatalf("got %v, want %v", got, wantPrefixes)
	}

	got := withoutPrefixes(list, []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("203.0.113.0/24")})
	want := []net.IPNet{mapped, other}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].IP.Equal(want[i].IP) || len(got[i].IP) != len(want[i].IP) || !slices.Equal(got[i].Mask, want[i].Mask) {
			t.Errorf("entry %d: got %v, want %v", i, got[i], want[i])
		}
	}
	if len(list) != 3 {
		t.Errorf("input was modified: %v", list)
	}
}

func TestRouteFilter(t *testing.T) {
	tests := []struct {
		name       string
		options    FilterOptions
		wantTable  int
		wantProto  bool
		protoValue netlink.RouteProtocol
	}{
		{name: "all", options: FilterOptions{Table: -1, Protocol: -1}},
		{name: "table", options: FilterOptions{Table: 100, Protocol: -1}, wantTable: 100},
		{name: "protocol", options: FilterOptions{Table: -1, Protocol: 12}, wantProto: true, protoValue: 12},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter, mask := tt.options.routeFilter(7)
			if mask&netlink.RT_FILTER_OIF == 0 || mask&netlink.RT_FILTER_TABLE == 0 || filter.LinkIndex != 7 {
				t.Errorf("link and table must always be filtered: %v %b", filter, mask)
			}
			if filter.Table != tt.wantTable {
				t.Errorf("table = %d, want %d", filter.Table, tt.wantTable)
			}
			if (mask&netlink.RT_FILTER_PROTOCOL != 0) != tt.wantProto || filter.Protocol != tt.protoValue {
				t.Errorf("protocol filter = %v/%d, want %v/%d", mask&netlink.RT_FILTER_PROTOCOL != 0, filter.Protocol, tt.wantProto, tt.protoValue)
			}
		})
	}
}
