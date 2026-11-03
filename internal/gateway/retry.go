package gateway

// Retries: a request that failed in a way that may mend itself is tried
// again, but only on the last candidate, which has no other to hand it to.
// It is tried up to maxRetries more times: a second after the first failure,
// two after the second, unless the vendor's Retry-After says otherwise. A
// Retry-After longer than maxRetryWait is not waited out: the error goes to
// the agent. A 429 is retried only when the vendor says when to try again.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxRetries   = 2
	maxRetryWait = 8 * time.Second
)

// transientStatus is whether a failure status may be gone by the next try.
func transientStatus(status int) bool {
	switch status {
	case 408, 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}

// retryDelay is how long to wait before try number n+1 (n counts the tries
// that failed so far, from zero), and whether to try again at all.
func retryDelay(status int, h http.Header, n int, now time.Time) (time.Duration, bool) {
	if n >= maxRetries || !transientStatus(status) {
		return 0, false
	}
	if v := h.Get("Retry-After"); v != "" {
		if d, ok := parseRetryAfter(v, now); ok {
			return d, d <= maxRetryWait
		}
	}
	if status == 429 {
		return 0, false
	}
	return time.Second << n, true
}

// parseRetryAfter reads a Retry-After header: seconds, or a date.
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
		if n < 0 {
			return 0, false
		}
		return time.Duration(n * float64(time.Second)), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

// pause waits d before a retry, and reports false if the agent went away
// meanwhile. Tests replace the wait through s.sleep.
func (s *Server) pause(ctx context.Context, d time.Duration) bool {
	if s.sleep != nil {
		return s.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
