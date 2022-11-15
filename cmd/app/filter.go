package main

import (
	"github.com/vishvananda/netlink"
)

type FilterOptions struct {
	Table    int
	Protocol int
}

func CheckFilter(options FilterOptions, route netlink.Route) bool {
	if (options.Table != -1 && options.Table != route.Table) ||
		(options.Protocol != -1 && options.Protocol != route.Protocol) {
		return false
	}
	return true
}
