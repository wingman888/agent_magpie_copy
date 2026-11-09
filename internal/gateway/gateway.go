package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// DefaultAddr is where the gateway listens unless MAGPIE_ADDR says otherwise.
const DefaultAddr = "127.0.0.1:3425"

// Token is the bearer token agents are told to use. The gateway only
// listens on loopback and accepts anything, but agents insist on one.
const Token = "magpie"

// Version is set by main.
var Version = "dev"

// Addr is the listen address.
func Addr() string {
	if a := os.Getenv("MAGPIE_ADDR"); a != "" {
		return a
	}
	return DefaultAddr
}

// URL is the base URL agents use, e.g. http://127.0.0.1:3425.
func URL() string { return "http://" + Addr() }

// Running reports whether a gateway answers at the address.
func Running() bool {
	c := &http.Client{Timeout: 700 * time.Millisecond}
	res, err := c.Get(URL() + "/")
	if err != nil {
		return false
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return bytes.Contains(b, []byte(`"magpie"`))
}

// Call is one request the gateway handled, for the status views.
type Call struct {
	Time              time.Time         `json:"time"`
	Agent             string            `json:"agent"` // who called, from the client's User-Agent
	Model             string            `json:"model"`
	Provider          string            `json:"provider"`
	Key               string            `json:"key,omitempty"`      // the key that answered: its name, else its fingerprint
	Fallback          string            `json:"fallback,omitempty"` // candidates that failed first, e.g. "relay (team)"
	From              provider.Protocol `json:"from"`
	To                provider.Protocol `json:"to"`
	Status            int               `json:"status"`
	Millis            int64             `json:"ms"`
	Error             string            `json:"error,omitempty"`
	Usage             Usage             `json:"usage"`
	RequestBody       string            `json:"requestBody,omitempty"`
	ResponseBody      string            `json:"responseBody,omitempty"`
	RequestTruncated  bool              `json:"requestTruncated,omitempty"`
	ResponseTruncated bool              `json:"responseTruncated,omitempty"`
}

// Server is the gateway.
type Server struct {
	client *http.Client
	mu     sync.Mutex
	recent []Call
	// responseOnly remembers models rejected by Chat Completions, avoiding a
	// known-failing probe on every Claude Code turn.
	responseOnly map[string]bool
	// keyPref is the key of each provider that answered last; keyRest holds
	// when each key last failed with a quota or rate-limit reply.
	keyPref      map[string]string
	keyRest      map[string]time.Time
	subscription *subscriptionBridge
	debug        bool
}

// keyRestFor is how long a key that ran out or was rate limited is passed over.
const keyRestFor = time.Minute

// New makes a gateway.
func New() *Server {
	return &Server{
		client: &http.Client{Transport: &http.Transport{
			Proxy:                 netproxy.Func,
			ResponseHeaderTimeout: 10 * time.Minute,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
		}},
		responseOnly: make(map[string]bool),
		keyPref:      make(map[string]string),
		keyRest:      make(map[string]time.Time),
		subscription: newSubscriptionBridge(),
		debug:        os.Getenv("MAGPIE_DEBUG") != "",
	}
}

// Recent lists the last calls, newest first.
func (s *Server) Recent() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Call, len(s.recent))
	for i, c := range s.recent {
		out[len(s.recent)-1-i] = c
	}
	return out
}

func (s *Server) record(c Call) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recent = append(s.recent, c)
	if len(s.recent) > 40 {
		s.recent = s.recent[len(s.recent)-40:]
	}
	if s.debug {
		log.Printf("%s %s → %s (%s→%s) %d %dms %s", c.Model, c.Provider, c.Provider, c.From, c.To, c.Status, c.Millis, c.Error)
	}
}

