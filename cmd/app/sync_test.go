package main

import (
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"git.ruekov.eu/ruakij/routingtabletowg/lib/reconcile"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestWarnings(t *testing.T) {
	plan := reconcile.Plan{
		Unresolved: []reconcile.Route{
			{Dst: netip.MustParsePrefix("203.0.113.0/24"), Gw: netip.MustParseAddr("10.9.9.9")},
			{Dst: netip.MustParsePrefix("198.51.100.0/24"), Gw: netip.MustParseAddr("10.9.9.9")},
		},
		Conflicts: []reconcile.Conflict{
			{Dst: netip.MustParsePrefix("192.0.2.0/24"), Present: wgtypes.Key{1}, Wanted: wgtypes.Key{2}},
		},
	}

	got := warnings(plan)
	if len(got) != 2 || got[0] != "No peer found containing gw-IP '10.9.9.9', ignoring 2 route(s)" {
		t.Errorf("got %q", got)
	}
	if want := warnings(plan); !slices.Equal(got, want) {
		t.Errorf("order is not stable: %q vs %q", got, want)
	}
}

func TestFreshReportsChangesOnly(t *testing.T) {
	s := &syncer{}
	steps := []struct {
		in, want []string
	}{
		{[]string{"a", "b"}, []string{"a", "b"}},
		{[]string{"a", "b"}, nil},
		{[]string{"b", "c"}, []string{"c"}},
		{nil, nil},
		{[]string{"a"}, []string{"a"}}, // gone and back is a change
	}
	for i, step := range steps {
		if got := s.fresh(step.in); !slices.Equal(got, step.want) {
			t.Errorf("step %d: got %q, want %q", i, got, step.want)
		}
	}
}

func TestObserveStartsOverForAnotherInterface(t *testing.T) {
	s := newSyncer("wg0", FilterOptions{}, nil, nil)
	s.observe(7)
	s.managed[netip.MustParsePrefix("203.0.113.0/24")] = struct{}{}
	s.started = true
	s.warned = map[string]struct{}{"x": {}}

	s.observe(7)
	if len(s.managed) != 1 || !s.started || len(s.warned) != 1 {
		t.Errorf("same interface must keep its state: %v %v %v", s.managed, s.started, s.warned)
	}

	// Gone for a moment, back as the same one
	s.ifindex = 0
	s.observe(7)
	if len(s.managed) != 1 || !s.started {
		t.Errorf("same interface must keep its state: %v %v", s.managed, s.started)
	}

	s.observe(9)
	if len(s.managed) != 0 || s.started || s.warned != nil || s.ifindex != 9 || s.known != 9 {
		t.Errorf("recreated interface must start over: %v %v %v %d %d", s.managed, s.started, s.warned, s.ifindex, s.known)
	}
}

var (
	keyA = wgtypes.Key{1}
	keyB = wgtypes.Key{2}
)

func ipNet(t *testing.T, cidr string) net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return *n
}

func ipNets(t *testing.T, list ...string) []net.IPNet {
	t.Helper()
	var out []net.IPNet
	for _, s := range list {
		out = append(out, ipNet(t, s))
	}
	return out
}

// fakeWg records the writes. Writes touching a peer in fail return an error.
type fakeWg struct {
	device  *wgtypes.Device
	configs []wgtypes.Config
	fail    map[wgtypes.Key]bool
}

func (f *fakeWg) Device(string) (*wgtypes.Device, error) { return f.device, nil }

func (f *fakeWg) ConfigureDevice(_ string, cfg wgtypes.Config) error {
	f.configs = append(f.configs, cfg)
	for _, peer := range cfg.Peers {
		if f.fail[peer.PublicKey] {
			return errors.New("rejected")
		}
	}
	return nil
}

func TestWritable(t *testing.T) {
	tests := []struct {
		cidr string
		want bool
	}{
		{"10.200.0.0/24", true},
		{"0.0.0.0/0", true},
		{"fd00:200::/64", true},
		{"::/0", true},
		{"::ffff:0:0/80", true}, // the host bits are cleared, nothing of the mapped form is left
		{"::ffff:10.0.0.0/104", false},
		{"::ffff:0:0/96", false},
		{"::ffff:10.1.2.3/128", false},
	}
	for _, tt := range tests {
		if got := writable(ipNet(t, tt.cidr)); got != tt.want {
			t.Errorf("writable(%s) = %v, want %v", tt.cidr, got, tt.want)
		}
	}
	if !writable(net.IPNet{IP: net.IP{10, 0, 0, 0}, Mask: net.CIDRMask(8, 32)}) {
		t.Error("4 byte IPv4 must be writable")
	}
}

