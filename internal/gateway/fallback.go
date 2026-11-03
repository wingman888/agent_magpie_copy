package gateway

// Fallback: a provider can name models to use when it can't take a request
// — a coding plan out of quota, a rate limit, an overloaded or failing
// vendor. The request goes to the next one only while none of the reply has
// been sent, so an agent sees one clean answer from whoever gave it, never a
// half from each. A provider that just failed that way waits at the back of
// the line for a minute, rather than costing every request a doomed try.

import (
	"bytes"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const fallbackCooldown = time.Minute

type candidate struct {
	p     provider.Provider
	model string
}

// attempt is one try at a request: a provider, sending with one of its keys.
type attempt struct {
	p     provider.Provider // p.Key is the key this try sends
	model string
	cred  provider.Credential
	// named says the call record names the account: the provider has several
	// enabled keys. A provider with one keeps the plain record.
	named bool
}

// label names the try in the call record.
func (a attempt) label() string {
	if a.named {
		return a.cred.Label(a.p.ID)
	}
	return a.p.ID
}

// candidates lists the tries in order: the primary's enabled keys, then each
// fallback's. Within a provider, keys resting after a recent failure move
// behind the rest; a provider with no ready key moves behind the others.
// A provider whose keys are all off is reported in skipped, never sent
// unkeyed.
func (s *Server) candidates(p provider.Provider, model string) (out []attempt, skipped []string) {
	provs := []candidate{{p, model}}
	seen := map[string]bool{p.ID + "/" + model: true}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok || seen[fp.ID+"/"+fm] {
			continue
		}
		seen[fp.ID+"/"+fm] = true
		provs = append(provs, candidate{fp, fm})
	}
	var ready, resting []attempt
	for _, c := range provs {
		creds := c.p.KeysOn()
		if len(creds) == 0 {
			skipped = append(skipped, c.p.ID+": no enabled key")
			continue
		}
		var rdy, rst []attempt
		for _, cred := range creds {
			a := attempt{p: withKey(c.p, cred), model: c.model, cred: cred, named: len(creds) > 1 && cred.ID != ""}
			if s.resting(restKey(c.p.ID, cred)) {
				rst = append(rst, a)
			} else {
				rdy = append(rdy, a)
			}
		}
		if len(rdy) == 0 {
			resting = append(resting, rst...)
			continue
		}
		ready = append(ready, rdy...)
		ready = append(ready, rst...)
	}
	return append(ready, resting...), skipped
}

// withKey is the provider as it is when sending with the credential's key.
func withKey(p provider.Provider, c provider.Credential) provider.Provider {
	if c.Key != "" {
		p.Key = c.Key
	}
	return p
}

// untried reports whether any of the later tries may still be sent.
func untried(later []attempt, failedKeys map[string]bool) bool {
	for _, a := range later {
		if !(a.named && failedKeys[restKey(a.p.ID, a.cred)]) {
			return true
		}
	}
	return false
}

// restKey is where a failed try rests: under its own key, so the provider's
// other keys are not held back with it.
func restKey(providerID string, c provider.Credential) string {
	return providerID + "\x00" + c.ID
}

// readyKey is the provider as it is when sending with its first enabled key
// that is not resting. When every enabled key rests, the first enabled one
// is used, as candidates would try it last. ok is false when no key is
// enabled at all.
func (s *Server) readyKey(p provider.Provider) (provider.Provider, bool) {
	creds := p.KeysOn()
	if len(creds) == 0 {
		return p, false
	}
	for _, c := range creds {
		if !s.resting(restKey(p.ID, c)) {
			return withKey(p, c), true
		}
	}
	return withKey(p, creds[0]), true
}