// ListenAndServe runs the gateway until ctx ends. A bind error means
// another magpie is already serving, which is fine for the caller to ignore.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", Addr())
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 5 * time.Minute}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler routes the client APIs.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.info)
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("GET /models", s.models)
	mux.HandleFunc("GET /v1/models/{id}", s.model)
	mux.HandleFunc("POST /v1/chat/completions", s.handle(provider.Chat))
	mux.HandleFunc("POST /chat/completions", s.handle(provider.Chat))
	mux.HandleFunc("POST /v1/responses", s.handle(provider.Responses))
	mux.HandleFunc("POST /responses", s.handle(provider.Responses))
	mux.HandleFunc("POST /v1/messages", s.handle(provider.Anthropic))
	mux.HandleFunc("POST /messages", s.handle(provider.Anthropic))
	mux.HandleFunc("POST /v1/messages/count_tokens", s.countTokens)
	mux.HandleFunc("POST /_magpie/claude-mcp/{token}", s.subscription.mcpCall)
	mux.HandleFunc("GET /v1beta/models", s.geminiModels)
	mux.HandleFunc("POST /v1beta/models/{call...}", s.gemini)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, provider.Chat, http.StatusNotFound, "magpie serves /v1/chat/completions, /v1/responses, /v1/messages and /v1beta/models/*")
	})
	return mux
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"name": "magpie", "version": Version, "models": len(provider.Catalog()),
		"apis": []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1beta/models/{model}:generateContent"}})
}

func modelObject(e provider.Entry) map[string]any {
	return map[string]any{"id": e.ID, "object": "model", "type": "model", "created": 0, "created_at": "2025-01-01T00:00:00Z",
		"owned_by": e.Provider.ID, "display_name": e.Name}
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	data := []map[string]any{}
	for _, e := range provider.Catalog() {
		data = append(data, modelObject(e))
	}
	out := map[string]any{"object": "list", "data": data, "has_more": false}
	if len(data) > 0 {
		out["first_id"], out["last_id"] = data[0]["id"], data[len(data)-1]["id"]
	}
	writeJSON(w, 200, out)
}

func (s *Server) model(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeJSON(w, 200, modelObject(e))
			return
		}
	}
	writeError(w, provider.Chat, 404, "unknown model "+id)
}

// countTokens answers Anthropic's count_tokens: through the provider when
// it speaks Anthropic, else a rough estimate.
func (s *Server) countTokens(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Anthropic, 400, err.Error())
		return
	}
	p, model, ok := provider.Resolve(modelOf(body))
	// Claude Subscription generations run through the Claude Code binary. Its
	// OAuth token must not take a direct HTTP side path just for token counting.
	if ok && p.Account != nil && p.Account.Agent == "claude" {
		req, err := parseAnthropic(body)
		if err != nil {
			writeError(w, provider.Anthropic, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"input_tokens": estimate(req)})
		return
	}
	// Keys limited to another protocol (OpenAI's, say) never count tokens.
	if a, found := anthropicAttempt(p, model); ok && found {
		res, err := s.forward(r.Context(), a.Provider, provider.Anthropic, "/v1/messages/count_tokens", rewriteModel(body, model), r.Header)
		if err == nil {
			defer res.Body.Close()
			if res.StatusCode < 400 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(res.StatusCode)
				io.Copy(w, res.Body)
				return
			}
		}
	}
	req, err := parseAnthropic(body)
	if err != nil {
		writeError(w, provider.Anthropic, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"input_tokens": estimate(req)})
}

// handle is the request path of one client API.
func (s *Server) handle(from provider.Protocol) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if err != nil {
			writeError(w, from, 400, err.Error())
			return
		}
		s.serve(w, r, from, body)
	}
}

// gemini serves /v1beta/models/{model}:{method}. The Gemini API keeps the
// model and the streaming choice in the URL; they move into the body so
// serve sees one shape.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	call := r.PathValue("call")
	i := strings.LastIndex(call, ":") // model ids may hold a colon (ollama tags), methods never do
	if i < 0 {
		writeError(w, provider.Gemini, 404, "expected /v1beta/models/{model}:generateContent")
		return
	}
	model, method := call[:i], call[i+1:]
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	var stream bool
	switch method {
	case "generateContent":
	case "streamGenerateContent":
		stream = true
	case "countTokens":
		s.geminiCount(w, model, body)
		return
	default:
		writeError(w, provider.Gemini, 404, "unknown method "+method)
		return
	}
	s.serve(w, r, provider.Gemini, withFields(body, map[string]any{"model": model, "stream": stream}))
}

