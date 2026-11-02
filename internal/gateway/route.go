package gateway

// Candidates: which (provider, key, model) a request may be served by, and
// what happens when one of them is out of quota. A request goes to its own
// provider first, once per key that is switched on (ordered by how well the
// key fits the model and the client), then to each "provider/model" the
// provider lists as a fallback. A candidate that fails before it has sent
// the client anything, for a reason another candidate could cure, is
// skipped over and rests for a minute.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// cooldown is how long a candidate that ran out of quota is left alone.
const cooldown = time.Minute

// candidate is one way to serve a request.
type candidate struct {
	p     provider.Provider // narrowed to one key, and the protocols that key may use
	model string            // the vendor's model id
}

// label names the candidate in the call log: the provider id, and the key's
// name when it has one.
func (c candidate) label() string {
	if c.p.KeyName != "" {
		return c.p.ID + " (" + c.p.KeyName + ")"
	}
	return c.p.ID
}

// restKey identifies what rests: the provider's account/key.
func (c candidate) restKey() string {
	k := ""
	if c.p.Key != "" {
		k = provider.KeyRef(c.p.Key)
	}
	return c.p.ID + "\x00" + k
}

var oSeries = regexp.MustCompile(`^o\d`)

// family guesses which vendor family a model id belongs to.
func family(model string) string {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "claude"):
		return "claude"
	case strings.HasPrefix(m, "gpt") || strings.HasPrefix(m, "chatgpt") || oSeries.MatchString(m) || strings.Contains(m, "codex"):
		return "openai"
	}
	return ""
}

func speaks(p provider.Provider, protos ...provider.Protocol) bool {
	for _, x := range protos {
		if p.Base(x) != "" {
			return true
		}
	}
	return false
}

// keyRoutes lists the candidates for one provider and model: one per key
// that is on and has an endpoint it may use, best fit first. Claude models
// prefer keys that reach an Anthropic endpoint, GPT / o-series / codex
// models keys that reach an OpenAI one, anything else keys that speak the
// client's own protocol (so nothing is translated needlessly). The rest
// come after, as a last resort.
func keyRoutes(p provider.Provider, model string, from provider.Protocol) []candidate {
	keys := p.ActiveKeys()
	if p.Account != nil || len(keys) == 0 {
		return []candidate{{p: p, model: model}}
	}
	type ranked struct {
		c    candidate
		rank int
	}
	var rs []ranked
	fam := family(model)
	for _, k := range keys {
		kp := p.WithKey(k)
		if k.Protocol != "" {
			if p.Base(k.Protocol) == "" {
				continue // the provider has no such endpoint: this key sits out
			}
			kp = kp.Only(k.Protocol)
		}
		if len(kp.Speaks()) == 0 {
			continue
		}
		fits := false
		switch fam {
		case "claude":
			fits = speaks(kp, provider.Anthropic)
		case "openai":
			fits = speaks(kp, provider.Chat, provider.Responses)
		default:
			fits = speaks(kp, from)
		}
		rank := 1
		if fits {
			rank = 0
		}
		rs = append(rs, ranked{candidate{p: kp, model: model}, rank})
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].rank < rs[j].rank })
	out := make([]candidate, len(rs))
	for i, r := range rs {
		out[i] = r.c
	}
	return out
}

// candidates is every way to serve a request, in the order to try them:
// the requested provider's keys, then the fallbacks'. Candidates resting
// from an earlier failure move to the back.
func (s *Server) candidates(p provider.Provider, model string, from provider.Protocol) []candidate {
	out := keyRoutes(p, model, from)
	seen := map[string]bool{p.ID + "\x00" + model: true}
	for _, f := range p.Fallback {
		fp, fm, ok := provider.Resolve(f)
		if !ok || seen[fp.ID+"\x00"+fm] {
			continue
		}
		seen[fp.ID+"\x00"+fm] = true
		for _, c := range keyRoutes(fp, fm, from) {
			out = append(out, c)
		}
	}
	var ready, resting []candidate
	for _, c := range out {
		if s.resting(c) {
			resting = append(resting, c)
		} else {
			ready = append(ready, c)
		}
	}
	return append(ready, resting...)
}

func (s *Server) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

func (s *Server) resting(c candidate) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.rest[c.restKey()]
	return ok && s.now().Before(until)
}

func (s *Server) markRest(c candidate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rest == nil {
		s.rest = map[string]time.Time{}
	}
	s.rest[c.restKey()] = s.now().Add(cooldown)
}

