package gateway

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A provider's keys are candidates for a request, tried best first. A
// candidate that fails in a way another may get past is passed over before
// anything reaches the client, and rests for a while so later requests try
// the others first.

// cooldown is how long a failed candidate waits before it leads again.
const cooldown = time.Minute

// quotaWords mark a 403 as "out of quota" rather than "forbidden".
var quotaWords = []string{"quota", "insufficient", "balance", "exhaust", "exceed", "limit", "额度", "余额", "用完", "用尽", "不足"}

// retryable reports whether a failure with this status and body is one a
// different candidate may get past: rate limits, quota, overload, outages.
// A bad request (400) is the caller's fault and is never retried.
func retryable(status int, body []byte) bool {
	switch {
	case status == http.StatusTooManyRequests, status == http.StatusPaymentRequired, status >= 500:
		return true
	case status == http.StatusForbidden:
		msg := strings.ToLower(string(body))
		for _, w := range quotaWords {
			if strings.Contains(msg, w) {
				return true
			}
		}
	}
	return false
}

// slotKey identifies a candidate for the cooldown table.
func slotKey(c provider.Provider) string {
	return c.ID + "\x00" + provider.KeyRef(c.Key)
}

// cool rests a candidate after it failed.
func (s *Server) cool(c provider.Provider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cooldown == nil {
		s.cooldown = make(map[string]time.Time)
	}
	s.cooldown[slotKey(c)] = time.Now().Add(cooldown)
}

// cooling reports whether a candidate failed within the cooldown.
func (s *Server) cooling(c provider.Provider) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().Before(s.cooldown[slotKey(c)])
}

// coolingLast keeps the candidates' order but moves those still resting
// behind the others, so a request tries them only when nothing else is left.
func (s *Server) coolingLast(cs []provider.Provider) []provider.Provider {
	var fresh, rest []provider.Provider
	for _, c := range cs {
		if s.cooling(c) {
			rest = append(rest, c)
		} else {
			fresh = append(fresh, c)
		}
	}
	return append(fresh, rest...)
}

// gate looks at a reply before it is written. For a failure another candidate
// may get past (and this one is not the last), it reports retry=true; the
// body is kept readable so that a final reply can still be relayed.
func gate(res *http.Response, final bool) (*http.Response, bool) {
	if final || res.StatusCode < 400 {
		return res, false
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(b))
	return res, retryable(res.StatusCode, b)
}

// failedName describes a failed candidate for the call log: its provider,
// its key's name when it has one, and the status it failed with.
func failedName(c provider.Provider, status int) string {
	name := c.ID
	if c.KeyName != "" {
		name += "/" + c.KeyName
	}
	return fmt.Sprintf("%s (%d)", name, status)
}

// tryCandidate sends the request through one candidate. retry=true means the
// candidate failed in a way another may get past, and nothing was written to
// w. Otherwise the reply, or the error, has been written to w and reported
// as status and msg. final marks the last candidate, which never retries.
func (s *Server) tryCandidate(w http.ResponseWriter, r *http.Request, c provider.Provider, from provider.Protocol, model string, body []byte, u *Usage, final bool) (to provider.Protocol, status int, msg string, retry bool) {
	// a backend that only streams gets a non-streaming request translated
	// (the provider is always streamed on that path) rather than relayed
	if c.Base(from) != "" && (c.Account == nil || !c.Account.Stream || streamOf(body)) {
		res, err := s.relaySend(r, c, from, model, body)
		if err != nil {
			if !final {
				return from, http.StatusBadGateway, err.Error(), true
			}
			return from, writeError(w, from, http.StatusBadGateway, c.Name+": "+err.Error()), err.Error(), false
		}
		res, again := gate(res, final)
		if again {
			return from, res.StatusCode, "", true
		}
		status, msg = s.relayReply(w, res, from, c, u)
		return from, status, msg, false
	}
	speaks := c.Speaks()
	if len(speaks) == 0 {
		msg = c.Name + " has no endpoint configured"
		return from, writeError(w, from, http.StatusBadGateway, msg), msg, false
	}
	to = speaks[0]
	request, err := parse(from, body)
	if err != nil {
		return to, writeError(w, from, http.StatusBadRequest, err.Error()), err.Error(), false
	}
	stream := request.Stream
	request.Stream = true
	res, actual, err := s.forwardTranslated(r.Context(), c, to, request, model, r.Header)
	if err != nil {
		if !final {
			return to, http.StatusBadGateway, err.Error(), true
		}
		return to, writeError(w, from, http.StatusBadGateway, c.Name+": "+err.Error()), err.Error(), false
	}
	res, again := gate(res, final)
	if again {
		return to, res.StatusCode, "", true
	}
	status, msg = s.translateReply(w, res, actual, from, c, request, stream, u)
	return to, status, msg, false
}
