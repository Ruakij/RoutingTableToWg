package reconcile

import (
	"net"
	"net/netip"
	"slices"
	"testing"
)

func TestPrefixFromIPNet(t *testing.T) {
	tests := []struct {
		name string
		in   net.IPNet
		want string // empty: invalid
	}{
		{"v4 4-byte", net.IPNet{IP: net.IP{10, 1, 2, 0}, Mask: net.CIDRMask(24, 32)}, "10.1.2.0/24"},
		{"v4 16-byte ip", net.IPNet{IP: net.ParseIP("10.1.2.0"), Mask: net.CIDRMask(24, 32)}, "10.1.2.0/24"},
		{"v4 default as synthesized", net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}, "0.0.0.0/0"},
		{"v4 default as wgctrl", net.IPNet{IP: net.IP{0, 0, 0, 0}, Mask: net.CIDRMask(0, 32)}, "0.0.0.0/0"},
		{"v4 4-byte ip with 128-bit mask", net.IPNet{IP: net.IP{10, 1, 2, 0}, Mask: net.CIDRMask(120, 128)}, "10.1.2.0/24"},
		{"v4 4-byte ip with short 128-bit mask", net.IPNet{IP: net.IP{10, 1, 2, 0}, Mask: net.CIDRMask(64, 128)}, ""},
		{"v4-mapped v6 stays v6", net.IPNet{IP: net.ParseIP("10.1.2.0"), Mask: net.CIDRMask(120, 128)}, "::ffff:10.1.2.0/120"},
		{"v4-mapped v6 default range", net.IPNet{IP: net.ParseIP("::ffff:0:0"), Mask: net.CIDRMask(96, 128)}, "::ffff:0.0.0.0/96"},
		{"v6", net.IPNet{IP: net.ParseIP("fd00::"), Mask: net.CIDRMask(64, 128)}, "fd00::/64"},
		{"v6 default", net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}, "::/0"},
		{"host bits are cleared", net.IPNet{IP: net.IP{10, 1, 2, 77}, Mask: net.CIDRMask(24, 32)}, "10.1.2.0/24"},
		{"non-canonical mask", net.IPNet{IP: net.IP{10, 1, 2, 0}, Mask: net.IPMask{255, 0, 255, 0}}, ""},
		{"no ip", net.IPNet{Mask: net.CIDRMask(24, 32)}, ""},
		{"v4 with 32-bit mask is unmapped", net.IPNet{IP: net.ParseIP("::ffff:10.1.2.0"), Mask: net.CIDRMask(24, 32)}, "10.1.2.0/24"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := PrefixFromIPNet(tt.in)
			if ok != (tt.want != "") {
				t.Fatalf("ok = %v, want %v (got %s)", ok, tt.want != "", got)
			}
			if ok && got != netip.MustParsePrefix(tt.want) {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestToIPNetRoundTrip(t *testing.T) {
	for _, s := range []string{"10.1.2.0/24", "0.0.0.0/0", "fd00::/64", "::/0", "192.0.2.1/32", "::ffff:0.0.0.0/96"} {
		want := netip.MustParsePrefix(s)
		n := ToIPNet(want)
		if want.Addr().Is4() && (len(n.IP) != 4 || len(n.Mask) != 4) {
			t.Errorf("%s: IPv4 is not 4 bytes: %v", s, n)
		}
		got, ok := PrefixFromIPNet(n)
		if !ok || got != want {
			t.Errorf("%s: round trip gave %s", s, got)
		}
	}
}

func TestParsePrefixes(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{name: "empty", in: ""},
		{name: "one", in: "10.0.0.0/8", want: []string{"10.0.0.0/8"}},
		{name: "spaces and trailing comma", in: " 10.0.0.0/8 , fd00::/8,", want: []string{"10.0.0.0/8", "fd00::/8"}},
		{name: "host bits are cleared", in: "10.1.2.3/8", want: []string{"10.0.0.0/8"}},
		{name: "not a prefix", in: "10.0.0.0", wantErr: true},
		{name: "one bad entry", in: "10.0.0.0/8,nope", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePrefixes(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if !slices.Equal(sorted(got), tt.want) {
				t.Errorf("got %v, want %v", sorted(got), tt.want)
			}
		})
	}
}