// ---- what counts as "out of quota" ------------------------------------------------

var quotaPhrases = []string{
	"quota", "insufficient", "balance", "exceeded", "exhausted", "rate limit", "rate_limit",
	"ratelimit", "too many requests", "credit", "billing", "overloaded", "usage limit",
	"limit reached", "payment", "recharge", "top up",
	"额度", "余额", "用完", "用尽", "耗尽", "欠费", "限流", "频率", "充值", "不足", "过载",
}

func quotaText(s string) bool {
	s = strings.ToLower(s)
	for _, p := range quotaPhrases {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// holdable says whether a status might be a failure another candidate could
// cure; the reply is kept back until its body settles that.
func holdable(status int) bool {
	switch status {
	case 402, 403, 429, 502, 503, 504, 529:
		return true
	}
	return false
}

// worthRetry reports whether a reply is an error to move on from: rate
// limits, overload, outage, and the 402/403 replies that say the quota is
// gone. Ordinary errors (400, 401, 404, 5xx bugs…) are the client's to see.
func worthRetry(status int, body []byte) bool {
	switch status {
	case 429, 502, 503, 504, 529, 402:
		return true
	case 403:
		return quotaText(string(body))
	case 200:
		return streamErrorWorth(body)
	}
	return false
}

// streamErrorWorth looks at the first event of an event stream: is it an
// error of the kind that another candidate could cure?
func streamErrorWorth(b []byte) bool {
	head := bytes.TrimLeft(b, " \t\r\n")
	if i := bytes.Index(head, []byte("\n\n")); i >= 0 {
		head = head[:i]
	}
	var name, data string
	for _, line := range strings.Split(string(head), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data += strings.TrimSpace(line[5:])
		}
	}
	if data == "" {
		return false
	}
	var v struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
		Resp  struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(data), &v) != nil {
		return false
	}
	isErr := name == "error" || v.Type == "error" || len(v.Error) > 0 && string(v.Error) != "null" ||
		v.Type == "response.failed" || v.Type == "response.error"
	if !isErr {
		return false
	}
	return quotaText(data)
}

// ---- holding a reply back ------------------------------------------------------------

// holdWriter stands between a candidate and the client. Nothing reaches the
// client until the candidate shows its reply is not a failure worth moving
// on from; then everything goes through. A failure is discarded instead.
type holdWriter struct {
	dst   http.ResponseWriter
	h     http.Header
	code  int
	state int // 0 undecided, 1 holding, 2 sending
	buf   bytes.Buffer
}

func newHold(dst http.ResponseWriter) *holdWriter {
	return &holdWriter{dst: dst, h: http.Header{}}
}

func (w *holdWriter) Header() http.Header { return w.h }

func (w *holdWriter) WriteHeader(code int) {
	if w.state == 0 && w.code == 0 {
		w.code = code
	}
}

func (w *holdWriter) commit() {
	h := w.dst.Header()
	for k, v := range w.h {
		h[k] = v
	}
	if w.code != 0 {
		w.dst.WriteHeader(w.code)
	}
	w.state = 2
}

func (w *holdWriter) Write(b []byte) (int, error) {
	if w.state == 0 {
		code := w.code
		if code == 0 {
			code = 200
		}
		switch {
		case holdable(code):
			w.state = 1
		case code == 200 && strings.HasPrefix(w.h.Get("Content-Type"), "text/event-stream") && streamErrorWorth(b):
			w.state = 1
		default:
			w.commit()
		}
	}
	if w.state == 1 {
		if w.buf.Len() < 1<<20 {
			w.buf.Write(b)
		}
		return len(b), nil
	}
	return w.dst.Write(b)
}

func (w *holdWriter) Flush() {
	if w.state == 2 {
		if f, ok := w.dst.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// finish settles the reply once the candidate returned. It reports true
// when the reply was a failure that has been thrown away; otherwise
// whatever was held is sent.
func (w *holdWriter) finish(canMoveOn bool) bool {
	switch w.state {
	case 2:
		return false
	case 0:
		if w.code != 0 {
			if canMoveOn && worthRetry(w.code, nil) && holdable(w.code) {
				return true
			}
			w.commit()
		}
		return false
	}
	code := w.code
	if code == 0 {
		code = 200
	}
	if canMoveOn && worthRetry(code, w.buf.Bytes()) {
		return true
	}
	w.commit()
	w.dst.Write(w.buf.Bytes())
	return false
}
