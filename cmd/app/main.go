package main

import (
	"net"
	"os"
	"strconv"

	envChecks "git.ruekov.eu/ruakij/routingtabletowg/lib/environmentchecks"
	ip2Map "git.ruekov.eu/ruakij/routingtabletowg/lib/iproute2mapping"
	"git.ruekov.eu/ruakij/routingtabletowg/lib/wgchecks"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

var envRequired = []string{
	"INTERFACE",
}
var envDefaults = map[string]string{
	"INTERFACE": "wg0",
	//"MANAGE_ALL": "true",

	"FILTER_PROTOCOL": "-1",
	"FILTER_TABLE":    "-1",

	"PERIODIC_SYNC": "-1",
}

func main() {
	// Environment-vars
	err := envChecks.HandleRequired(envRequired)
	if(err != nil){
		logger.Error.Fatal(err)
	}
	envChecks.HandleDefaults(envDefaults)

	iface := os.Getenv("INTERFACE")
	//MANAGE_ALL = os.Getenv("MANAGE_ALL")

	// Check if ip2Map has init-errors
	for _, err := range ip2Map.Errors {
		logger.Warn.Printf("iproute2mapping: %s", err)
	}

	// Parse filter-env-vars
	filterProtocolStr := os.Getenv("FILTER_PROTOCOL")
	filterProtocol, err := ip2Map.TryGetId(ip2Map.PROTOCOL, filterProtocolStr)
	if err != nil {
		logger.Error.Fatalf("Couldn't read FILTER_PROTOCOL '%s': %s", filterProtocolStr, err)
	}

	filterTableStr := os.Getenv("FILTER_TABLE")
	filterTable, err := ip2Map.TryGetId(ip2Map.TABLE, filterTableStr)
	if err != nil {
		logger.Error.Fatalf("Couldn't read FILTER_TABLE '%s': %s", filterTableStr, err)
	}

	periodicSyncStr := os.Getenv("PERIODIC_SYNC")
	periodicSync, err := strconv.Atoi(periodicSyncStr)
	if err != nil {
		logger.Error.Fatalf("Couldn't read PERIODIC_SYNC '%s': %s", periodicSyncStr, err)
	}

	// Create filter
	filterOptions := FilterOptions{
		Table: filterTable,
		Protocol: filterProtocol,
	}

	// Get Link-Device
	link, err := netlink.LinkByName(iface)
	if err != nil {
		logger.Error.Fatalf("Couldn't get interface '%s': %s", iface, err)
	}

	// Test getting wg-client
	client, err := wgctrl.New()
	if err != nil {
		logger.Error.Fatalf("Couldn't create wgctl-client: %s", err)
	}
	// Test getting wg-device
	_, err = client.Device(iface)
	if err != nil {
		logger.Error.Fatalf("Couldn't get wg-interface '%s': %s", iface, err)
	}

	
	// Subscribe to route-change events
	routeSubChan, routeSubDoneChan := make(chan netlink.RouteUpdate), make(chan struct{})

	netlink.RouteSubscribe(routeSubChan, routeSubDoneChan)
	go handleRouteEvents(routeSubChan, filterOptions, iface)

	//# Initial Route-setup
	// Get routing-table entries from device
	routeList, err := netlink.RouteList(link, netlink.FAMILY_ALL)
	if err != nil {
		logger.Error.Fatalf("Couldn't get route-entries: %s", err)
	}
	
	logger.Info.Printf("Initially setting all current routes")
	syncCurrentRoutesToHandler(routeSubChan, routeList)
	
	select {}
}

func syncCurrentRoutesToHandler(routeSubChan chan netlink.RouteUpdate, routeList []netlink.Route){
	
	for _, route := range routeList {
		// Ignore routes with empty gateway
		if(route.Gw == nil){
			continue
		}
	
		// Send current routes to handler
		routeSubChan <- netlink.RouteUpdate{
			Type:  unix.RTM_NEWROUTE,
			Route: route,
		}
	}
}

var routeUpdateTypeMapFromId = map[uint16]string{
	unix.RTM_NEWROUTE: "+",
	unix.RTM_DELROUTE: "-",
}
// TODO: Add proxy to apply filter in channels rather than.. this mess
func handleRouteEvents(routeSubChan <-chan netlink.RouteUpdate, filterOptions FilterOptions, iface string) {
	// Create wg-client
	client, err := wgctrl.New()
	if err != nil {
		logger.Error.Fatalf("Couldn't create wgctl-client: %s", err)
	}
	
	for {
		// Receive Route-Updates
		routeUpdate := <-routeSubChan
		route := routeUpdate.Route

		// Check filter
		if(!CheckFilter(filterOptions, routeUpdate.Route)){
			continue
		}
		
		// Special case for default-route
		if route.Dst == nil{
			if route.Gw.To4() != nil { // IPv4
				route.Dst = &net.IPNet{
					IP: net.IPv4zero,
					Mask: net.CIDRMask(0, 32),
				}
			} else { // IPv6
				route.Dst = &net.IPNet{
					IP: net.IPv6zero,
					Mask: net.CIDRMask(0, 128),
				}
			}
		}

		logger.Info.Printf("Route-Update: [%s] %s via %s", routeUpdateTypeMapFromId[routeUpdate.Type], route.Dst, route.Gw)

		// Get wgDevice
		wgDevice, err := client.Device(iface)
		if err != nil {
			logger.Error.Fatalf("Couldn't get wg-interface '%s' while running: %s", iface, err)
		}

		// Empty config for filling in switch
		var wgConfig wgtypes.Config

		switch routeUpdate.Type{
		case unix.RTM_NEWROUTE:
			// Check if gateway is set
			if route.Gw == nil{
				logger.Warn.Printf("Gateway unset, ignoring")
				continue
			}

			// Check if other peer already has exact same dst
			if peer, err := wgChecks.PeerByIPNet(wgDevice.Peers, *route.Dst); err == nil {
				logger.Warn.Printf("dst-IPNet already set for Peer '%s', ignoring", peer.PublicKey)
				continue
			}

			// Get peer containing gateway-addr
			peer, err := wgChecks.PeerByIP(wgDevice.Peers, route.Gw)
			if(err != nil){
				logger.Warn.Printf("No peer found containing gw-IP '%s', ignoring", route.Gw)
				continue
			}

			// Set peerConfig, this will override set values for that peer
			wgConfig.Peers = []wgtypes.PeerConfig{
				{
					PublicKey: peer.PublicKey,
					AllowedIPs: append(peer.AllowedIPs, *route.Dst),
				},
			}

		case unix.RTM_DELROUTE:
			// Get peer containing dst-NetIP
			peerIndex, ipNetIndex, err := wgChecks.PeerIndexByIPNet(wgDevice.Peers, *route.Dst)
			if(err != nil){
				logger.Warn.Printf("No peer found having dst-IPNet '%s', ignoring", route.Dst)
				continue
			}
			peer := wgDevice.Peers[peerIndex]

			// Delete dstNet from allowedIPs
			peer.AllowedIPs[ipNetIndex] = peer.AllowedIPs[len(peer.AllowedIPs)-1]
			peer.AllowedIPs = peer.AllowedIPs[:len(peer.AllowedIPs)-1]

			// Set peerConfig, this will override set values for that peer
			wgConfig.Peers = []wgtypes.PeerConfig{
				{
					PublicKey: peer.PublicKey,
					UpdateOnly: true,

					ReplaceAllowedIPs: true,
					AllowedIPs: peer.AllowedIPs,
				},
			}
		}

		err = client.ConfigureDevice(iface, wgConfig)
		if(err != nil){
			logger.Error.Fatalf("Error configuring wg-device '%s': %s", iface, err)
		}
	}
}
