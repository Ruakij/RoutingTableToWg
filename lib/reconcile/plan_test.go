package reconcile

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

var (
	keyA = wgtypes.Key{1}
	keyB = wgtypes.Key{2}
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func pfxs(list ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range list {
		out = append(out, pfx(s))
	}
	return out
}

func set(list ...string) Set {
	s := make(Set)
	for _, p := range list {
		s[pfx(p)] = struct{}{}
	}
	return s
}

func sorted(s Set) []string {
	var out []string
	for p := range s {
		out = append(out, p.String())
	}
	slices.Sort(out)
	return out
}

func route(dst, gw string, metric int) Route {
	return Route{Dst: pfx(dst), Gw: netip.MustParseAddr(gw), Metric: metric}
}

func peerA(allowed ...string) Peer { return Peer{Key: keyA, AllowedIPs: pfxs(allowed...)} }
func peerB(allowed ...string) Peer { return Peer{Key: keyB, AllowedIPs: pfxs(allowed...)} }

func peerName(key wgtypes.Key) string {
	return map[wgtypes.Key]string{keyA: "A", keyB: "B"}[key]
}

func render(plan Plan) []string {
	var out []string
	for _, c := range plan.Changes {
		var parts []string
		for _, p := range c.Add {
			parts = append(parts, "+"+p.String())
		}
		for _, p := range c.Remove {
			parts = append(parts, "-"+p.String())
		}
		out = append(out, fmt.Sprintf("%s %s", peerName(c.Peer), strings.Join(parts, " ")))
	}
	return out
}

func TestCompute(t *testing.T) {
	tests := []struct {
		name           string
		in             Input
		want           []string // changes
		wantManaged    []string
		wantUnresolved int
		wantConflicts  []string
	}{
		{
			name: "route is added to the peer containing the gateway",
			in: Input{
				Routes: []Route{route("203.0.113.0/24", "10.0.0.1", 0)},
				Peers:  []Peer{peerA("10.0.0.0/24"), peerB("10.1.0.0/24")},
			},
			want:        []string{"A +203.0.113.0/24"},
			wantManaged: []string{"203.0.113.0/24"},
		},
		{
			name: "gateway matches the longest prefix",
			in: Input{
				Routes: []Route{route("203.0.113.0/24", "10.1.0.1", 0)},
				Peers:  []Peer{peerA("10.0.0.0/8"), peerB("10.1.0.0/16")},
			},
			want:        []string{"B +203.0.113.0/24"},
			wantManaged: []string{"203.0.113.0/24"},
		},
		{
			name: "ipv6 gateway",
			in: Input{
				Routes: []Route{route("2001:db8::/32", "fd00::1", 1024)},
				Peers:  []Peer{peerA("10.0.0.0/24"), peerB("fd00::/64")},
			},
			want:        []string{"B +2001:db8::/32"},
			wantManaged: []string{"2001:db8::/32"},
		},
		{
			name: "default routes of both families",
			in: Input{
				Routes: []Route{route("0.0.0.0/0", "10.0.0.1", 0), route("::/0", "fd00::1", 1024)},
				Peers:  []Peer{peerA("10.0.0.0/24"), peerB("fd00::/64")},
			},
			want:        []string{"A +0.0.0.0/0", "B +::/0"},
			wantManaged: []string{"0.0.0.0/0", "::/0"},
		},
		{
			name: "gateway in no peer",
			in: Input{
				Routes: []Route{route("203.0.113.0/24", "192.168.9.9", 0)},
				Peers:  []Peer{peerA("10.0.0.0/24")},
			},
			wantUnresolved: 1,
		},
		{
			name: "startup: prefix on the right peer is kept and adopted",
			in: Input{
				Routes:  []Route{route("203.0.113.0/24", "10.0.0.1", 0)},
				Peers:   []Peer{peerA("10.0.0.0/24", "203.0.113.0/24")},
				Startup: true,
			},
			wantManaged: []string{"203.0.113.0/24"},
		},
		{
			name: "prefix on the wrong peer moves, without a removal",
			in: Input{
				Routes:  []Route{route("203.0.113.0/24", "10.1.0.1", 0)},
				Peers:   []Peer{peerA("10.0.0.0/24", "203.0.113.0/24"), peerB("10.1.0.0/24")},
				Managed: set("203.0.113.0/24"),
			},
			want:        []string{"B +203.0.113.0/24"},
			wantManaged: []string{"203.0.113.0/24"},
		},
		{
			name: "lowest metric wins",
			in: Input{
				Routes: []Route{route("203.0.113.0/24", "10.1.0.1", 200), route("203.0.113.0/24", "10.0.0.1", 100)},
				Peers:  []Peer{peerA("10.0.0.0/24"), peerB("10.1.0.0/24")},
			},
			want:        []string{"A +203.0.113.0/24"},
			wantManaged: []string{"203.0.113.0/24"},
		},
		{
			name: "equal metric prefers the main table, then the lower table, then the lower gateway",
			in: Input{
				Routes: []Route{
					{Dst: pfx("203.0.113.0/24"), Gw: netip.MustParseAddr("10.1.0.1"), Table: 100},
					{Dst: pfx("203.0.113.0/24"), Gw: netip.MustParseAddr("10.0.0.1"), Table: 254},
					{Dst: pfx("198.51.100.0/24"), Gw: netip.MustParseAddr("10.0.0.1"), Table: 200},
					{Dst: pfx("198.51.100.0/24"), Gw: netip.MustParseAddr("10.1.0.1"), Table: 100},
					{Dst: pfx("192.0.2.0/24"), Gw: netip.MustParseAddr("10.1.0.1"), Table: 254},
					{Dst: pfx("192.0.2.0/24"), Gw: netip.MustParseAddr("10.0.0.1"), Table: 254},
				},
				Peers: []Peer{peerA("10.0.0.0/24"), peerB("10.1.0.0/24")},
			},
			want:        []string{"A +192.0.2.0/24 +203.0.113.0/24", "B +198.51.100.0/24"},
			wantManaged: []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24"},
		},
		{
			name: "a lower metric wins over the main table",
			in: Input{
				Routes: []Route{
					{Dst: pfx("203.0.113.0/24"), Gw: netip.MustParseAddr("10.1.0.1"), Table: 100, Metric: 10},
					{Dst: pfx("203.0.113.0/24"), Gw: netip.MustParseAddr("10.0.0.1"), Table: 254, Metric: 20},
				},
				Peers: []Peer{peerA("10.0.0.0/24"), peerB("10.1.0.0/24")},
			},
			want:        []string{"B +203.0.113.0/24"},
			wantManaged: []string{"203.0.113.0/24"},
		},
		{
			name: "deleting the backup route keeps the primary peer's prefix",
			in: Input{
				Routes:  []Route{route("203.0.113.0/24", "10.0.0.1", 100)},
				Peers:   []Peer{peerA("10.0.0.0/24", "203.0.113.0/24"), peerB("10.1.0.0/24")},
				Managed: set("203.0.113.0/24"),
			},
			wantManaged: []string{"203.0.113.0/24"},
		},
		{
			name: "deleting the primary route moves the prefix to the backup peer",
			in: Input{
				Routes:  []Route{route("203.0.113.0/24", "10.1.0.1", 200)},
				Peers:   []Peer{peerA("10.0.0.0/24", "203.0.113.0/24"), peerB("10.1.0.0/24")},
				Managed: set("203.0.113.0/24"),
			},
			want:        []string{"B +203.0.113.0/24"},
			wantManaged: []string{"203.0.113.0/24"},
		},
		{
			name: "managed prefix without route is removed",
			in: Input{
				Peers:   []Peer{peerA("10.0.0.0/24", "203.0.113.0/24", "198.51.100.0/24")},
				Managed: set("203.0.113.0/24"),
			},
			want: []string{"A -203.0.113.0/24"},
		},
		{
			name: "managed prefix is removed from the peer it is on now",
			in: Input{
				Peers:   []Peer{peerA("10.0.0.0/24"), peerB("10.1.0.0/24", "203.0.113.0/24")},
				Managed: set("203.0.113.0/24"),
			},
			want: []string{"B -203.0.113.0/24"},
		},
		{
			name: "managed prefix that is gone already is forgotten",
			in: Input{
				Peers:   []Peer{peerA("10.0.0.0/24")},
				Managed: set("203.0.113.0/24"),
			},
		},
		{
			name: "startup: present prefix with a route on another peer is moved",
			in: Input{
				Routes:  []Route{route("192.0.2.0/24", "10.1.0.1", 0)},
				Peers:   []Peer{peerA("10.0.0.0/24", "192.0.2.0/24"), peerB("10.1.0.0/24")},
				Startup: true,
			},
			want:        []string{"B +192.0.2.0/24"},
			wantManaged: []string{"192.0.2.0/24"},
		},
		{
			name: "startup: prefix without a route is manual and stays",
			in: Input{
				Peers:   []Peer{peerA("10.0.0.0/24", "192.0.2.0/24")},
				Startup: true,
			},
		},
		{
			name: "after startup: present prefix with a route on the same peer stays manual",
			in: Input{
				Routes: []Route{route("192.0.2.0/24", "10.0.0.1", 0)},
				Peers:  []Peer{peerA("10.0.0.0/24", "192.0.2.0/24")},
			},
		},
		{
			name: "after startup: present prefix with a route on another peer is a conflict and stays",
			in: Input{
				Routes: []Route{route("192.0.2.0/24", "10.1.0.1", 0)},
				Peers:  []Peer{peerA("10.0.0.0/24", "192.0.2.0/24"), peerB("10.1.0.0/24")},
			},
			wantConflicts: []string{"192.0.2.0/24 on A, route to B"},
		},
		{
			name: "after startup: managed prefix on another peer than its route is moved",
			in: Input{
				Routes:  []Route{route("192.0.2.0/24", "10.1.0.1", 0)},
				Peers:   []Peer{peerA("10.0.0.0/24", "192.0.2.0/24"), peerB("10.1.0.0/24")},
				Managed: set("192.0.2.0/24"),
			},
			want:        []string{"B +192.0.2.0/24"},
			wantManaged: []string{"192.0.2.0/24"},
		},
		{
			name: "after startup: manual gateway prefix with a learned route on another peer resolves gateways",
			in: Input{
				Routes: []Route{
					route("203.0.113.0/24", "10.250.0.2", 0),
					route("10.250.0.2/32", "10.1.0.1", 0),
				},
				Peers: []Peer{peerA("10.0.0.0/24", "10.250.0.2/32"), peerB("10.1.0.0/24")},
			},
			want:          []string{"A +203.0.113.0/24"},
			wantManaged:   []string{"203.0.113.0/24"},
			wantConflicts: []string{"10.250.0.2/32 on A, route to B"},
		},
		{
			name: "startup: prefix of a learned route does not resolve gateways, protecting it does",
			in: Input{
				Routes: []Route{
					route("203.0.113.0/24", "10.250.0.2", 0),
					route("198.51.100.0/24", "10.251.0.2", 0),
					route("10.250.0.2/32", "10.1.0.1", 0),
					route("10.251.0.2/32", "10.1.0.1", 0),
				},
				Peers:     []Peer{peerA("10.0.0.0/24", "10.250.0.2/32", "10.251.0.2/32"), peerB("10.1.0.0/24")},
				Protected: set("10.250.0.2/32"),
				Startup:   true,
			},
			want:           []string{"A +203.0.113.0/24", "B +10.251.0.2/32"},
			wantManaged:    []string{"10.251.0.2/32", "203.0.113.0/24"},
			wantUnresolved: 1,
		},
		{
			name: "protected prefixes are not added, moved or removed",
			in: Input{
				Routes: []Route{
					route("192.0.2.0/24", "10.1.0.1", 0),
					route("198.51.100.0/24", "10.1.0.1", 0),
				},
				Peers:     []Peer{peerA("10.0.0.0/24", "192.0.2.0/24"), peerB("10.1.0.0/24")},
				Managed:   set("203.0.113.0/24"),
				Protected: set("192.0.2.0/24", "203.0.113.0/24"),
			},
			want:        []string{"B +198.51.100.0/24"},
			wantManaged: []string{"198.51.100.0/24"},
		},
		{
			name: "route derived prefixes do not resolve gateways",
			in: Input{
				Routes: []Route{
					route("10.0.0.0/8", "192.168.1.1", 0),
					route("172.16.0.0/12", "10.5.5.5", 0),
				},
				Peers:   []Peer{peerA("192.168.1.0/24", "10.0.0.0/8")},
				Managed: set("10.0.0.0/8"),
			},
			wantManaged:    []string{"10.0.0.0/8"},
			wantUnresolved: 1,
		},
		{
			name: "startup: prefix present that a route backs does not resolve gateways either",
			in: Input{
				Routes: []Route{
					route("10.0.0.0/8", "192.168.1.1", 0),
					route("172.16.0.0/12", "10.5.5.5", 0),
				},
				Peers:   []Peer{peerA("192.168.1.0/24", "10.0.0.0/8")},
				Startup: true,
			},
			wantManaged:    []string{"10.0.0.0/8"},
			wantUnresolved: 1,
		},
		{
			name: "unresolved route removes its managed prefix",
			in: Input{
				Routes:  []Route{route("203.0.113.0/24", "10.9.9.9", 0)},
				Peers:   []Peer{peerA("10.0.0.0/24", "203.0.113.0/24")},
				Managed: set("203.0.113.0/24"),
			},
			want:           []string{"A -203.0.113.0/24"},
			wantUnresolved: 1,
		},
		{
			name: "changes are ordered by peer and prefix",
			in: Input{
				Routes: []Route{
					route("203.0.113.0/24", "10.1.0.1", 0),
					route("198.51.100.0/24", "10.1.0.1", 0),
					route("192.0.2.0/24", "10.0.0.1", 0),
				},
				Peers: []Peer{peerB("10.1.0.0/24"), peerA("10.0.0.0/24")},
			},
			want:        []string{"A +192.0.2.0/24", "B +198.51.100.0/24 +203.0.113.0/24"},
			wantManaged: []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := Compute(tt.in)
			if got := render(plan); !slices.Equal(got, tt.want) {
				t.Errorf("changes:\n got %q\nwant %q", got, tt.want)
			}
			if got := sorted(plan.Managed); !slices.Equal(got, tt.wantManaged) {
				t.Errorf("managed:\n got %q\nwant %q", got, tt.wantManaged)
			}
			if got := len(plan.Unresolved); got != tt.wantUnresolved {
				t.Errorf("unresolved = %d, want %d", got, tt.wantUnresolved)
			}
			var conflicts []string
			for _, c := range plan.Conflicts {
				conflicts = append(conflicts, fmt.Sprintf("%s on %s, route to %s", c.Dst, peerName(c.Present), peerName(c.Wanted)))
			}
			if !slices.Equal(conflicts, tt.wantConflicts) {
				t.Errorf("conflicts:\n got %q\nwant %q", conflicts, tt.wantConflicts)
			}
		})
	}
}

