//go:build linux && integration

// Needs root, the wireguard kernel module, the wg tool (wireguard-tools-wg on Alpine) and ip
// (BusyBox on Alpine is enough), run it in a privileged container:
//
//	go test -tags integration ./cmd/app
package main

import (
	"net"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.ruekov.eu/ruakij/routingtabletowg/lib/reconcile"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const (
	wgName    = "wgtest0"
	otherName = "dmytest0"
)

type env struct {
	t      *testing.T
	client *wgctrl.Client
	priv   wgtypes.Key
	keyA   wgtypes.Key
	keyB   wgtypes.Key
}

func mustKey(t *testing.T) wgtypes.Key {
	t.Helper()
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func cidr(s string) net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return *n
}

func cidrs(list ...string) []net.IPNet {
	var out []net.IPNet
	for _, s := range list {
		out = append(out, cidr(s))
	}
	return out
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// createWg creates the interface with two peers. A has 10.200.0.0/24 and fd00:200::/64 for its
// gateways, B has 10.201.0.0/24 and 10.99.0.0/24. The extra prefixes are set before the syncer starts.
func (e *env) createWg(extraA ...string) netlink.Link {
	t := e.t
	t.Helper()

	check(t, netlink.LinkAdd(&netlink.Wireguard{LinkAttrs: netlink.LinkAttrs{Name: wgName}}))
	link, err := netlink.LinkByName(wgName)
	check(t, err)

	check(t, e.client.ConfigureDevice(wgName, wgtypes.Config{
		PrivateKey:   &e.priv,
		ReplacePeers: true,
		Peers: []wgtypes.PeerConfig{
			{PublicKey: e.keyA, AllowedIPs: cidrs(append([]string{"10.200.0.0/24", "fd00:200::/64"}, extraA...)...)},
			{PublicKey: e.keyB, AllowedIPs: cidrs("10.201.0.0/24", "10.99.0.0/24")},
		},
	}))

	for _, addr := range []string{"10.200.0.254/24", "10.201.0.254/24", "fd00:200::254/64"} {
		a, err := netlink.ParseAddr(addr)
		check(t, err)
		a.Flags = unix.IFA_F_NODAD
		check(t, netlink.AddrAdd(link, a))
	}
	check(t, netlink.LinkSetUp(link))
	return link
}

func (e *env) route(link netlink.Link, dst, gw string, metric int) *netlink.Route {
	r := &netlink.Route{LinkIndex: link.Attrs().Index, Gw: net.ParseIP(gw), Priority: metric}
	if dst != "" {
		n := cidr(dst)
		r.Dst = &n
	}
	return r
}

// allowed returns the AllowedIPs of the peers as sorted strings.
func (e *env) allowed() map[wgtypes.Key][]string {
	device, err := e.client.Device(wgName)
	if err != nil {
		return nil
	}
	out := make(map[wgtypes.Key][]string)
	for _, peer := range toPeers(device) {
		var list []string
		for _, p := range peer.AllowedIPs {
			list = append(list, p.String())
		}
		slices.Sort(list)
		out[peer.Key] = list
	}
	return out
}

func sortedCopy(list []string) []string {
	out := slices.Clone(list)
	slices.Sort(out)
	return out
}

// expect waits until the peers have exactly these AllowedIPs.
func (e *env) expect(step string, a, b []string) {
	e.t.Helper()
	want := map[wgtypes.Key][]string{e.keyA: sortedCopy(a), e.keyB: sortedCopy(b)}

	var got map[wgtypes.Key][]string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got = e.allowed()
		if slices.Equal(got[e.keyA], want[e.keyA]) && slices.Equal(got[e.keyB], want[e.keyB]) {
			e.t.Logf("%s: A=%v B=%v", step, got[e.keyA], got[e.keyB])
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("%s:\n got A=%v B=%v\nwant A=%v B=%v", step, got[e.keyA], got[e.keyB], want[e.keyA], want[e.keyB])
}

// settle gives the syncer time to react to something that must not change anything.
func settle() { time.Sleep(4 * debounce) }

func TestSyncIntegration(t *testing.T) {
	client, err := wgctrl.New()
	check(t, err)
	t.Cleanup(func() { client.Close() })

	e := &env{t: t, client: client, priv: mustKey(t), keyA: mustKey(t).PublicKey(), keyB: mustKey(t).PublicKey()}

	// Leftovers of a previous run
	for _, name := range []string{wgName, otherName} {
		if link, err := netlink.LinkByName(name); err == nil {
			check(t, netlink.LinkDel(link))
		}
	}
	t.Cleanup(func() {
		for _, name := range []string{wgName, otherName} {
			if link, err := netlink.LinkByName(name); err == nil {
				if err := netlink.LinkDel(link); err != nil {
					t.Log(err)
				}
			}
		}
	})

	// A has a manual prefix, and one a route backs from before the start
	link := e.createWg("192.0.2.0/24", "198.51.100.0/24")
	check(t, netlink.RouteAdd(e.route(link, "198.51.100.0/24", "10.200.0.1", 0)))

	// An interface of another kind with a route whose gateway is in a prefix of B
	check(t, netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: otherName}}))
	other, err := netlink.LinkByName(otherName)
	check(t, err)
	otherAddr, err := netlink.ParseAddr("10.99.0.1/24")
	check(t, err)
	check(t, netlink.AddrAdd(other, otherAddr))
	check(t, netlink.LinkSetUp(other))

	// The subscription can be cut to see that it is restored
	var (
		subscriptions atomic.Int32
		cut           = make(chan struct{}, 1)
	)
	s := newSyncer(wgName, FilterOptions{Table: -1, Protocol: -1}, make(reconcile.Set), client)
	s.subscribe = func() (<-chan netlink.RouteUpdate, chan struct{}, error) {
		updates, done, err := subscribeRoutes()
		if err != nil {
			return nil, nil, err
		}
		subscriptions.Add(1)

		out := make(chan netlink.RouteUpdate)
		go func() {
			defer close(out)
			for {
				select {
				case u := <-updates:
					out <- u
				case <-cut:
					return
				case <-done:
					return
				}
			}
		}()
		return out, done, nil
	}

	// t.Context() ends before the cleanup functions are called
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		s.run(t.Context(), 0)
	}()
	t.Cleanup(func() {
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("run did not stop")
		}
	})

	manual := "192.0.2.0/24"
	base := []string{"10.200.0.0/24", "fd00:200::/64", manual}
	baseB := []string{"10.201.0.0/24", "10.99.0.0/24"}

	t.Run("start leaves everything as it is", func(t *testing.T) {
		e.t = t
		settle()
		e.expect("start", append([]string{"198.51.100.0/24"}, base...), baseB)
	})

	t.Run("add route", func(t *testing.T) {
		e.t = t
		check(t, netlink.RouteAdd(e.route(link, "203.0.113.0/24", "10.200.0.1", 0)))
		e.expect("A", append([]string{"198.51.100.0/24", "203.0.113.0/24"}, base...), baseB)

		check(t, netlink.RouteAdd(e.route(link, "198.18.0.0/24", "10.201.0.1", 0)))
		e.expect("B", append([]string{"198.51.100.0/24", "203.0.113.0/24"}, base...), append([]string{"198.18.0.0/24"}, baseB...))
	})

	t.Run("replace route changes the peer", func(t *testing.T) {
		e.t = t
		check(t, netlink.RouteReplace(e.route(link, "203.0.113.0/24", "10.201.0.1", 0)))
		e.expect("moved", append([]string{"198.51.100.0/24"}, base...), append([]string{"198.18.0.0/24", "203.0.113.0/24"}, baseB...))
	})

	t.Run("delete route of a previous run and of this one", func(t *testing.T) {
		e.t = t
		check(t, netlink.RouteDel(e.route(link, "198.51.100.0/24", "10.200.0.1", 0)))
		e.expect("adopted prefix removed, manual stays", base, append([]string{"198.18.0.0/24", "203.0.113.0/24"}, baseB...))

		check(t, netlink.RouteDel(e.route(link, "198.18.0.0/24", "10.201.0.1", 0)))
		check(t, netlink.RouteDel(e.route(link, "203.0.113.0/24", "10.201.0.1", 0)))
		e.expect("all removed", base, baseB)
	})

	t.Run("primary and backup route", func(t *testing.T) {
		e.t = t
		primary := e.route(link, "100.64.0.0/10", "10.200.0.1", 10)
		backup := e.route(link, "100.64.0.0/10", "10.201.0.1", 20)

		check(t, netlink.RouteAdd(backup))
		check(t, netlink.RouteAdd(primary))
		e.expect("primary wins", append([]string{"100.64.0.0/10"}, base...), baseB)

		check(t, netlink.RouteDel(backup))
		settle()
		e.expect("backup deleted, primary keeps it", append([]string{"100.64.0.0/10"}, base...), baseB)

		check(t, netlink.RouteAdd(backup))
		check(t, netlink.RouteDel(primary))
		e.expect("primary deleted, backup takes over", base, append([]string{"100.64.0.0/10"}, baseB...))

		check(t, netlink.RouteDel(backup))
		e.expect("both deleted", base, baseB)
	})

	t.Run("default routes", func(t *testing.T) {
		e.t = t
		// The container has default routes of its own
		v4 := e.route(link, "", "10.200.0.1", 100)
		v6 := e.route(link, "", "fd00:200::1", 100)
		check(t, netlink.RouteAdd(v4))
		check(t, netlink.RouteAdd(v6))
		e.expect("added", append([]string{"0.0.0.0/0", "::/0"}, base...), baseB)

		check(t, netlink.RouteDel(v4))
		check(t, netlink.RouteDel(v6))
		e.expect("deleted", base, baseB)
	})

	t.Run("routes of other interfaces are ignored", func(t *testing.T) {
		e.t = t
		sub := e.route(other, "172.16.0.0/16", "10.99.0.5", 0)
		def := e.route(other, "", "10.99.0.5", 200)
		check(t, netlink.RouteAdd(sub))
		check(t, netlink.RouteAdd(def))
		settle()
		e.expect("added", base, baseB)

		// An existing route of ours stays when another interface drops a route
		ours := e.route(link, "203.0.113.0/24", "10.200.0.1", 0)
		check(t, netlink.RouteAdd(ours))
		e.expect("ours added", append([]string{"203.0.113.0/24"}, base...), baseB)
		check(t, netlink.RouteDel(def))
		check(t, netlink.RouteDel(sub))
		settle()
		e.expect("other deleted", append([]string{"203.0.113.0/24"}, base...), baseB)

		// Replacing ours from another interface is no delete, but the prefix has to go
		check(t, netlink.RouteReplace(e.route(other, "203.0.113.0/24", "10.99.0.5", 0)))
		e.expect("replaced from other interface", base, baseB)
		check(t, netlink.RouteDel(e.route(other, "203.0.113.0/24", "10.99.0.5", 0)))
	})

	t.Run("lost subscription is restored", func(t *testing.T) {
		e.t = t
		before := subscriptions.Load()
		cut <- struct{}{}

		// Added while there is no subscription
		check(t, netlink.RouteAdd(e.route(link, "203.0.113.0/24", "10.200.0.1", 0)))
		e.expect("added in the gap", append([]string{"203.0.113.0/24"}, base...), baseB)
		if subscriptions.Load() <= before {
			t.Error("did not subscribe again")
		}

		check(t, netlink.RouteDel(e.route(link, "203.0.113.0/24", "10.200.0.1", 0)))
		e.expect("deleted again", base, baseB)
	})

	t.Run("manual gateway prefix is not taken over by a learned route", func(t *testing.T) {
		e.t = t
		const gateway = "10.250.0.2/32"

		// Set by hand after the start, so it is never adopted or moved
		check(t, e.client.ConfigureDevice(wgName, wgtypes.Config{Peers: []wgtypes.PeerConfig{
			{PublicKey: e.keyA, UpdateOnly: true, AllowedIPs: cidrs(gateway)},
		}}))
		withGateway := append([]string{gateway}, base...)

		via := e.route(link, "203.0.113.0/24", "10.250.0.2", 0)
		via.Flags = int(netlink.FLAG_ONLINK)
		check(t, netlink.RouteAdd(via))
		e.expect("route via the manual prefix", append([]string{"203.0.113.0/24"}, withGateway...), baseB)

		// A routing daemon learns the gateway itself on another peer
		learned := e.route(link, gateway, "10.201.0.1", 0)
		check(t, netlink.RouteAdd(learned))
		settle()
		e.expect("learned route to the gateway", append([]string{"203.0.113.0/24"}, withGateway...), baseB)

		check(t, netlink.RouteDel(learned))
		settle()
		e.expect("learned route gone", append([]string{"203.0.113.0/24"}, withGateway...), baseB)

		check(t, netlink.RouteDel(via))
		e.expect("route gone, manual stays", withGateway, baseB)

		check(t, e.client.ConfigureDevice(wgName, wgtypes.Config{Peers: []wgtypes.PeerConfig{
			{PublicKey: e.keyA, UpdateOnly: true, ReplaceAllowedIPs: true, AllowedIPs: cidrs(base...)},
		}}))
		e.expect("restored", base, baseB)
	})

	t.Run("peer with an entry wgctrl cannot write is left alone", func(t *testing.T) {
		e.t = t
		// wgctrl sends IPv4-mapped IPv6 as IPv4 with a mask too long for it, and
		// the kernel then drops this entry and all after it when replacing the list
		const mapped = "::ffff:10.0.0.0/104"
		wgSet := func(list ...string) {
			t.Helper()
			out, err := exec.Command("wg", "set", wgName, "peer", e.keyA.String(), "allowed-ips", strings.Join(list, ",")).CombinedOutput()
			if err != nil {
				t.Fatalf("wg set: %v: %s", err, out)
			}
		}
		ours := "203.0.113.0/24"

		route := e.route(link, ours, "10.200.0.1", 0)
		check(t, netlink.RouteAdd(route))
		e.expect("added", append([]string{ours}, base...), baseB)

		wgSet(append([]string{ours, mapped}, base...)...)
		stuck := append([]string{ours, mapped}, base...)
		e.expect("entry set", stuck, baseB)

		check(t, netlink.RouteDel(route))
		settle()
		e.expect("nothing lost", stuck, baseB)

		// Once the entry is gone the removal goes through, also when nothing else triggers it
		wgSet(append([]string{ours}, base...)...)
		other := e.route(link, "198.18.0.0/24", "10.200.0.1", 0)
		check(t, netlink.RouteAdd(other))
		e.expect("removed once possible", append([]string{"198.18.0.0/24"}, base...), baseB)

		check(t, netlink.RouteDel(other))
		e.expect("restored", base, baseB)
	})

	t.Run("route to an IPv4-mapped prefix is skipped", func(t *testing.T) {
		e.t = t
		// The netlink library cannot add this one, ip can
		const mapped = "::ffff:10.0.0.0/104"
		if out, err := exec.Command("ip", "-6", "route", "add", mapped, "via", "fd00:200::1", "dev", wgName).CombinedOutput(); err != nil {
			t.Fatalf("ip route add: %v: %s", err, out)
		}
		t.Cleanup(func() { _ = exec.Command("ip", "-6", "route", "del", mapped, "dev", wgName).Run() })

		// wgctrl would reject the mapped prefix and with it the ones sorted after it
		after := e.route(link, "2001:db8::/32", "fd00:200::1", 0)
		check(t, netlink.RouteAdd(after))
		e.expect("others are added", append([]string{"2001:db8::/32"}, base...), baseB)

		check(t, netlink.RouteDel(after))
		e.expect("others are removed", base, baseB)
	})

	t.Run("recreated interface", func(t *testing.T) {
		e.t = t
		check(t, netlink.RouteAdd(e.route(link, "203.0.113.0/24", "10.200.0.1", 0)))
		e.expect("added", append([]string{"203.0.113.0/24"}, base...), baseB)

		// The new interface has the prefix in the config of A. It is not one of ours anymore.
		check(t, netlink.LinkDel(link))
		link = e.createWg(manual, "203.0.113.0/24")
		check(t, netlink.RouteAdd(e.route(link, "198.18.0.0/24", "10.201.0.1", 0)))
		e.expect("new interface follows its routes", append([]string{"203.0.113.0/24"}, base...), append([]string{"198.18.0.0/24"}, baseB...))
	})
}
