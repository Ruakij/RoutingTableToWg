RoutingTableToWG
--

Translate Routing-Table Entries to Wireguard AllowedIPs with Filters

<br>

<!-- TOC -->
- [1. Overview](#1-overview)
  - [1.1. Usage](#11-usage)
  - [1.2. Install](#12-install)
- [2. Behaviour](#2-behaviour)
  - [2.1. Synced routes](#21-synced-routes)
  - [2.2. Reconcile](#22-reconcile)
  - [2.3. Ownership of AllowedIPs](#23-ownership-of-allowedips)
  - [2.4. Errors and limits](#24-errors-and-limits)
<!-- /TOC -->

<br>

# 1. Overview 

## 1.1. Usage

The Program watches the routes of a Wireguard-Interface and translates them to the AllowedIPs of its Peers.

It detects the Peer for a Route using the Gateway from the Route-Entry.<br>
In case routes clash or cant be added to Wireguard, Warnings will be logged.

<br>

### 1.1.1. Examples

- Dynamic Routing with Routing-Protocols (e.g. OSPF)
- Interacting with Wireguard using the familiar Tools like `iproute2`

<br>

## 1.2. Install

### 1.2.1. Environment

Variable|Description|Type|Default
-|-|-|-
`INTERFACE`       | Wireguard-Interface Name, empty for the default | String       | wg0
`FILTER_PROTOCOL` | Protocol to react on | Number / iproute2-name  | All
`FILTER_TABLE`    | Table to react on | Number / iproute2-name  | All
`PERIODIC_SYNC`   | Reguarly sync the routing-table <br> Useful when the wg-interface is changed/updated without us knowing  | Seconds  | -1
`PROTECTED_PREFIXES` | Prefixes that are never added, moved or removed, see [2.3](#23-ownership-of-allowedips) | Comma-separated CIDRs  | -

Protocol and table names are read from `rt_protos` and `rt_tables`, and the `*.conf` files in `rt_protos.d` and `rt_tables.d`, in `/usr/share/iproute2` and `/etc/iproute2`. Entries in `/etc/iproute2` take precedence.

<br>

### 1.2.2. Docker

The Container has to run in the network namespace of the Wireguard-Interface, which is the host for most setups. That makes it `network_mode: host`, as the routing-tables and interfaces are only visible there.

The only required capability is `NET_ADMIN`, for interacting with interfaces. All others can be dropped.

<details><summary><code>docker-compose.yml</code></summary>

```yaml
version: '3'

services:
  routingtabletowg:
    image: ghcr.io/ruakij/routingtabletowg
    restart: unless-stopped
    network_mode: host
    cap_drop:
      - ALL
    cap_add:
      - NET_ADMIN
    environment:
      - INTERFACE=<wgInterfaceName or empty for wg0>
```
</details>

<br>

### 1.2.3. Without Docker

Requires Go 1.27.3 or newer and Linux.

Clone the Repository `git clone <URI>` and build the Program with `make build` (or `go build -o bin/routingtabletowg ./cmd/app`)

Then you can run it with `./bin/routingtabletowg`

<br>

### 1.2.4. Development

Enable the pre-commit hook (formatting, `go vet`, tests, `golangci-lint`) with `git config core.hooksPath .githooks`.

The program only builds for Linux; on other systems set `GOOS=linux` for `go vet` and `golangci-lint`.

The integration test needs root, the Wireguard kernel module and the `wg` tool, e.g. in a privileged container: `docker run --privileged --rm -v "$PWD":/src -w /src golang:1.27-alpine sh -c "apk add --no-cache wireguard-tools-wg && go test -tags integration ./cmd/app"`

CI runs on GitHub Actions. Pushing a tag like `1.2.3` publishes `ghcr.io/ruakij/routingtabletowg` for `linux/amd64`, `linux/arm64` and `linux/arm/v7` and creates a GitHub release.

<br>

# 2. Behaviour

## 2.1. Synced routes

A route is translated when

- it is on the interface (`INTERFACE`) and passes `FILTER_PROTOCOL` and `FILTER_TABLE`. Without `FILTER_TABLE` the routes of all tables are used, not only the main table.
- it has a single gateway. Routes without gateway (e.g. connected routes) and multipath routes are ignored.
- its destination can be an AllowedIP. IPv4-mapped IPv6 prefixes such as `::ffff:10.0.0.0/104` cannot, so routes to them are ignored with a warning.
- the gateway lies in the AllowedIPs of a Peer. If several Peers contain it, the one with the longest matching prefix is chosen, like Wireguard does.

Gateways are looked up in the AllowedIPs the program does not manage (see [2.3](#23-ownership-of-allowedips)), so a gateway has to lie in an AllowedIP set up by hand. Right after the start the prefixes a route points to are left out as well, because they may be taken over. List prefixes that serve as gateways in `PROTECTED_PREFIXES`.<br>
Default routes are translated to `0.0.0.0/0` and `::/0`.

If several routes have the same destination, the one with the lowest metric is used (then the main table, then the lowest table number, then the lowest gateway). A backup route with a higher metric takes over when the primary route is gone.

<br>

## 2.2. Reconcile

Route changes only trigger a sync, the state is always read anew. This makes the result independent of lost or reordered route changes.

1. A sync starts on a change of a route of the interface or of a route replacing one of its destinations (changes within 250ms are combined), every `PERIODIC_SYNC` seconds, after (re)subscribing to route changes and after a failed sync.
2. The interface is looked up by name, so it may be recreated (it is then treated like a fresh start, see [2.3](#23-ownership-of-allowedips)). Its routes and the Peers are listed.
3. Every destination gets the Peer of its route, see [2.1](#21-synced-routes).
4. Missing prefixes are added to their Peer. Only the new prefixes are sent and Peers are never created, so nothing else on the Peer changes. A managed prefix that is on another Peer is moved to the Peer of the route.
5. Prefixes without a route that the program manages are removed.

<br>

## 2.3. Ownership of AllowedIPs

The program only removes prefixes it manages. These are the prefixes it added because of a route, or that were on the Peer of the route when it started. All other AllowedIPs are left alone, also those on Peers without a route.

- At the start, a prefix that is on a Peer already and that a route points to is taken over and managed from then on: it stays when it is on the Peer of the route, and it is moved there otherwise. Start means the first sync that could read the routes and the Peers, and the first one after the interface was recreated. This is the only point where a prefix that is there already can be taken over. It picks up what an earlier run left behind.
- Afterwards, a prefix that is there and not managed is manual. A route to it on the same Peer changes nothing and the prefix stays manual. A route to it via another Peer is a conflict: a warning is logged once and the prefix stays where it is.
- Prefixes in `PROTECTED_PREFIXES` are not touched at all, also not at the start. They are matched exactly, a covering or covered prefix does not protect. Prefixes that serve as gateways should be listed there: if a routing daemon has learned such a prefix as a route on the interface when the program starts, the prefix would be moved to the Peer of that route and routes behind it would lose their Peer.
- The managed prefixes are kept in memory. Prefixes derived from routes by an earlier run, whose routes were removed while the program was not running, are not recognised and stay until removed by hand.

<br>

## 2.4. Errors and limits

- Failing calls to Wireguard or netlink (also a missing interface) are logged and retried after 1s, doubling up to 30s. The program only exits on invalid configuration at start, or on `SIGINT` / `SIGTERM`.
- When the subscription to route changes breaks (e.g. a burst overflows its buffer), it is renewed and everything is synced.
- Wireguard can only replace the whole list of AllowedIPs of a Peer. Removing prefixes reads the list right before writing it, but changes to the same Peer by other tools within these milliseconds can be lost. Adding prefixes has no such window.
- The library used to configure Wireguard (wgctrl) cannot write an IPv4-mapped IPv6 AllowedIP such as `::ffff:10.0.0.0/104`: the kernel rejects it, and replacing the list would lose it and all entries after it. Removals on a Peer having one are skipped: the prefixes stay, with a warning, until the entry is gone.
