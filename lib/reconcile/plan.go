// Package reconcile computes the AllowedIPs changes that make WireGuard peers
// follow a set of routes. It does no I/O.
package reconcile

import (
	"bytes"
	"cmp"
	"net/netip"
	"slices"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Set of prefixes.
type Set = map[netip.Prefix]struct{}

// Peer with its current AllowedIPs, which must be canonical (see PrefixFromIPNet).
type Peer struct {
	Key        wgtypes.Key
	AllowedIPs []netip.Prefix
}

// Route to a destination via a gateway, with canonical prefix and address.
type Route struct {
	Dst    netip.Prefix
	Gw     netip.Addr
	Metric int
	Table  int
}

type Input struct {
	Routes []Route
	Peers  []Peer

	// Managed are the prefixes the program added or adopted because of a route.
	// Only these are removed once no route backs them anymore.
	Managed Set

	// Protected prefixes are never added, moved or removed.
	Protected Set

	// Startup is set for the first reconcile after the start or after the interface
	// was recreated. Only then a prefix that is present already and not managed is
	// adopted or moved when a route backs it, as it can be left from an earlier run.
	// Later it is manual, whatever the routes say.
	Startup bool
}

// Change to the AllowedIPs of one peer.
type Change struct {
	Peer wgtypes.Key

	// Add prefixes. When a prefix is currently on another peer, adding it moves it.
	Add    []netip.Prefix
	Remove []netip.Prefix
}

// Conflict is a route to a prefix that is on another peer and not managed.
// It is left as it is.
type Conflict struct {
	Dst     netip.Prefix
	Present wgtypes.Key // peer having the prefix
	Wanted  wgtypes.Key // peer the route points to
}

type Plan struct {
	Changes []Change

	// Unresolved are the routes whose gateway belongs to no peer.
	Unresolved []Route

	// Conflicts are ordered by prefix.
	Conflicts []Conflict

	// Managed is the new managed set once all changes are applied.
	// It is the set of prefixes backed by a route that the program added or adopted.
	Managed Set
}

// Compute the changes that bring the peers in line with the routes.
//
// Every destination is assigned to the peer with the longest AllowedIPs prefix
// containing the gateway. Of several routes to a destination the lowest metric wins.
// A managed prefix on the wrong peer is moved, a managed prefix without a route is removed.
// Prefixes the program does not manage are left alone, except at startup (see Input.Startup).
func Compute(in Input) Plan {
	present := make(map[netip.Prefix]wgtypes.Key)
	for _, peer := range in.Peers {
		for _, prefix := range peer.AllowedIPs {
			present[prefix] = peer.Key
		}
	}

	desired, unresolved := desiredState(in)

	changes := make(map[wgtypes.Key]*Change)
	change := func(key wgtypes.Key) *Change {
		if changes[key] == nil {
			changes[key] = &Change{Peer: key}
		}
		return changes[key]
	}

	plan := Plan{Unresolved: unresolved}
	managed := make(Set, len(desired))
	for dst, key := range desired {
		current, isPresent := present[dst]
		_, ours := in.Managed[dst]
		if isPresent && !ours && !in.Startup {
			// Somebody else set it up, so the route has no say about it
			if current != key {
				plan.Conflicts = append(plan.Conflicts, Conflict{Dst: dst, Present: current, Wanted: key})
			}
			continue
		}

		managed[dst] = struct{}{}
		if !isPresent || current != key {
			c := change(key)
			c.Add = append(c.Add, dst)
		}
	}

	for dst := range in.Managed {
		_, wanted := desired[dst]
		_, protected := in.Protected[dst]
		if wanted || protected {
			continue
		}
		if key, ok := present[dst]; ok {
			c := change(key)
			c.Remove = append(c.Remove, dst)
		}
	}

	plan.Managed = managed
	slices.SortFunc(plan.Conflicts, func(a, b Conflict) int { return comparePrefix(a.Dst, b.Dst) })
	for _, c := range changes {
		slices.SortFunc(c.Add, comparePrefix)
		slices.SortFunc(c.Remove, comparePrefix)
		plan.Changes = append(plan.Changes, *c)
	}
	slices.SortFunc(plan.Changes, func(a, b Change) int { return bytes.Compare(a.Peer[:], b.Peer[:]) })
	return plan
}

type entry struct {
	prefix netip.Prefix
	key    wgtypes.Key
}

// desiredState assigns the destination of every usable route to a peer.
func desiredState(in Input) (map[netip.Prefix]wgtypes.Key, []Route) {
	// Destinations that may be adopted or moved at startup
	dsts := make(Set, len(in.Routes))
	if in.Startup {
		for _, route := range in.Routes {
			if _, protected := in.Protected[route.Dst]; !protected {
				dsts[route.Dst] = struct{}{}
			}
		}
	}

	// Gateways are matched against the prefixes set up by hand only. Prefixes
	// derived from routes would make the result depend on what was applied before.
	// At startup the managed set is not known, so the prefixes a route backs are
	// left out, as they are about to be taken over.
	var static []entry
	for _, peer := range in.Peers {
		for _, prefix := range peer.AllowedIPs {
			_, managed := in.Managed[prefix]
			_, routed := dsts[prefix]
			if !managed && !routed {
				static = append(static, entry{prefix, peer.Key})
			}
		}
	}

	type owner struct {
		key wgtypes.Key
		ok  bool
	}
	owners := make(map[netip.Addr]owner)
	peerFor := func(gw netip.Addr) (wgtypes.Key, bool) {
		if o, seen := owners[gw]; seen {
			return o.key, o.ok
		}
		key, ok := longestMatch(static, gw)
		owners[gw] = owner{key, ok}
		return key, ok
	}

	best := make(map[netip.Prefix]Route)
	desired := make(map[netip.Prefix]wgtypes.Key)
	var unresolved []Route
	for _, route := range in.Routes {
		if !route.Dst.IsValid() || !route.Gw.IsValid() {
			continue
		}
		if _, protected := in.Protected[route.Dst]; protected {
			continue
		}

		key, ok := peerFor(route.Gw)
		if !ok {
			unresolved = append(unresolved, route)
			continue
		}

		if current, ok := best[route.Dst]; !ok || compareRoutes(route, current) < 0 {
			best[route.Dst] = route
			desired[route.Dst] = key
		}
	}
	return desired, unresolved
}

// longestMatch returns the peer owning the longest prefix that contains the address,
// like WireGuard picks the peer for a destination.
func longestMatch(entries []entry, addr netip.Addr) (wgtypes.Key, bool) {
	var (
		key  wgtypes.Key
		bits = -1
	)
	for _, e := range entries {
		if e.prefix.Bits() > bits && e.prefix.Contains(addr) {
			key, bits = e.key, e.prefix.Bits()
		}
	}
	return key, bits >= 0
}

// tableMain is the main routing table.
const tableMain = 254

// compareRoutes orders routes by preference: metric, then table (main first, then the
// lower number) and gateway as stable tie-breakers.
func compareRoutes(a, b Route) int {
	return cmp.Or(
		cmp.Compare(a.Metric, b.Metric),
		cmp.Compare(tableRank(a.Table), tableRank(b.Table)),
		a.Gw.Compare(b.Gw),
	)
}

func tableRank(table int) int {
	if table == tableMain {
		return -1
	}
	return table
}

func comparePrefix(a, b netip.Prefix) int {
	return cmp.Or(
		a.Addr().Compare(b.Addr()),
		cmp.Compare(a.Bits(), b.Bits()),
	)
}