// without returns the list without the removed prefixes.
func without(list, removed []netip.Prefix) []netip.Prefix {
	return slices.DeleteFunc(slices.Clone(list), func(p netip.Prefix) bool { return slices.Contains(removed, p) })
}

// apply the plan like WireGuard does and carry over the managed set.
func apply(in *Input, plan Plan) {
	for _, c := range plan.Changes {
		for i := range in.Peers {
			if in.Peers[i].Key == c.Peer {
				in.Peers[i].AllowedIPs = append(without(in.Peers[i].AllowedIPs, c.Remove), c.Add...)
			} else {
				in.Peers[i].AllowedIPs = without(in.Peers[i].AllowedIPs, c.Add) // kernel moves
			}
		}
	}
	in.Managed = plan.Managed
}

// Applying a plan and computing again must not change anything.
func TestComputeConverges(t *testing.T) {
	for _, startup := range []bool{true, false} {
		in := Input{
			Routes: []Route{
				route("203.0.113.0/24", "10.1.0.1", 100),
				route("203.0.113.0/24", "10.0.0.1", 200),
				route("0.0.0.0/0", "10.0.0.1", 0),
			},
			Peers: []Peer{
				peerA("10.0.0.0/24", "203.0.113.0/24", "192.0.2.0/24"),
				peerB("10.1.0.0/24"),
			},
			Managed: set("203.0.113.0/24", "198.51.100.0/24"),
			Startup: startup,
		}
		apply(&in, Compute(in))

		in.Startup = false
		if again := Compute(in); len(again.Changes) != 0 {
			t.Errorf("startup=%v: second pass has changes: %q", startup, render(again))
		}
	}
}

