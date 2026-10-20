package main

import (
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// FilterOptions select the routes to translate, -1 selects all.
type FilterOptions struct {
	Table    int
	Protocol int
}

// routeFilter returns the filter and mask to list the matching routes of a link.
func (o FilterOptions) routeFilter(ifindex int) (*netlink.Route, uint64) {
	// RT_TABLE_UNSPEC selects all tables, otherwise only the main table is listed
	filter := &netlink.Route{LinkIndex: ifindex, Table: unix.RT_TABLE_UNSPEC}
	mask := netlink.RT_FILTER_OIF | netlink.RT_FILTER_TABLE

	if o.Table > 0 {
		filter.Table = o.Table
	}
	if o.Protocol != -1 {
		filter.Protocol = netlink.RouteProtocol(o.Protocol)
		mask |= netlink.RT_FILTER_PROTOCOL
	}
	return filter, mask
}
