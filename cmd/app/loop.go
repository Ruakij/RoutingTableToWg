package main

import (
	"context"
	"time"

	"github.com/vishvananda/netlink"
)

const (
	// debounce coalesces bursts of route updates into one reconcile
	debounce = 250 * time.Millisecond

	minBackoff = time.Second
	maxBackoff = 30 * time.Second

	// Receive buffer of the subscription, so bursts of route updates (e.g. a
	// full table being loaded) do not overflow it while a reconcile is running.
	// An overflow closes the subscription.
	subscribeBufferSize = 4 << 20
	updatesQueueSize    = 4096
)

// backoff doubles the delay on every failure up to maxBackoff.
type backoff time.Duration

func (b *backoff) next() time.Duration {
	d := min(max(time.Duration(*b)*2, minBackoff), maxBackoff)
	*b = backoff(d)
	return d
}

func (b *backoff) reset() { *b = 0 }

// subscribeRoutes subscribes to route updates. Closing done ends the subscription.
func subscribeRoutes() (<-chan netlink.RouteUpdate, chan struct{}, error) {
	updates := make(chan netlink.RouteUpdate, updatesQueueSize)
	done := make(chan struct{})

	err := netlink.RouteSubscribeWithOptions(updates, done, netlink.RouteSubscribeOptions{
		ErrorCallback: func(err error) {
			select {
			case <-done: // closing the socket makes the receive fail
			default:
				logger.Warn.Printf("Route-subscription: %s", err)
			}
		},
		ReceiveBufferSize:      subscribeBufferSize,
		ReceiveBufferForceSize: true,
	})
	if err != nil {
		close(done)
		return nil, nil, err
	}
	return updates, done, nil
}

// run reconciles until the context ends. A reconcile happens
//   - after route updates of the interface, debounced,
//   - every periodic if it is set,
//   - after (re)subscribing, as updates may have been missed,
//   - after a failure, with backoff.
func (s *syncer) run(ctx context.Context, periodic time.Duration) {
	var ticks <-chan time.Time
	if periodic > 0 {
		ticker := time.NewTicker(periodic)
		defer ticker.Stop()
		ticks = ticker.C
	}

	var (
		updates <-chan netlink.RouteUpdate // nil without subscription
		done    chan struct{}

		// dirty: the state has to be reconciled
		// recheck: updates of other interfaces came in, one of them may be a recreated interface
		// armed: the work timer is running
		dirty, recheck, armed = true, false, true

		reconcileBackoff, subscribeBackoff backoff
	)
	work := time.NewTimer(0)
	resubscribe := time.NewTimer(0)

	arm := func() {
		if !armed {
			work.Reset(debounce)
			armed = true
		}
	}
	trigger := func() {
		dirty = true
		arm()
	}
	unsubscribe := func() {
		if done != nil {
			close(done)
		}
		updates, done = nil, nil
	}
	defer unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return

		case <-resubscribe.C:
			var err error
			updates, done, err = s.subscribe()
			if err != nil {
				delay := subscribeBackoff.next()
				logger.Error.Printf("Couldn't subscribe to route-changes, retrying in %s: %s", delay, err)
				resubscribe.Reset(delay)
				continue
			}
			subscribeBackoff.reset()
			trigger()

		case update, ok := <-updates:
			if !ok {
				unsubscribe()
				delay := subscribeBackoff.next()
				logger.Warn.Printf("Route-subscription closed, resubscribing in %s", delay)
				resubscribe.Reset(delay)
				continue
			}

			if s.watches(update.Route) {
				trigger()
			} else {
				recheck = true
				arm()
			}

		case <-ticks:
			logger.Info.Printf("Periodically syncing all routes")
			trigger()

		case <-work.C:
			armed = false
			if recheck && s.linkChanged() {
				dirty = true
			}
			recheck = false
			if !dirty {
				continue
			}

			if err := s.reconcile(); err != nil {
				delay := reconcileBackoff.next()
				logger.Error.Printf("Sync failed, retrying in %s: %s", delay, err)
				work.Reset(delay)
				armed = true
				continue
			}
			dirty = false
			reconcileBackoff.reset()
		}
	}
}