// scrub keeps a key out of a message: it appears masked, as everywhere else.
func scrub(msg, key string) string {
	if key == "" || !strings.Contains(msg, key) {
		return msg
	}
	mask := provider.Mask(key)
	if len(key) >= 8 {
		return strings.ReplaceAll(msg, key, mask)
	}
	// A very short key would mangle ordinary words ("k" inside "Fake"), so it
	// is masked only where it stands as a whole word.
	var b strings.Builder
	last := 0
	for i := 0; ; {
		j := strings.Index(msg[i:], key)
		if j < 0 {
			break
		}
		at := i + j
		end := at + len(key)
		if (at == 0 || !wordByte(msg[at-1])) && (end == len(msg) || !wordByte(msg[end])) {
			b.WriteString(msg[last:at])
			b.WriteString(mask)
			last = end
		}
		i = end
	}
	b.WriteString(msg[last:])
	return b.String()
}

// wordByte reports whether c can be part of a word.
func wordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

var restingUntil = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func (s *Server) resting(key string) bool {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	return time.Now().Before(restingUntil.m[key])
}

func (s *Server) rest(key string) {
	restingUntil.Lock()
	restingUntil.m[key] = time.Now().Add(fallbackCooldown)
	restingUntil.Unlock()
}

// quotaWords are how vendors say "out of quota" or "slow down" when their
// status code doesn't: some answer 400 or 403 with it.
var quotaWords = regexp.MustCompile(`(?i)quota|insufficient|balance|credit|billing|exceeded|rate.?limit|usage.?limit|limit.?reached|too many requests|overloaded|余额|额度|欠费|限流|频率|套餐|用量|上限`)

// retryable says whether another provider may do better with a request
// that failed this way.
func retryable(status int, body []byte) bool {
	switch {
	case status == 402, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 401, status == 403:
		return quotaWords.Match(body)
	}
	return false
}

// holdWriter keeps an error reply back while another provider may still
// answer: headers and body wait until release, or are dropped for the next
// try. Anything else goes straight through.
type holdWriter struct {
	w       http.ResponseWriter
	hold    bool
	header  http.Header
	status  int
	passing bool
	held    bytes.Buffer
	// key is the key this try sent; an error reply shows it masked.
	key string
	// upstream is the provider's own error body, before the gateway wrapped
	// it. Whether to try another key reads this, never the wrapped text.
	upstream []byte
}

// noteUpstream records the provider's own error body on a held reply.
func noteUpstream(w http.ResponseWriter, body []byte) {
	if h, ok := w.(*holdWriter); ok {
		h.upstream = body
	}
}

func newHoldWriter(w http.ResponseWriter, hold bool) *holdWriter {
	return &holdWriter{w: w, hold: hold, header: http.Header{}}
}

func (h *holdWriter) Header() http.Header { return h.header }

func (h *holdWriter) WriteHeader(code int) {
	if h.status != 0 {
		return
	}
	h.status = code
	if h.hold && code >= 400 {
		return
	}
	h.pass()
}

func (h *holdWriter) pass() {
	dst := h.w.Header()
	for k, v := range h.header {
		dst[k] = v
	}
	h.w.WriteHeader(h.status)
	h.passing = true
}

func (h *holdWriter) Write(b []byte) (int, error) {
	if h.status == 0 {
		h.WriteHeader(http.StatusOK)
	}
	n := len(b)
	if h.status >= 400 && h.key != "" {
		// an error reply may echo the key back; the client gets it masked
		b = []byte(scrub(string(b), h.key))
	}
	if !h.passing {
		h.held.Write(b)
		return n, nil
	}
	h.w.Write(b)
	return n, nil
}

func (h *holdWriter) Flush() {
	if f, ok := h.w.(http.Flusher); ok && h.passing {
		f.Flush()
	}
}

// failed reports a held error another provider could answer instead. It
// judges the provider's own status and body; a failure the gateway made
// itself (no endpoint, transport error) carries no body and judges by status.
func (h *holdWriter) failed() bool {
	return !h.passing && h.status >= 400 && retryable(h.status, h.upstream)
}

// release sends a held reply after all: nobody else is left to try.
func (h *holdWriter) release() {
	if h.passing || h.status == 0 {
		return
	}
	h.pass()
	h.w.Write(h.held.Bytes())
}