// A manual prefix that is a gateway must survive a routing daemon learning it on another peer.
func TestComputeKeepsManualGateway(t *testing.T) {
	in := Input{
		Peers: []Peer{peerA("10.0.0.0/24", "10.250.0.2/32"), peerB("10.1.0.0/24")},
	}
	step := func(name string, want ...string) {
		t.Helper()
		plan := Compute(in)
		if got := render(plan); !slices.Equal(got, want) {
			t.Errorf("%s:\n got %q\nwant %q", name, got, want)
		}
		apply(&in, plan)
	}

	in.Routes = []Route{route("203.0.113.0/24", "10.250.0.2", 0)}
	step("route via the manual prefix", "A +203.0.113.0/24")

	in.Routes = append(in.Routes, route("10.250.0.2/32", "10.1.0.1", 0))
	step("gateway learned on B")
	step("again")

	in.Routes = in.Routes[:1]
	step("learned route gone")

	in.Routes = nil
	step("route gone", "A -203.0.113.0/24")

	if want := pfxs("10.0.0.0/24", "10.250.0.2/32"); !slices.Equal(in.Peers[0].AllowedIPs, want) {
		t.Errorf("A = %v, want %v", in.Peers[0].AllowedIPs, want)
	}
}

// What an earlier run left behind is taken over once, at startup.
func TestComputeStartupTakesOverLeftovers(t *testing.T) {
	in := Input{
		Routes:  []Route{route("203.0.113.0/24", "10.0.0.1", 0)},
		Peers:   []Peer{peerA("10.0.0.0/24", "203.0.113.0/24", "192.0.2.0/24"), peerB("10.1.0.0/24")},
		Startup: true,
	}
	apply(&in, Compute(in))
	in.Startup = false

	in.Routes = nil
	plan := Compute(in)
	if got, want := render(plan), []string{"A -203.0.113.0/24"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