// geminiCount answers countTokens with a rough estimate.
func (s *Server) geminiCount(w http.ResponseWriter, model string, body []byte) {
	var wrap struct {
		Inner json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(wrap.Inner) > 0 {
		body = wrap.Inner
	}
	req, err := parseGemini(body)
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"totalTokens": estimate(req)})
}

func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, geminiModel(e.ID, e.Name))
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// estimate is a token count from sizes, for clients that ask before sending.
func estimate(req *Request) int {
	n := len(req.System)
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Name)
		}
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	return n / 4
}

// serve routes one parsed-enough request to its provider.
func (s *Server) serve(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte) {
	start := time.Now()
	requestBody, requestTruncated := captureRequestBody(body)
	capture := &captureResponseWriter{ResponseWriter: w}
	w = capture
	call := Call{Time: start, From: from, Model: modelOf(body), Agent: usage.AgentOf(r.Header.Get("User-Agent")),
		RequestBody: requestBody, RequestTruncated: requestTruncated}
	finishCapture := func() {
		call.ResponseBody = capture.body.text()
		call.ResponseTruncated = capture.body.truncated
	}
	p, model, ok := provider.Resolve(call.Model)
	if !ok {
		call.Status, call.Error = 404, "unknown model"
		msg := fmt.Sprintf("magpie knows no model %q", call.Model)
		if ids := provider.IDs(); len(ids) > 0 {
			msg += "; it has " + strings.Join(ids, ", ")
		} else {
			msg += "; add a provider in magpie first"
		}
		writeError(w, from, 404, msg)
		finishCapture()
		s.record(call)
		return
	}
	call.Provider = p.ID
	// A Claude Code subscription must run through the genuine binary. Direct
	// OAuth HTTP requests are content-classified as third-party traffic when
	// they carry another agent's harness (Pi, OpenCode, and others).
	if p.Account != nil && p.Account.Agent == "claude" {
		call.To = provider.Anthropic
		call.Status, call.Error = s.serveClaudeSubscription(w, r, from, model, body, &call.Usage)
		call.Millis = time.Since(start).Milliseconds()
		finishCapture()
		s.record(call)
		usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: model,
			Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
			CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
		return
	}
	cands := s.candidates(p, from, model, body)
	if len(cands) == 0 {
		call.Status, call.Error = 502, p.Name+" has no endpoint configured"
		writeError(w, from, call.Status, call.Error)
	} else {
		used := s.tryCandidates(w, r, cands, from, body, &call)
		p.ID, model = used.a.Provider.ID, used.model
	}
	call.Millis = time.Since(start).Milliseconds()
	finishCapture()
	s.record(call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: model,
		Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
		CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
}

// candidate is one way to send a request: a provider's key to one of its
// endpoints, under the model name that provider knows it by.
type candidate struct {
	a     provider.Attempt
	model string
	relay bool // the endpoint is the client's own: send the body as it is
}

// candidates lists every way to answer the request, best first: the
// provider's own attempts (its enabled keys, by protocol preference), then
// the attempts of each fallback "provider/model" in the order the user gave.
// Fallback entries that do not resolve are skipped without a word.
func (s *Server) candidates(p provider.Provider, from provider.Protocol, model string, body []byte) []candidate {
	out := s.providerCandidates(p, from, model, body)
	for _, ref := range p.Fallback {
		fp, fmodel, ok := provider.Resolve(ref)
		if !ok {
			continue
		}
		out = append(out, s.providerCandidates(fp, from, fmodel, body)...)
	}
	return out
}

// providerCandidates lists the candidates one provider offers for the model.
func (s *Server) providerCandidates(p provider.Provider, from provider.Protocol, model string, body []byte) []candidate {
	if p.Account == nil {
		var out []candidate
		for _, a := range s.orderAttempts(p.ID, p.Attempts(from, model)) {
			out = append(out, candidate{a: a, model: model, relay: a.To == from})
		}
		return out
	}
	// a signed-in agent has one way in, per account. A backend that only
	// streams gets a non-streaming request translated (the provider is always
	// streamed on that path) rather than relayed. Accounts that are on are
	// tried in turn: the one signed in first, then the others.
	var out []candidate
	for _, acc := range provider.OnAccounts(p) {
		relay := acc.Base(from) != "" && (!acc.Account.Stream || streamOf(body))
		to := from
		if !relay {
			speaks := acc.Speaks()
			if len(speaks) == 0 {
				continue
			}
			to = speaks[0]
		}
		a := provider.Attempt{Provider: acc, To: to, KeyRef: provider.AccountRef(acc.Account.User), KeyName: acc.Account.User}
		out = append(out, candidate{a: a, model: model, relay: relay})
	}
	return out
}

// tryCandidates sends the request down the candidates in turn. A candidate
// that is out of quota, rate limited, overloaded or unreachable hands the
// request to the next one, and rests for keyRestFor, during which the others
// are tried first. Nothing reaches the client until a candidate answers or
// the last one fails. It returns the candidate that did the final attempt.
func (s *Server) tryCandidates(w http.ResponseWriter, r *http.Request, cands []candidate, from provider.Protocol, body []byte, call *Call) candidate {
	cands = s.restingLast(cands)
	var failed []string
	var last candidate
	for i, c := range cands {
		more := i < len(cands)-1
		last = c
		var retry bool
		a := c.a
		call.Provider, call.To = a.Provider.ID, a.To
		if c.relay {
			call.Status, call.Error, retry = s.passthrough(w, r, a, from, c.model, body, &call.Usage, more)
		} else {
			call.Status, call.Error, retry = s.translate(w, r, a, from, a.To, c.model, body, &call.Usage, more)
		}
		if retry {
			s.restKey(a.Provider.ID, a.KeyRef)
			failed = append(failed, failedLabel(a.Provider.ID, a))
			continue
		}
		call.Key = a.KeyName
		if call.Key == "" {
			call.Key = a.KeyRef
		}
		if len(failed) > 0 {
			call.Fallback = strings.Join(failed, ", ")
		}
		if call.Status < 400 && a.KeyRef != "" {
			s.preferKey(a.Provider.ID, a.KeyRef)
		}
		return c
	}
	return last
}

// restingLast moves the candidates that failed within keyRestFor behind the
// rest. Order is otherwise kept, so a provider's keys stay ahead of its
// fallbacks.
func (s *Server) restingLast(cands []candidate) []candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var fresh, resting []candidate
	for _, c := range cands {
		if t, ok := s.keyRest[c.a.Provider.ID+"\x00"+c.a.KeyRef]; ok && now.Sub(t) < keyRestFor {
			resting = append(resting, c)
		} else {
			fresh = append(fresh, c)
		}
	}
	return append(fresh, resting...)
}

