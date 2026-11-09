package gateway

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyRest is how long a key that just failed is tried last.
const keyRest = time.Minute

// gate sits between an upstream attempt and the client. An error status is
// held back, not sent, so the next key or model can still answer the request;
// anything else passes straight through, streamed as it comes. release sends
// a held error after all.
type gate struct {
	http.ResponseWriter
	hdr  http.Header // the attempt's own headers, copied out when it is sent
	code int
	held bool
	buf  bytes.Buffer

	// probe is set when a later candidate may still take over: the first
	// events of a reply are held back until they show whether the reply opens
	// with a retryable error (then it is held like an error status) or not.
	probe bool
	head  bytes.Buffer
	// leadMsg is the text of a retryable error the reply opened with; code is
	// then 502 and the attempt's own status is not meaningful.
	leadMsg string
}

// Header is the attempt's own header map, so a discarded error leaves no
// headers behind on the client's response.
func (g *gate) Header() http.Header {
	if g.hdr == nil {
		g.hdr = make(http.Header)
	}
	return g.hdr
}

func (g *gate) WriteHeader(code int) {
	if g.code != 0 {
		return
	}
	g.code = code
	if code >= 400 {
		g.held = true
		return
	}
	if g.probe {
		// sent once the first events show the reply is not an error
		return
	}
	g.commit(code)
}

// commit sends the status and the headers the attempt set.
func (g *gate) commit(code int) {
	h := g.ResponseWriter.Header()
	for k, v := range g.Header() {
		h[k] = v
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gate) Write(b []byte) (int, error) {
	if g.code == 0 {
		g.WriteHeader(http.StatusOK)
	}
	if g.held {
		return g.buf.Write(b)
	}
	if g.probe {
		g.head.Write(b)
		g.lead()
		return len(b), nil
	}
	return g.ResponseWriter.Write(b)
}

func (g *gate) Flush() {
	if g.held || g.probe {
		return
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// lead decides, from the events held back so far, whether the reply opens
// with a retryable error. A reply that does is held like an error status;
// one that does not is sent with everything held back.
func (g *gate) lead() {
	msg, state := leadState(g.head.Bytes())
	if state == leadUndecided {
		return
	}
	g.probe = false
	if state == leadError && retryText(msg) {
		g.held = true
		g.code = http.StatusBadGateway
		g.leadMsg = msg
		g.buf.Write(g.head.Bytes())
		g.head.Reset()
		return
	}
	g.commit(g.code)
	g.ResponseWriter.Write(g.head.Bytes())
	g.head.Reset()
}

// release sends a held error to the client, or the reply held back by probe.
func (g *gate) release() {
	if g.probe && !g.held {
		g.probe = false
		if g.code != 0 {
			g.commit(g.code)
			g.ResponseWriter.Write(g.head.Bytes())
		}
		g.head.Reset()
		return
	}
	if !g.held {
		return
	}
	g.held = false
	g.commit(g.code)
	g.ResponseWriter.Write(g.buf.Bytes())
}

// route decides how one candidate serves a request from client protocol from:
// relayed as-is when it speaks that protocol, else translated to to. A key
// limited to a protocol always goes to that protocol's endpoint.
func route(c provider.Provider, from provider.Protocol, body []byte) (to provider.Protocol, relay, ok bool) {
	if c.KeyProtocol != "" {
		return c.KeyProtocol, c.KeyProtocol == from, true
	}
	// a backend that only streams gets a non-streaming request translated
	// (the provider is always streamed on that path) rather than relayed
	relay = c.Base(from) != "" && (c.Account == nil || !c.Account.Stream || streamOf(body))
	if relay {
		return from, true, true
	}
	speaks := c.Speaks()
	if len(speaks) == 0 {
		return "", false, false
	}
	return speaks[0], false, true
}

// retryable reports whether an upstream failure should move the request on to
// the next candidate: rate limits, exhausted quota (402, or a 403 that says
// so), overload and server errors. Anything else, such as a 400, goes back to
// the client as it is. msg is the upstream's own error text.
func retryable(status int, msg string) bool {
	switch {
	case status == 402, status == 429, status == 529, status >= 500:
		return true
	case status == 403:
		m := strings.ToLower(msg)
		for _, w := range quotaWords {
			if strings.Contains(m, w) {
				return true
			}
		}
	}
	return false
}

// failedLabel names a candidate that failed, for the call record.
func failedLabel(c provider.Provider) string {
	if c.Account != nil {
		return c.ID + " (" + c.Account.User + ")"
	}
	if c.KeyName != "" {
		return c.ID + " (" + c.KeyName + ")"
	}
	return c.ID
}

func restKey(c provider.Provider) string {
	if c.Account != nil {
		// signed-in logins share an empty key; each one rests on its own
		return c.ID + "\x00login:" + strings.ToLower(c.Account.User)
	}
	return c.ID + "\x00" + provider.KeyRef(c.Key)
}

// keyOrder moves candidates that failed within keyRest to the back, keeping
// the order otherwise.
func (s *Server) keyOrder(cands []candidate) []candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var fresh, resting []candidate
	for _, a := range cands {
		t, ok := s.rested[restKey(a.c)]
		switch {
		case ok && now.Before(t):
			resting = append(resting, a)
		case ok:
			delete(s.rested, restKey(a.c))
			fresh = append(fresh, a)
		default:
			fresh = append(fresh, a)
		}
	}
	return append(fresh, resting...)
}

// restKeyFor marks a candidate as failed for keyRest.
func (s *Server) restKeyFor(c provider.Provider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rested[restKey(c)] = time.Now().Add(keyRest)
}

// unrestKey clears a candidate's failure mark once it has answered.
func (s *Server) unrestKey(c provider.Provider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rested, restKey(c))
}
