package main

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"

	"git.ruekov.eu/ruakij/routingtabletowg/lib/reconcile"

	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// wireguard is the part of wgctrl.Client the syncer uses, so tests can replace it.
type wireguard interface {
	Device(name string) (*wgtypes.Device, error)
	ConfigureDevice(name string, cfg wgtypes.Config) error
}

// syncer makes the AllowedIPs of the peers follow the routes of the interface.
//
// Routes only tell when to look. Every reconcile lists the routes and the
// peers anew, so lost or reordered route updates cannot leave a stale state.
// It is not safe for concurrent use, the run loop is the only caller.
type syncer struct {
	iface     string
	filter    FilterOptions
	protected reconcile.Set
	wg        wireguard

	// subscribe is replaced in tests
	subscribe func() (<-chan netlink.RouteUpdate, chan struct{}, error)

	// ifindex of the interface at the last reconcile, 0 if it did not exist
	ifindex int

	// known is the last index the interface had. Another one means it was recreated.
	known int

	// managed are the prefixes added or adopted because of a route. Others are
	// never removed. It is kept in memory only.
	managed reconcile.Set

	// started is set once a reconcile of the interface got as far as planning, also
	// if writing failed. Until then the prefixes present already are taken over if a route backs them.
	started bool

	// warned are the warnings logged for the current state, so they are not repeated
	warned map[string]struct{}
}

func newSyncer(iface string, filter FilterOptions, protected reconcile.Set, client wireguard) *syncer {
	return &syncer{
		iface:     iface,
		filter:    filter,
		protected: protected,
		wg:        client,
		subscribe: subscribeRoutes,
		managed:   make(reconcile.Set),
	}
}

func (s *syncer) reconcile() error {
	// The interface can be recreated, so it is resolved again every time
	link, err := netlink.LinkByName(s.iface)
	if err != nil {
		s.ifindex = 0
		return fmt.Errorf("couldn't get interface '%s': %w", s.iface, err)
	}
	s.observe(link.Attrs().Index)

	filter, mask := s.filter.routeFilter(s.ifindex)
	routeList, err := netlink.RouteListFiltered(netlink.FAMILY_ALL, filter, mask)
	if err != nil {
		return fmt.Errorf("couldn't get route-entries: %w", err)
	}

	device, err := s.wg.Device(s.iface)
	if err != nil {
		return fmt.Errorf("couldn't get wg-interface '%s': %w", s.iface, err)
	}

	routes, skipped := toRoutes(routeList)
	plan := reconcile.Compute(reconcile.Input{
		Routes:    routes,
		Peers:     toPeers(device),
		Managed:   s.managed,
		Protected: s.protected,
		Startup:   !s.started,
	})
	return s.commit(plan, skippedWarnings(skipped))
}

// commit applies the plan and takes over the resulting state. The notes are
// warnings about what was left out before the plan.
func (s *syncer) commit(plan reconcile.Plan, notes []string) error {
	// From here on prefixes that are present are not taken over anymore, also when
	// applying fails: the managed set below covers what the plan took over.
	s.started = true

	result, err := s.apply(plan)
	s.warn(slices.Concat(notes, warnings(plan), result.warnings))
	if err != nil {
		// It is unknown how far the plan got, so keep what may belong to us
		maps.Copy(s.managed, plan.Managed)
		maps.Copy(s.managed, result.kept)
		return err
	}

	s.managed = plan.Managed
	maps.Copy(s.managed, result.kept)
	return nil
}

// observe records the index of the interface. Another index than before means the
// interface was recreated, so what was learned about the old one is dropped. The
// prefixes of the new one are not ours, except the ones a route backs, like at the start.
func (s *syncer) observe(index int) {
	s.ifindex = index
	if s.known != 0 && s.known != index {
		logger.Info.Printf("Interface '%s' was recreated, starting over", s.iface)
		s.managed = make(reconcile.Set)
		s.started = false
		s.warned = nil
	}
	s.known = index
}

// outcome of applying a plan, besides the error
type outcome struct {
	// warnings about changes that were not made
	warnings []string

	// kept are the prefixes that were to be removed and are still there. They stay managed.
	kept reconcile.Set
}

// apply writes the changes of the plan to the wg-interface.
func (s *syncer) apply(plan reconcile.Plan) (outcome, error) {
	// Adding a prefix to a peer takes it from the one having it. Only the new
	// prefixes are sent, so nothing else on the peer is touched.
	var add wgtypes.Config
	for _, change := range plan.Changes {
		if len(change.Add) == 0 {
			continue
		}
		add.Peers = append(add.Peers, wgtypes.PeerConfig{
			PublicKey:  change.Peer,
			UpdateOnly: true, // never recreate a peer removed in the meantime
			AllowedIPs: toIPNets(change.Add),
		})
	}
	if len(add.Peers) > 0 {
		if err := s.wg.ConfigureDevice(s.iface, add); err != nil {
			return outcome{}, fmt.Errorf("error adding to wg-device '%s': %w", s.iface, err)
		}
		for _, change := range plan.Changes {
			for _, prefix := range change.Add {
				logger.Info.Printf("[+] %s to peer %s", prefix, change.Peer)
			}
		}
	}

	return s.remove(plan.Changes)
}