// failedLabel names a candidate in a call record: the provider, and the key's
// name when it has one.
func failedLabel(id string, a provider.Attempt) string {
	if a.KeyName == "" {
		return id
	}
	return id + " (" + a.KeyName + ")"
}

// orderAttempts keeps the provider's own order (rank first), but within a
// rank puts the key that answered last first, and keys that failed within
// keyRestFor after the others. Resting keys are still tried, just last.
func (s *Server) orderAttempts(id string, attempts []provider.Attempt) []provider.Attempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	pref := s.keyPref[id]
	resting := func(ref string) bool {
		t, ok := s.keyRest[id+"\x00"+ref]
		return ok && now.Sub(t) < keyRestFor
	}
	sort.SliceStable(attempts, func(i, j int) bool {
		a, b := attempts[i], attempts[j]
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		if ra, rb := resting(a.KeyRef), resting(b.KeyRef); ra != rb {
			return !ra
		}
		return a.KeyRef == pref && b.KeyRef != pref
	})
	return attempts
}

func (s *Server) preferKey(id, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keyPref[id] = ref
}

func (s *Server) restKey(id, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keyRest[id+"\x00"+ref] = time.Now()
}

// fallbackOn reports whether a failed reply says this candidate cannot serve
// the request right now (out of quota, rate limited, overloaded or down), so
// another candidate may. A bad request is the client's fault and is not
// retried anywhere.
func fallbackOn(status int, body []byte) bool {
	switch {
	case status == http.StatusTooManyRequests, status == http.StatusPaymentRequired:
		return true
	case status >= 500:
		return true // 500, 502, 503, 504 and Anthropic's 529 overloaded
	case status == http.StatusForbidden:
		msg := strings.ToLower(string(body))
		for _, phrase := range []string{"quota", "insufficient", "balance", "credit", "exhaust", "exceed", "额度", "余额"} {
			if strings.Contains(msg, phrase) {
				return true
			}
		}
	}
	return false
}

