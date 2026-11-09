package gateway

// Retries and the head of a stream. The last provider a request may go to
// is asked again when its failure may clear by itself, with a wait between
// the tries; and a stream from a provider is held back until its first
// content, so that one that fails before saying anything hands the request
// on as if it had failed at once.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// retryDelays are the waits before the last provider's first and second
// retry. A Retry-After the reply gave replaces them.
var retryDelays = []time.Duration{time.Second, 2 * time.Second}

// retryMaxAfter is the longest Retry-After worth waiting for; a longer one
// is not waited for, and the error is handed to the agent.
var retryMaxAfter = 8 * time.Second

// gateWait and gateBytes bound how long, and how much, a stream may be held
// back waiting for its first content; past either, it goes on to the agent.
var (
	gateWait  = 15 * time.Second
	gateBytes = 1 << 20
)

// errorStatus is the HTTP status an error stands for, by the type or code the
// provider named it with: a number, or words such as overloaded_error. An
// error it cannot place is a bad gateway.
func errorStatus(typ string, code json.RawMessage) int {
	c := strings.Trim(string(code), `"`)
	if n, err := strconv.Atoi(c); err == nil && n >= 400 && n < 600 {
		return n
	}
	k := strings.ToLower(typ + " " + c)
	switch {
	case strings.Contains(k, "overload"):
		return 529
	case strings.Contains(k, "insufficient_quota"), strings.Contains(k, "rate_limit"), strings.Contains(k, "ratelimit"), strings.Contains(k, "rate limit"):
		return 429
	case strings.Contains(k, "authentication"), strings.Contains(k, "invalid_api_key"):
		return 401
	case strings.Contains(k, "permission"):
		return 403
	case strings.Contains(k, "not_found"):
		return 404
	case strings.Contains(k, "timeout"):
		return 504
	case strings.Contains(k, "invalid_request"), strings.Contains(k, "invalid_prompt"), strings.Contains(k, "context_length"):
		return 400
	}
	return 502
}

// eventStatus is the status a stream's error event stands for.
func eventStatus(ev Event) int {
	if ev.Status == 0 {
		return 502
	}
	return ev.Status
}

// retryAfterOf is the Retry-After a reply gave, in seconds or as a date.
func retryAfterOf(h http.Header, now time.Time) (time.Duration, bool) {
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

// retryWait is how long to wait before retry n (from 0) of a last provider
// whose reply carried h, and whether to retry at all.
func retryWait(h http.Header, n int) (time.Duration, bool) {
	if n >= len(retryDelays) {
		return 0, false
	}
	d := retryDelays[n]
	if w, ok := retryAfterOf(h, time.Now()); ok {
		d = w
	}
	if d > retryMaxAfter {
		return 0, false
	}
	return d, true
}

// sleepFor waits d, or until ctx ends; it says whether the wait ran out.
func sleepFor(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// isContent says an event carries what the agent is after: text, reasoning,
// or a tool call. Before the first of these, nothing has been said.
func isContent(k EventKind) bool {
	return k == KText || k == KThink || k == KToolStart || k == KToolArgs
}

// streamFail is the error a stream opened with, before any content.
type streamFail struct {
	status int
	msg    string
}

// errPeek ends the reading of a stream's head.
var errPeek = errors.New("gateway: stream head read")

// awaitContent reads a relayed stream up to its first content, keeping every
// byte read in pre. It returns the error the stream opened with, when another
// provider may answer it instead; nil otherwise, which includes a stream that
// ends first, or that has been waited on too long or grown too big.
func awaitContent(rd io.Reader, proto provider.Protocol, pre *bytes.Buffer) *streamFail {
	var fail *streamFail
	decided := false
	began := time.Now()
	dec := decoder(proto)
	_ = readSSE(io.TeeReader(rd, pre), func(_, data string) error {
		_ = dec(data, func(ev Event) {
			if decided {
				return
			}
			switch {
			case ev.Kind == KError:
				decided = true
				if st := eventStatus(ev); retryable(st, []byte(ev.Text)) {
					fail = &streamFail{st, ev.Text}
				}
			case isContent(ev.Kind):
				decided = true
			}
		})
		if decided || pre.Len() > gateBytes || time.Since(began) > gateWait {
			return errPeek
		}
		return nil
	})
	return fail
}

// errorPayload is an error in the client's own shape, as the body of a reply.
func errorPayload(proto provider.Protocol, status int, msg string) []byte {
	b, _ := json.Marshal(errorBody(proto, status, msg))
	return b
}