// remove deletes prefixes from their peers. WireGuard can only replace the
// whole list, so it is read right before to keep the window for changes made
// by others small.
//
// The kernel drops the list first and then inserts the new entries one by one,
// until one is rejected. A list that cannot be written back completely would
// lose the entries from the rejected one on, so such a peer is left alone.
// Every peer is written on its own, so a failing one does not stop the others.
func (s *syncer) remove(changes []reconcile.Change) (outcome, error) {
	result := outcome{kept: make(reconcile.Set)}
	if !slices.ContainsFunc(changes, func(c reconcile.Change) bool { return len(c.Remove) > 0 }) {
		return result, nil
	}

	device, err := s.wg.Device(s.iface)
	if err != nil {
		return result, fmt.Errorf("couldn't get wg-interface '%s': %w", s.iface, err)
	}
	current := make(map[wgtypes.Key][]net.IPNet)
	for _, peer := range device.Peers {
		current[peer.PublicKey] = peer.AllowedIPs
	}

	var errs []error
	for _, change := range changes {
		allowed := current[change.Peer]
		left := withoutPrefixes(allowed, change.Remove)
		if len(left) == len(allowed) { // gone already, or the peer is
			continue
		}

		if i := slices.IndexFunc(left, func(n net.IPNet) bool { return !writable(n) }); i >= 0 {
			result.warnings = append(result.warnings, fmt.Sprintf(
				"Peer %s has the AllowedIP %s, which cannot be written back to Wireguard, so %v stay on the peer",
				change.Peer, prefixString(left[i]), change.Remove))
			for _, prefix := range change.Remove {
				result.kept[prefix] = struct{}{}
			}
			continue
		}

		err := s.wg.ConfigureDevice(s.iface, wgtypes.Config{Peers: []wgtypes.PeerConfig{{
			PublicKey:         change.Peer,
			UpdateOnly:        true,
			ReplaceAllowedIPs: true,
			AllowedIPs:        left,
		}}})
		if err != nil {
			errs = append(errs, fmt.Errorf("error removing from peer %s of wg-device '%s': %w", change.Peer, s.iface, err))
			continue
		}
		for _, prefix := range change.Remove {
			logger.Info.Printf("[-] %s from peer %s", prefix, change.Peer)
		}
	}
	return result, errors.Join(errs...)
}

// linkChanged tells if the interface is another one than at the last reconcile.
func (s *syncer) linkChanged() bool {
	link, err := netlink.LinkByName(s.iface)
	if err != nil {
		return s.ifindex != 0
	}
	return link.Attrs().Index != s.ifindex
}

// watches tells if a route update may change what the last reconcile did.
func (s *syncer) watches(route netlink.Route) bool {
	if s.ifindex != 0 && route.LinkIndex == s.ifindex {
		return true
	}

	// A route of another interface can replace one of ours without a delete
	dst, ok := routeDst(route)
	_, managed := s.managed[dst]
	return ok && managed
}

// warnings describes what a plan leaves out, in a stable order.
func warnings(plan reconcile.Plan) []string {
	counts := make(map[netip.Addr]int)
	for _, route := range plan.Unresolved {
		counts[route.Gw]++
	}

	var messages []string
	for gw, count := range counts {
		messages = append(messages, fmt.Sprintf("No peer found containing gw-IP '%s', ignoring %d route(s)", gw, count))
	}
	for _, c := range plan.Conflicts {
		messages = append(messages, fmt.Sprintf(
			"Route to %s points to peer %s, but the prefix is on peer %s and not managed by this program, leaving it",
			c.Dst, c.Wanted, c.Present))
	}
	slices.Sort(messages)
	return messages
}

// skippedWarnings describes the routes that were left out because their destination
// cannot be written to WireGuard, once per destination.
func skippedWarnings(skipped []netip.Prefix) []string {
	messages := make([]string, 0, len(skipped))
	for _, prefix := range skipped {
		messages = append(messages, fmt.Sprintf("Ignoring route to %s, WireGuard cannot take an IPv4-mapped IPv6 prefix as AllowedIP", prefix))
	}
	slices.Sort(messages)
	return slices.Compact(messages)
}

// warn logs the messages that were not part of the last call. A condition that
// lasts is logged when it appears, not at every reconcile.
func (s *syncer) warn(messages []string) {
	for _, message := range s.fresh(messages) {
		logger.Warn.Print(message)
	}
}

// fresh returns the messages that were not part of the last call and remembers the current ones.
func (s *syncer) fresh(messages []string) []string {
	var fresh []string
	current := make(map[string]struct{}, len(messages))
	for _, message := range messages {
		current[message] = struct{}{}
		if _, seen := s.warned[message]; !seen {
			fresh = append(fresh, message)
		}
	}
	s.warned = current
	return fresh
}
