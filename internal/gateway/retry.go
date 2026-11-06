package gateway

// Retrying: a candidate that is the last to try, and fails in a way that may
// mend by itself, is tried again, up to maxRetries times. The waits are a
// second, then two, unless the vendor's Retry-After says otherwise; a
// Retry-After longer than maxRetryWait is not waited for, and the error goes
// to the agent. A 429 with no Retry-After is not retried: the vendor gave no
// time to wait for.

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

// waitRetry sleeps before a retry; the agent going away ends the wait early
// with its error. Tests replace it so they need not wait.
var waitRetry = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryAfterHeader is the Retry-After a reply carries, in seconds or as a
// date, and whether it carries one.
func retryAfterHeader(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		return max(time.Duration(n*float64(time.Second)), 0), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

// recoverable says whether a failure may mend by itself: a timeout, a server
// fault, or a rate limit that says when to try again.
func recoverable(status int, h http.Header) bool {
	switch status {
	case 408, 500, 502, 503, 504, 529:
		return true
	case 429:
		_, ok := retryAfterHeader(h, time.Now())
		return ok
	}
	return false
}

// retryDelay says whether the failed try n (from 0) is retried, and how long
// to wait first.
func retryDelay(status int, h http.Header, n int) (time.Duration, bool) {
	if !recoverable(status, h) {
		return 0, false
	}
	if d, ok := retryAfterHeader(h, time.Now()); ok {
		return d, d <= maxRetryWait
	}
	return time.Duration(1<<n) * time.Second, true
}
