package provider

// Routing: how the gateway spreads requests over the keys or accounts a
// provider has on (see Provider.Routing). Choosing by use needs to know the
// use without waiting for it, so a subscription's allowance is read from
// what was fetched last, and fetched again behind the request when it has
// gone stale.

import (
	"context"
	"sync"
	"time"
)

// The routings besides the default, smart, in order.
const (
	Ordered   = "order"
	Rotate    = "rotate"
	LeastUsed = "usage"
)

// SetRouting changes how a provider's requests spread over its keys or
// accounts.
func SetRouting(id, routing string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.Routing = routing
	return Save(*p)
}

var usedCache struct {
	sync.Mutex
	m       map[string]map[string]float64 // agent → user → share used
	at      map[string]time.Time
	loading map[string]bool
}

// AllowanceUsed is how much of its allowance each of an agent's accounts
// has used, 0–100 by user, as last known: it never waits, and asks again
// in the background when that was over a minute ago. An account missing
// is one not known yet.
func AllowanceUsed(agent string) map[string]float64 {
	c := &usedCache
	c.Lock()
	defer c.Unlock()
	if c.m == nil {
		c.m, c.at, c.loading = map[string]map[string]float64{}, map[string]time.Time{}, map[string]bool{}
	}
	if !c.loading[agent] && time.Since(c.at[agent]) > time.Minute {
		c.loading[agent] = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			used := map[string]float64{}
			for user, q := range LoginUsage(ctx, agent) {
				if q.Error != "" || len(q.Windows) == 0 {
					continue
				}
				most := 0.0
				for _, w := range q.Windows {
					most = max(most, w.Used)
				}
				used[user] = most
			}
			c.Lock()
			c.m[agent], c.at[agent], c.loading[agent] = used, time.Now(), false
			c.Unlock()
		}()
	}
	return c.m[agent]
}