// anthropicAttempt is the first way to reach the provider's Anthropic endpoint
// for the model. Keys limited to another protocol are never chosen.
func anthropicAttempt(p provider.Provider, model string) (provider.Attempt, bool) {
	for _, a := range p.Attempts(provider.Anthropic, model) {
		if a.To == provider.Anthropic {
			return a, true
		}
	}
	return provider.Attempt{}, false
}

// forward sends a request to the provider.
func (s *Server) forward(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Base(to)+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", "magpie/"+Version)
	if to == provider.Anthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
		for _, h := range []string{"anthropic-version", "anthropic-beta"} {
			if v := in.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
	}
	if p.IsOpenCode() {
		req.Header.Set("x-opencode-session", conversationID(in, body))
	}
	if err := p.Sign(ctx, req, to, body); err != nil {
		return nil, err
	}
	return s.client.Do(req)
}

// passthrough relays a request the endpoint understands as-is, with the
// model name swapped for the candidate's own. The token counts the reply
// carries are read on the way past into u. When more is set and the attempt
// fails in a way another candidate may survive (see fallbackOn), nothing is
// written and retry is true. That covers a failed connection and, for a
// stream, an error that comes before its first event.
func (s *Server) passthrough(w http.ResponseWriter, r *http.Request, a provider.Attempt, proto provider.Protocol, model string, body []byte, u *Usage, more bool) (status int, msg string, retry bool) {
	p := a.Provider
	body = rewriteModel(body, model)
	if proto == provider.Chat {
		body = developerAsSystem(body)
	}
	res, err := s.forward(r.Context(), p, proto, pathOf(proto), p.Prepare(body), r.Header)
	if err != nil {
		if more && r.Context().Err() == nil {
			return 502, p.Name + ": " + err.Error(), true
		}
		return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), false
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if more && fallbackOn(res.StatusCode, b) {
			return res.StatusCode, p.Name + ": " + provider.APIError(b, res.Status), true
		}
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, proto, res.StatusCode, msg), msg, false
	}
	rd, sse := eventStream(res)
	if sse && more {
		// an error before the first event means the stream never really began
		br := bufio.NewReaderSize(rd, 4<<10)
		head := firstEvent(br)
		if errorEvent(head) {
			return res.StatusCode, p.Name + ": " + strings.TrimSpace(string(head)), true
		}
		rd = io.MultiReader(bytes.NewReader(head), br)
	}
	h := w.Header()
	for _, k := range []string{"Content-Type", "Request-Id", "X-Request-Id"} {
		if v := res.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	if sse {
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(res.StatusCode)
	f, _ := w.(http.Flusher)
	sniff := newSniffer(proto, res.Header.Get("Content-Type"))
	defer func() { u.add(sniff.usage()) }()
	buf := make([]byte, 32<<10)
	for {
		n, err := rd.Read(buf)
		if n > 0 {
			sniff.write(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				return res.StatusCode, "", false
			}
			if f != nil {
				f.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	return res.StatusCode, "", false
}

// eventStream reports whether a reply is server-sent events. The header
// says so for most vendors; the ChatGPT backend sends none, so the body's
// first bytes decide, and the header is filled in for whoever reads it.
func eventStream(res *http.Response) (io.Reader, bool) {
	ct := res.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		return res.Body, true
	}
	if ct != "" && !strings.HasPrefix(ct, "text/plain") {
		return res.Body, false
	}
	br := bufio.NewReaderSize(res.Body, 4<<10)
	head, _ := br.Peek(16)
	head = bytes.TrimLeft(head, " \t\r\n")
	for _, pfx := range []string{"event:", "data:", ":"} {
		if bytes.HasPrefix(head, []byte(pfx)) {
			res.Header.Set("Content-Type", "text/event-stream")
			return br, true
		}
	}
	return br, false
}

// firstEvent reads the first server-sent event of a reply, through its blank
// line, and returns the bytes it read. Leading blank lines are skipped.
func firstEvent(br *bufio.Reader) []byte {
	var head []byte
	for len(head) < 64<<10 {
		line, err := br.ReadBytes('\n')
		head = append(head, line...)
		if err != nil {
			break
		}
		if len(bytes.TrimSpace(line)) == 0 && len(bytes.TrimSpace(head)) > 0 {
			break
		}
	}
	return head
}

// errorEvent reports whether an event is an error: Anthropic's and
// OpenAI's error events, a Chat chunk that carries an "error" object, or a
// Responses response.failed.
func errorEvent(head []byte) bool {
	for _, line := range bytes.Split(head, []byte("\n")) {
		line = bytes.TrimSpace(line)
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			switch strings.TrimSpace(string(line[len("event:"):])) {
			case "error", "response.failed":
				return true
			}
		case bytes.HasPrefix(line, []byte("data:")):
			var v map[string]json.RawMessage
			if json.Unmarshal(bytes.TrimSpace(line[len("data:"):]), &v) != nil {
				continue
			}
			if _, ok := v["error"]; ok {
				return true
			}
			var typ string
			json.Unmarshal(v["type"], &typ)
			if typ == "error" || typ == "response.failed" {
				return true
			}
		}
	}
	return false
}

func (s *Server) responseOnlyModel(providerID, model string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.responseOnly[providerID+"\x00"+model]
}

func (s *Server) markResponseOnly(providerID, model string) {
	s.mu.Lock()
	s.responseOnly[providerID+"\x00"+model] = true
	s.mu.Unlock()
}

// forwardTranslated sends one translated, streaming request upstream. Chat is
// preferred by most OpenAI-compatible providers, but some models are exposed
// only by the Responses endpoint; retry that endpoint before failing, like
// Alma's Claude Code provider proxy does. A key limited to its protocol
// (pinned) never leaves that protocol's endpoint, so it skips both moves.
func (s *Server) forwardTranslated(ctx context.Context, p provider.Provider, to provider.Protocol, req *Request, model string, in http.Header, pinned bool) (*http.Response, provider.Protocol, error) {
	send := func(proto provider.Protocol) (*http.Response, error) {
		body := build(proto, req, model, p.Host(), p.RejectsTemperature(model))
		return s.forward(ctx, p, proto, pathOf(proto), p.Prepare(body), in)
	}
	if !pinned && to == provider.Chat && p.Responses != "" && s.responseOnlyModel(p.ID, model) {
		to = provider.Responses
	}
	res, err := send(to)
	if err != nil {
		return nil, to, err
	}
	if pinned || to != provider.Chat || p.Responses == "" || res.StatusCode < 400 {
		return res, to, nil
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	if !notChatModel(res.StatusCode, b) {
		res.Body = io.NopCloser(bytes.NewReader(b))
		return res, to, nil
	}
	s.markResponseOnly(p.ID, model)
	res, err = send(provider.Responses)
	return res, provider.Responses, err
}

// notChatModel recognizes the errors used by OpenAI-compatible servers when a
// model can only be called through /responses.
func notChatModel(status int, body []byte) bool {
	if status < 400 {
		return false
	}
	msg := strings.ToLower(string(body))
	for _, phrase := range []string{
		"not a chat model",
		"not supported in the v1/chat/completions",
		"not supported in /v1/chat/completions",
		"use v1/completions",
		"use /v1/completions",
		"use v1/responses",
		"use /v1/responses",
	} {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// translate serves a client API the provider lacks by speaking another
// one to it. The provider is always streamed; the client gets whichever
// it asked for.
func (s *Server) translate(w http.ResponseWriter, r *http.Request, a provider.Attempt, from provider.Protocol, to provider.Protocol, model string, body []byte, u *Usage, more bool) (status int, msg string, retry bool) {
	p := a.Provider
	request, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error(), false
	}
	stream := request.Stream
	request.Stream = true
	res, actual, err := s.forwardTranslated(r.Context(), p, to, request, model, r.Header, a.Limited)
	if err != nil {
		if more && r.Context().Err() == nil {
			return 502, p.Name + ": " + err.Error(), true
		}
		return writeError(w, from, 502, p.Name+": "+err.Error()), err.Error(), false
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if more && fallbackOn(res.StatusCode, b) {
			return res.StatusCode, p.Name + ": " + provider.APIError(b, res.Status), true
		}
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, from, res.StatusCode, msg), msg, false
	}
	dec := decoder(actual)
	rd, sse := eventStream(res)
	if !sse {
		// the provider ignored stream:true; read the whole reply as one
		// event stream would be wrong, so give up cleanly
		b, _ := io.ReadAll(io.LimitReader(rd, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeError(w, from, 502, msg), msg, false
	}
	if stream {
		// Start and usage events are held until the first real event shows
		// the reply is good: an error before any output may still move on.
		enc := encoder(from, newSSEWriter(w), request.Model)
		var held []Event
		var failed, early string
		begun := false
		emit := func(ev Event) {
			switch ev.Kind {
			case KError:
				failed = ev.Text
			case KStart, KUsage:
				u.add(ev.Usage)
			}
			enc.event(ev)
		}
		begin := func() {
			begun = true
			for _, ev := range held {
				emit(ev)
			}
			held = nil
		}
		errStop := errors.New("early error")
		readSSE(rd, func(_, data string) error {
			dec(data, func(ev Event) {
				if early != "" {
					return
				}
				if begun {
					emit(ev)
					return
				}
				switch {
				case ev.Kind == KStart || ev.Kind == KUsage:
					held = append(held, ev)
					return
				case ev.Kind == KError && more:
					early = ev.Text
					return
				}
				begin()
				emit(ev)
			})
			if early != "" {
				return errStop
			}
			return nil
		})
		if early != "" {
			msg := p.Name + ": " + early
			return 502, msg, true
		}
		if !begun {
			begin()
		}
		enc.finish()
		return 200, failed, false
	}
	var col collector
	readSSE(rd, func(_, data string) error {
		return dec(data, col.add)
	})
	if col.err != "" && len(col.res.Parts) == 0 {
		if more {
			return 502, p.Name + ": " + col.err, true
		}
		return writeError(w, from, 502, p.Name+": "+col.err), col.err, false
	}
	res2 := col.finish()
	u.add(res2.Usage)
	out := render(from, res2, request.Model)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(out)
	return 200, col.err, false
}

// ---- protocol tables --------------------------------------------------------

func pathOf(proto provider.Protocol) string {
	switch proto {
	case provider.Chat:
		return "/chat/completions"
	case provider.Responses:
		return "/responses"
	}
	return "/v1/messages"
}

func parse(proto provider.Protocol, body []byte) (*Request, error) {
	switch proto {
	case provider.Chat:
		return parseChat(body)
	case provider.Responses:
		return parseResponses(body)
	case provider.Gemini:
		return parseGemini(body)
	}
	return parseAnthropic(body)
}

func build(proto provider.Protocol, r *Request, model, host string, rejectTemp bool) []byte {
	switch proto {
	case provider.Chat:
		return buildChat(r, model, host, rejectTemp)
	case provider.Responses:
		return buildResponses(r, model, rejectTemp)
	}
	return buildAnthropic(r, model)
}

func decoder(proto provider.Protocol) func(data string, emit func(Event)) error {
	switch proto {
	case provider.Chat:
		d := &chatDecoder{}
		return d.decode
	case provider.Responses:
		d := &responsesDecoder{}
		return d.decode
	}
	return decodeAnthropic
}

type streamEncoder interface {
	event(Event)
	finish()
}

func encoder(proto provider.Protocol, w *sseWriter, model string) streamEncoder {
	switch proto {
	case provider.Chat:
		return &chatEncoder{w: w, model: model}
	case provider.Responses:
		return &responsesEncoder{w: w, model: model}
	case provider.Gemini:
		return &geminiEncoder{w: w, model: model}
	}
	return &anthropicEncoder{w: w, model: model}
}

func render(proto provider.Protocol, res Result, model string) []byte {
	switch proto {
	case provider.Chat:
		return renderChat(res, model)
	case provider.Responses:
		return renderResponses(res, model)
	case provider.Gemini:
		return renderGemini(res, model)
	}
	return renderAnthropic(res, model)
}

// ---- small helpers ------------------------------------------------------------

func streamOf(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &v)
	return v.Stream
}

func modelOf(body []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &v)
	return v.Model
}

// rewriteModel swaps the model field, keeping every other field as it was.
func rewriteModel(body []byte, model string) []byte {
	return withFields(body, map[string]any{"model": model})
}

// developerAsSystem turns "developer" messages into "system" ones. Agents
// such as Pi send the developer role to reasoning models, which OpenAI
// accepts, but other Chat Completions backends (DeepSeek among them) reject
// the whole request; every backend accepts system, OpenAI included.
func developerAsSystem(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"developer"`)) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return body
	}
	msgs, _ := m["messages"].([]any)
	changed := false
	for _, v := range msgs {
		if msg, ok := v.(map[string]any); ok && msg["role"] == "developer" {
			msg["role"] = "system"
			changed = true
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// sessionHeaders are where agents already name their conversation: OpenCode
// (to its own gateway, and to everyone else), Pi, Codex and Claude Code.
var sessionHeaders = []string{
	"x-opencode-session", "x-session-affinity", "x-session-id",
	"session_id", "session-id", "x-claude-code-session-id",
}

// conversationID is a stable id for the conversation a request belongs to.
// It is the agent's own session id when it sends one; otherwise it is derived
// from the conversation's first user message, which every later turn repeats.
func conversationID(in http.Header, body []byte) string {
	for _, h := range sessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	var m struct {
		Messages []json.RawMessage `json:"messages"`
		Input    json.RawMessage   `json:"input"`
	}
	// An undecodable body still gets an id: the hash of the whole body.
	_ = json.Unmarshal(body, &m)
	items := m.Messages
	if len(items) == 0 && len(m.Input) > 0 && m.Input[0] == '[' {
		// A malformed input array leaves items empty, and the whole body is hashed.
		_ = json.Unmarshal(m.Input, &items)
	}
	first := []byte(m.Input)
	if len(items) > 0 {
		first = items[0]
	}
	for _, it := range items {
		var r struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(it, &r) == nil && r.Role == "user" {
			first = it
			break
		}
	}
	if len(first) == 0 {
		first = body
	}
	sum := sha256.Sum256(first)
	return "magpie-" + hex.EncodeToString(sum[:12])
}

// withFields sets top-level fields, keeping every other field as it was.
func withFields(body []byte, fields map[string]any) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return body
	}
	for k, v := range fields {
		m[k] = v
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError answers in the client's own error shape.
func writeError(w http.ResponseWriter, proto provider.Protocol, status int, msg string) int {
	typ := "api_error"
	switch {
	case status == 400:
		typ = "invalid_request_error"
	case status == 401:
		typ = "authentication_error"
	case status == 403:
		typ = "permission_error"
	case status == 404:
		typ = "not_found_error"
	case status == 429:
		typ = "rate_limit_error"
	case status == 529:
		typ = "overloaded_error"
	}
	var v any
	switch proto {
	case provider.Anthropic:
		v = map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}}
	case provider.Gemini:
		st := map[int]string{400: "INVALID_ARGUMENT", 401: "UNAUTHENTICATED", 403: "PERMISSION_DENIED", 404: "NOT_FOUND",
			429: "RESOURCE_EXHAUSTED", 500: "INTERNAL", 502: "UNAVAILABLE", 503: "UNAVAILABLE", 529: "UNAVAILABLE"}[status]
		if st == "" {
			st = "UNKNOWN"
		}
		v = map[string]any{"error": map[string]any{"code": status, "message": msg, "status": st}}
	default:
		v = map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": nil, "param": nil}}
	}
	writeJSON(w, status, v)
	return status
}
