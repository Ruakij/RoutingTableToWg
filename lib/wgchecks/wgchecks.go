package wgChecks

import (
	"fmt"
	"net"

	"git.ruekov.eu/ruakij/routingtabletowg/lib/netchecks"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func PeerIndexByIP(peers []wgtypes.Peer, ip net.IP) (int, int, error) {
	for index, peer := range peers {
		if ipIndex, err := netchecks.IPNetIndexByIP(peer.AllowedIPs, ip); err == nil {
			return index, ipIndex, nil
		}
	}
	return -1, -1, fmt.Errorf("no peer by ip in list")
}
func PeerByIP(peers []wgtypes.Peer, ip net.IP) (*wgtypes.Peer, error) {
	index, _, err := PeerIndexByIP(peers, ip)
	if(err != nil) {
		return nil, err
	}
	return &peers[index], nil
}

func PeerIndexByIPNet(peers []wgtypes.Peer, ipNet net.IPNet) (int, int, error) {
	for index, peer := range peers {
		if ipNetIndex, err := netchecks.IPNetIndexByIPNet(peer.AllowedIPs, ipNet); err == nil {
			return index, ipNetIndex, nil
		}
	}
	return -1, -1, fmt.Errorf("no peer by ipNet in list")
}
func PeerByIPNet(peers []wgtypes.Peer, ipNet net.IPNet) (*wgtypes.Peer, error) {
	index, _, err := PeerIndexByIPNet(peers, ipNet)
	if(err != nil) {
		return nil, err
	}
	return &peers[index], nil
}
