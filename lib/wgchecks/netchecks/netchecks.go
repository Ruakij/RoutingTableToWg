package netchecks

import (
	"fmt"
	"net"
	"reflect"
)

func IPNetIndexByIP(list *[]net.IPNet, ip *net.IP) (int, error) {
	for index, ipNetEntry := range *list {
		if ipNetEntry.Contains(*ip) {
			return index, nil
		}
	}
	return -1, fmt.Errorf("ip not in ipNet-list")
}

func IPNetIndexByIPNet(list *[]net.IPNet, ipNet *net.IPNet) (int, error) {
	for index, ipNetEntry := range *list {
		if reflect.DeepEqual(ipNetEntry, *ipNet) {
			return index, nil
		}
	}
	return -1, fmt.Errorf("ipNet not in ipNet-list")
}
