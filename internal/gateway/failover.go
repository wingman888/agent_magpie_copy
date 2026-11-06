package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
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
	// a Claude Code account can only be served by the Claude Code binary,
	// which writes to the client directly; its reply is held until known
	if c.Account != nil && c.Account.Agent == "claude" {
		return s.tryClaudeAccount(w, r, from, model, body, u, final)
	}
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
		if st := openingFailure(res, final); st != 0 {
			res.Body.Close()
			return from, st, "", true
		}
		status, msg = s.relayReply(w, res, from, c, u)
		return from, status, msg, false
	}
	speaks := c.Speaks()
	if len(speaks) == 0 {
		msg = c.Name + " has no endpoint configured"
		// no protocol to send in: an empty to marks that nothing was sent
		return "", writeError(w, from, http.StatusBadGateway, msg), msg, false
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
	if st := openingFailure(res, final); st != 0 {
		res.Body.Close()
		return to, st, "", true
	}
	status, msg = s.translateReply(w, res, actual, from, c, request, stream, u)
	return to, status, msg, false
}

// attempt is one candidate a request may be sent to: a provider or one of its
// keys, and the vendor model it answers as. owner is the provider the attempt
// belongs to, which is what the call log names.
type attempt struct {
	c     provider.Provider
	model string
	owner string
}

// attempts lists what a request is tried against, in order: the provider's
// keys, best first, then each model of its fallback list with that provider's
// keys. Candidates still resting from a failure move behind the others.
func (s *Server) attempts(p provider.Provider, from provider.Protocol, model string) []attempt {
	var out []attempt
	for _, c := range p.Candidates(from, model) {
		out = append(out, attempt{c: c, model: model, owner: p.ID})
	}
	for _, entry := range p.Fallback {
		fp, fmodel, ok := fallbackTarget(entry)
		if !ok {
			continue
		}
		for _, c := range fp.Candidates(from, fmodel) {
			if c.Account == nil && len(c.Speaks()) == 0 {
				continue
			}
			out = append(out, attempt{c: c, model: fmodel, owner: fp.ID})
		}
	}
	var fresh, rest []attempt
	for _, a := range out {
		if s.cooling(a.c) {
			rest = append(rest, a)
		} else {
			fresh = append(fresh, a)
		}
	}
	return append(fresh, rest...)
}

// fallbackTarget reads one fallback entry, "provider/model". An entry that
// does not name a ready provider and a model is skipped silently.
func fallbackTarget(entry string) (provider.Provider, string, bool) {
	pid, model, ok := strings.Cut(strings.TrimSpace(entry), "/")
	pid, model = strings.TrimSpace(pid), strings.TrimSpace(model)
	if !ok || pid == "" || model == "" {
		return provider.Provider{}, "", false
	}
	fp, err := provider.Find(pid)
	if err != nil || !fp.Ready() {
		return provider.Provider{}, "", false
	}
	return *fp, model, true
}

// tryClaudeAccount serves a request through the Claude Code binary. The reply
// is held in memory so that a failure another candidate may get past is
// dropped before anything reaches the client.
func (s *Server) tryClaudeAccount(w http.ResponseWriter, r *http.Request, from provider.Protocol, model string, body []byte, u *Usage, final bool) (provider.Protocol, int, string, bool) {
	buf := &bufferedWriter{}
	status, msg := s.serveClaudeSubscription(buf, r, from, model, body, u)
	if status == 0 {
		status = buf.status
	}
	if !final && retryable(status, buf.body.Bytes()) {
		return provider.Anthropic, status, msg, true
	}
	for k, vs := range buf.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	if buf.status == 0 {
		buf.status = http.StatusOK
	}
	w.WriteHeader(buf.status)
	w.Write(buf.body.Bytes())
	return provider.Anthropic, status, msg, false
}

// bufferedWriter holds a reply until the caller knows whether to keep it.
type bufferedWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedWriter) Header() http.Header {
	if b.header == nil {
		b.header = http.Header{}
	}
	return b.header
}

func (b *bufferedWriter) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

func (b *bufferedWriter) Flush() {}

// openingFailure reads the first event of a streamed reply before anything is
// written to the client. A stream that opens with an error event saying the
// vendor is out of quota, rate limited, overloaded or unavailable is a failure
// another candidate may get past; it returns the status that failure stands
// for. Otherwise it returns 0. The bytes read are put back in front of the body.
func openingFailure(res *http.Response, final bool) int {
	if final || res.StatusCode >= 400 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		return 0
	}
	br := bufio.NewReaderSize(res.Body, 64<<10)
	var head bytes.Buffer
	var data []string
	for head.Len() < 64<<10 {
		line, err := br.ReadBytes('\n')
		head.Write(line)
		text := strings.TrimRight(string(line), "\r\n")
		switch {
		case text == "" && len(data) > 0:
			// the first data event is complete
			err = io.EOF
		case strings.HasPrefix(text, "data:"):
			data = append(data, strings.TrimPrefix(text[len("data:"):], " "))
		}
		if err != nil {
			break
		}
	}
	res.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head.Bytes()), br), res.Body}
	return streamErrorStatus(strings.Join(data, "\n"))
}

// streamErrorStatus maps an error event's payload to the status it stands for,
// or 0 when the event is not an error another candidate may get past. The
// shapes read are Anthropic's {"type":"error"}, OpenAI's {"error":...} and the
// Responses API's response.failed.
func streamErrorStatus(data string) int {
	type apiError struct {
		Type string          `json:"type"`
		Code json.RawMessage `json:"code"`
	}
	var v struct {
		Type     string    `json:"type"`
		Error    *apiError `json:"error"`
		Response *struct {
			Error *apiError `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(data), &v) != nil {
		return 0
	}
	e := v.Error
	if e == nil && v.Response != nil {
		e = v.Response.Error
	}
	if e == nil {
		return 0
	}
	k := strings.ToLower(e.Type + " " + string(e.Code))
	switch {
	case strings.Contains(k, "rate_limit"), strings.Contains(k, "rate limit"), strings.Contains(k, "too_many"):
		return http.StatusTooManyRequests
	case strings.Contains(k, "quota"), strings.Contains(k, "insufficient"), strings.Contains(k, "billing"), strings.Contains(k, "exhaust"):
		return http.StatusPaymentRequired
	case strings.Contains(k, "overload"):
		return 529
	case strings.Contains(k, "unavailable"), strings.Contains(k, "server_error"), strings.Contains(k, "api_error"), strings.Contains(k, "internal"):
		return http.StatusServiceUnavailable
	}
	return 0
}
