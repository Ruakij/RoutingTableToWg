package main

import (
	"context"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	envChecks "git.ruekov.eu/ruakij/routingtabletowg/lib/environmentchecks"
	ip2Map "git.ruekov.eu/ruakij/routingtabletowg/lib/iproute2mapping"
	"git.ruekov.eu/ruakij/routingtabletowg/lib/reconcile"

	"golang.zx2c4.com/wireguard/wgctrl"
)

var envRequired = []string{
	"INTERFACE",
}
var envDefaults = map[string]string{
	"INTERFACE": "wg0",
	// "MANAGE_ALL": "true",

	"FILTER_PROTOCOL": "-1",
	"FILTER_TABLE":    "-1",

	"PERIODIC_SYNC": "-1",
}

func main() {
	// Environment-vars
	err := envChecks.HandleRequired(envRequired)
	if err != nil {
		logger.Error.Fatal(err)
	}
	envChecks.HandleDefaults(envDefaults)

	iface := os.Getenv("INTERFACE")
	// MANAGE_ALL = os.Getenv("MANAGE_ALL")

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

	protectedStr := os.Getenv("PROTECTED_PREFIXES")
	protected, err := reconcile.ParsePrefixes(protectedStr)
	if err != nil {
		logger.Error.Fatalf("Couldn't read PROTECTED_PREFIXES '%s': %s", protectedStr, err)
	}

	// Create filter
	filterOptions := FilterOptions{
		Table:    filterTable,
		Protocol: filterProtocol,
	}

	client, err := wgctrl.New()
	if err != nil {
		logger.Error.Fatalf("Couldn't create wgctl-client: %s", err)
	}
	defer client.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	newSyncer(iface, filterOptions, protected, client).run(ctx, time.Duration(periodicSync)*time.Second)
	logger.Info.Printf("Shutting down")
}