func TestRemoveLeavesPeerWithUnwritableEntryAlone(t *testing.T) {
	fake := &fakeWg{device: &wgtypes.Device{Peers: []wgtypes.Peer{
		{PublicKey: keyA, AllowedIPs: ipNets(t, "10.200.0.0/24", "::ffff:10.0.0.0/104", "fd00:200::/64", "203.0.113.0/24")},
		{PublicKey: keyB, AllowedIPs: ipNets(t, "10.201.0.0/24", "198.51.100.0/24")},
	}}}
	s := newSyncer("wg0", FilterOptions{}, nil, fake)

	result, err := s.remove([]reconcile.Change{
		{Peer: keyA, Remove: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}},
		{Peer: keyB, Remove: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(fake.configs) != 1 || len(fake.configs[0].Peers) != 1 || fake.configs[0].Peers[0].PublicKey != keyB {
		t.Fatalf("only peer B may be written: %+v", fake.configs)
	}
	peer := fake.configs[0].Peers[0]
	if !peer.ReplaceAllowedIPs || !peer.UpdateOnly || len(peer.AllowedIPs) != 1 || peer.AllowedIPs[0].String() != "10.201.0.0/24" {
		t.Errorf("wrong write for peer B: %+v", peer)
	}

	if _, kept := result.kept[netip.MustParsePrefix("203.0.113.0/24")]; !kept || len(result.kept) != 1 {
		t.Errorf("kept = %v", result.kept)
	}
	if len(result.warnings) != 1 || !strings.Contains(result.warnings[0], "::ffff:10.0.0.0/104") ||
		!strings.Contains(result.warnings[0], "203.0.113.0/24") {
		t.Errorf("warnings = %q", result.warnings)
	}
}

func TestRemoveWritesEachPeerOnItsOwn(t *testing.T) {
	fake := &fakeWg{
		device: &wgtypes.Device{Peers: []wgtypes.Peer{
			{PublicKey: keyA, AllowedIPs: ipNets(t, "10.200.0.0/24", "203.0.113.0/24")},
			{PublicKey: keyB, AllowedIPs: ipNets(t, "10.201.0.0/24", "198.51.100.0/24")},
		}},
		fail: map[wgtypes.Key]bool{keyA: true},
	}
	s := newSyncer("wg0", FilterOptions{}, nil, fake)

	_, err := s.remove([]reconcile.Change{
		{Peer: keyA, Remove: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}},
		{Peer: keyB, Remove: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}},
	})
	if err == nil || !strings.Contains(err.Error(), keyA.String()) {
		t.Errorf("error = %v, want one naming peer A", err)
	}
	if len(fake.configs) != 2 {
		t.Fatalf("want a write per peer, got %d", len(fake.configs))
	}
	for _, cfg := range fake.configs {
		if len(cfg.Peers) != 1 {
			t.Errorf("a write must touch one peer: %+v", cfg)
		}
	}
}

func TestCommitStartsEvenIfApplyFails(t *testing.T) {
	fake := &fakeWg{fail: map[wgtypes.Key]bool{keyA: true}}
	s := newSyncer("wg0", FilterOptions{}, nil, fake)
	prefix := netip.MustParsePrefix("203.0.113.0/24")

	err := s.commit(reconcile.Plan{
		Changes: []reconcile.Change{{Peer: keyA, Add: []netip.Prefix{prefix}}},
		Managed: reconcile.Set{prefix: {}},
	}, nil)
	if err == nil {
		t.Fatal("want the error of the write")
	}
	if !s.started {
		t.Error("startup must end with the first plan, or it lasts over the retries")
	}
	if _, ok := s.managed[prefix]; !ok {
		t.Errorf("what the plan took over must stay managed: %v", s.managed)
	}
}

func TestCommitKeepsPrefixManagedThatCannotBeRemoved(t *testing.T) {
	fake := &fakeWg{device: &wgtypes.Device{Peers: []wgtypes.Peer{
		{PublicKey: keyA, AllowedIPs: ipNets(t, "::ffff:10.0.0.0/104", "203.0.113.0/24")},
	}}}
	s := newSyncer("wg0", FilterOptions{}, nil, fake)
	prefix := netip.MustParsePrefix("203.0.113.0/24")
	s.managed[prefix] = struct{}{}

	err := s.commit(reconcile.Plan{
		Changes: []reconcile.Change{{Peer: keyA, Remove: []netip.Prefix{prefix}}},
		Managed: make(reconcile.Set),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.managed[prefix]; !ok {
		t.Errorf("the removal has to be tried again later: %v", s.managed)
	}
	if len(fake.configs) != 0 {
		t.Errorf("nothing may be written: %+v", fake.configs)
	}
	if len(s.warned) != 1 {
		t.Errorf("want one warning, got %v", s.warned)
	}
}

func TestSkippedWarnings(t *testing.T) {
	mapped := netip.MustParsePrefix("::ffff:10.0.0.0/104")
	other := netip.MustParsePrefix("::ffff:0:0/96")

	got := skippedWarnings([]netip.Prefix{mapped, other, mapped})
	if len(got) != 2 || !strings.Contains(got[0], "::ffff:0.0.0.0/96") || !strings.Contains(got[1], "::ffff:10.0.0.0/104") {
		t.Errorf("want one message per prefix, got %q", got)
	}
	if got := skippedWarnings(nil); len(got) != 0 {
		t.Errorf("got %q", got)
	}
}

// What a reconcile leaves out is reported when it appears, not at every reconcile.
func TestCommitReportsNotesOnce(t *testing.T) {
	s := newSyncer("wg0", FilterOptions{}, nil, &fakeWg{})
	notes := skippedWarnings([]netip.Prefix{netip.MustParsePrefix("::ffff:10.0.0.0/104")})

	for range 2 {
		if err := s.commit(reconcile.Plan{Managed: make(reconcile.Set)}, notes); err != nil {
			t.Fatal(err)
		}
		if _, remembered := s.warned[notes[0]]; !remembered {
			t.Fatalf("note was not remembered: %v", s.warned)
		}
	}
	if fresh := s.fresh(notes); len(fresh) != 0 {
		t.Errorf("a repeated note must not be logged again: %q", fresh)
	}
}
