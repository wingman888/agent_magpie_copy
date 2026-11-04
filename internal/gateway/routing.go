package gateway

// Routing spreads a provider's requests over the keys or accounts it has
// on (see provider.Provider.Routing): smartly, in order, in turn, or the
// least used first. Whichever goes first, a key that suits the request
// still goes before one that doesn't, and one resting after a failure
// waits at the back for as long as its failure says: out of credit for
// half an hour, out of quota until it says it resets, rate limited until
// it says to try again, and otherwise a minute, longer each time it
// fails again.

import (
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// usageHalfLife is how fast the tokens a key served stop counting against
// it when choosing by use: half of them after an hour.
const usageHalfLife = time.Hour

var routed = struct {
	sync.Mutex
	turn     map[string]int      // provider → requests routed in turn
	used     map[string]tokenUse // candidate rest id → tokens it served
	failures map[string]int      // candidate rest id → failures since it last answered
}{turn: map[string]int{}, used: map[string]tokenUse{}, failures: map[string]int{}}

type tokenUse struct {
	n  float64
	at time.Time
}

func (u tokenUse) now(t time.Time) float64 {
	return u.n * math.Exp2(-t.Sub(u.at).Seconds()/usageHalfLife.Seconds())
}

// served counts what a candidate just answered against it.
func served(rest string, tokens int) {
	if tokens <= 0 {
		tokens = 1 // it answered, whether or not it said how much
	}
	now := time.Now()
	routed.Lock()
	routed.used[rest] = tokenUse{routed.used[rest].now(now) + float64(tokens), now}
	delete(routed.failures, rest)
	routed.Unlock()
}

// How long a candidate sits out, by why it failed.
const (
	creditRest   = 30 * time.Minute // out of credit: until someone tops it up
	quotaRest    = 15 * time.Minute // out of quota, with no word of when it resets
	longestWait  = time.Hour        // the most a vendor's own "try again at" is trusted
	longestRetry = 10 * time.Minute // failing again and again
)

var (
	// creditWords: the account or key has no money left.
	creditWords = regexp.MustCompile(`(?i)insufficient.?(balance|credit|fund)|balance|credit|billing|payment|arrear|overdue|suspended|余额|欠费|充值|账户.*(不足|停)`)
	// quotaWords: it has used up what its plan allows for now.
	usedUpWords = regexp.MustCompile(`(?i)quota|usage.?limit|limit.?reached|exceeded.*(plan|limit)|额度|用量|套餐|上限`)
)

// Why a candidate failed, as rest tells it.
const (
	failCredit = "credit"
	failQuota  = "quota"
	failRate   = "rate"
	failOther  = "other"
)

// failure says why a reply failed.
func failure(status int, body []byte) string {
	switch {
	case status == 402, creditWords.Match(body) && status != 429 || strings.Contains(string(body), "insufficient_quota"):
		return failCredit
	case usedUpWords.Match(body):
		return failQuota
	case status == 429:
		return failRate
	}
	return failOther
}

// restAfter sets a failed candidate aside for as long as its failure says.
func (s *Server) restAfter(rest string, status int, header http.Header, body []byte) {
	now := time.Now()
	d := fallbackCooldown
	switch failure(status, body) {
	case failCredit:
		d = creditRest
	case failQuota:
		d = quotaRest
		if w := retryAfter(header, now); w > 0 {
			d = w
		}
	case failRate:
		if w := retryAfter(header, now); w > 0 {
			d = w
		}
	default:
		routed.Lock()
		routed.failures[rest]++
		n := routed.failures[rest]
		routed.Unlock()
		d = min(fallbackCooldown<<min(n-1, 10), longestRetry)
	}
	restingUntil.Lock()
	restingUntil.m[rest] = now.Add(d)
	restingUntil.Unlock()
}

// retryAfter is when a vendor says to try again: Retry-After, in seconds
// or as a date, or the reset headers of OpenAI's and Anthropic's kind.
func retryAfter(h http.Header, now time.Time) time.Duration {
	var d time.Duration
	if v := h.Get("Retry-After"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			d = time.Duration(n * float64(time.Second))
		} else if t, err := http.ParseTime(v); err == nil {
			d = t.Sub(now)
		}
	}
	for k, vs := range h {
		k = strings.ToLower(k)
		if d > 0 || len(vs) == 0 || !strings.Contains(k, "reset") || !strings.Contains(k, "ratelimit") {
			continue
		}
		v := vs[0]
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			d = max(d, t.Sub(now))
		} else if w, err := time.ParseDuration(v); err == nil {
			d = max(d, w)
		} else if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 1e9 {
			d = max(d, time.Unix(n, 0).Sub(now))
		}
	}
	if d <= 0 {
		return 0
	}
	return min(d, longestWait)
}

var allowanceUsed = provider.AllowanceUsed

// Shares of an allowance past which an account is kept for when the others
// can't take a request: low, and all but used up.
const (
	lowShare  = 90
	usedShare = 98
)

// route orders one provider's candidates as its routing says.
func route(p provider.Provider, cs []candidate, model string, from provider.Protocol) []candidate {
	if len(cs) < 2 {
		return cs
	}
	var share map[string]float64
	if p.Account != nil {
		share = allowanceUsed(p.Account.Agent)
	}
	shareOf := func(c candidate) float64 { // 0 when not known
		if c.p.Account == nil {
			return 0
		}
		return share[c.p.Account.User]
	}
	switch p.Routing {
	case "":
		// the first while it has quota to spare, keeping the vendor's
		// prompt cache warm; past that, whichever has the most left, and
		// one all but used up only when nothing else can take it
		var fine, low, spent []candidate
		for _, c := range cs {
			switch v := shareOf(c); {
			case v >= usedShare:
				spent = append(spent, c)
			case v >= lowShare:
				low = append(low, c)
			default:
				fine = append(fine, c)
			}
		}
		for _, l := range [][]candidate{low, spent} {
			sort.SliceStable(l, func(i, j int) bool {
				return shareOf(l[i]) < shareOf(l[j])
			})
		}
		cs = append(append(fine, low...), spent...)
	case provider.Ordered:
		return cs
	case provider.Rotate:
		routed.Lock()
		n := routed.turn[p.ID] % len(cs)
		routed.turn[p.ID]++
		routed.Unlock()
		cs = append(append([]candidate{}, cs[n:]...), cs[:n]...)
	case provider.LeastUsed:
		// a subscription by the share of its allowance used, as the vendor
		// says; then, and for keys, by what magpie sent it lately
		now := time.Now()
		routed.Lock()
		tokens := make([]float64, len(cs))
		for i, c := range cs {
			tokens[i] = routed.used[c.rest].now(now)
		}
		routed.Unlock()
		idx := make([]int, len(cs))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool {
			ca, cb := cs[idx[a]], cs[idx[b]]
			if sa, sb := shareOf(ca), shareOf(cb); sa != sb {
				return sa < sb
			}
			return tokens[idx[a]] < tokens[idx[b]]
		})
		out := make([]candidate, len(cs))
		for i, j := range idx {
			out[i] = cs[j]
		}
		cs = out
	default:
		return cs
	}
	if p.Account == nil {
		sort.SliceStable(cs, func(i, j int) bool { return keyFit(cs[i].p, model, from) < keyFit(cs[j].p, model, from) })
	}
	return cs
}
