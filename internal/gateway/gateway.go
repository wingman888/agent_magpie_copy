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
	"slices"
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
	From              provider.Protocol `json:"from"`
	To                provider.Protocol `json:"to"`
	Status            int               `json:"status"`
	Millis            int64             `json:"ms"`
	Error             string            `json:"error,omitempty"`
	Fallback          string            `json:"fallback,omitempty"` // providers that failed first, and why
	Retries           int               `json:"retries,omitempty"`  // times the last one left was tried again
	Canceled          bool              `json:"canceled,omitempty"` // the agent went away before the answer
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
	// unserved remembers the endpoints a provider turned a model away from
	// as not served there, by provider, model and endpoint, sparing every
	// later request a known-failing try.
	unserved     map[string]bool
	subscription *subscriptionBridge
	debug        bool
	trace        trace // what routing did with each request, for the Gateway view
}

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
		unserved:     make(map[string]bool),
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
	mux.HandleFunc(CodexPath+"/", s.codexBackend)
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
	if ok && p.Account != nil && (p.Account.Agent == "claude" || p.Account.Agent == "cursor" || p.Account.Agent == "grok") {
		req, err := parseAnthropic(body)
		if err != nil {
			writeError(w, provider.Anthropic, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"input_tokens": estimate(req)})
		return
	}
	if ok {
		// a relay's OpenAI-only key can't count Anthropic tokens, and a model
		// not served on the Anthropic endpoint can't be counted there
		q := p
		q.Anthropic = ""
		for _, c := range perKey(p, model, provider.Anthropic) {
			if c.p.Anthropic != "" && s.servesOn(c.p, model, provider.Anthropic) {
				q = c.p
				break
			}
		}
		p = q
	}
	if ok && p.Anthropic != "" {
		res, err := s.forward(r.Context(), p, provider.Anthropic, "/v1/messages/count_tokens", rewriteModel(body, model), r.Header)
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
	// the primary, then its fallbacks while it can't take the request and
	// nothing has been sent yet
	var cands []candidate
	var pl planned
	var group *GroupRef
	// a conversation's affinity is to one model: another's cache is its
	// own, and an agent's side requests (a title, a summary) to a smaller
	// model leave the conversation where it is
	scope, mode, rotate := p.ID+"/"+model, p.Affinity, p.Routing == provider.Rotate
	if g, ms, ok := provider.FindGroup(call.Model); ok {
		// a routing group: every member's keys or accounts weighed together
		cands, pl = s.planGroup(g, ms, from)
		group = &GroupRef{ID: g.ID, Name: g.Name, Routing: g.Routing, Affinity: g.Affinity, Auto: g.Auto}
		for _, m := range ms {
			group.Members = append(group.Members, m.Provider.ID+"/"+m.Model)
		}
		scope, mode, rotate = provider.GroupPrefix+g.ID, g.Affinity, g.Routing == provider.Rotate
	} else {
		cands, pl = s.plan(p, model, from)
	}
	if len(cands) == 0 {
		call.Status, call.Error = 404, "no member ready"
		writeError(w, from, 404, fmt.Sprintf("none of %s's models is ready", call.Model))
		finishCapture()
		s.record(call)
		return
	}
	// the conversation stays with who answered it last, while its
	// affinity says: what the vendor cached of it is read again
	var aff *Affinity
	var stuck string
	if len(cands) > 1 {
		cands, pl, aff, stuck = affine(scope, mode, rotate, r.Header, from, body, cands, pl)
	}
	tr := s.trace.begin(Route{Time: start, Agent: call.Agent, Model: call.Model, Provider: p.ID, Group: group, Affinity: aff, Order: pl.order, Left: pl.left})
	var skipped []string
	// cancel: the agent went away; nobody is to blame, nobody rests, and
	// nobody else is asked
	cancel := func() {
		call.Status, call.Error, call.Canceled = 499, "canceled", true
		s.trace.update(tr, func(t *Route) {
			t.Canceled = true
			if n := len(t.Tries); n > 0 {
				lt := &t.Tries[n-1]
				lt.Done, lt.Status, lt.Error, lt.Fail, lt.Then, lt.Rest = true, 499, "canceled", failCanceled, thenCanceled, nil
				if lt.Millis == 0 {
					lt.Millis = time.Since(lt.Start).Milliseconds()
				}
			}
		})
	}
	done := false
	for i := 0; i < len(cands) && !done; i++ {
		c := cands[i]
		last := i == len(cands)-1
		// the last one left may be tried again when its failure may clear
		// up; any other hands the request on instead
		for n := 0; ; n++ {
			retry := last && n < maxRetries
			hw := newHoldWriter(w, from, !last || retry)
			call.Provider, call.To, call.Usage = c.p.ID, "", Usage{}
			began := time.Now()
			s.trace.update(tr, func(t *Route) { t.Tries = append(t.Tries, Try{ID: c.rest, Start: began, Retry: n}) })
			call.Status, call.Error = s.attempt(hw, r, from, c.p, c.model, body, &call)
			hw.settle()
			model = c.model
			if r.Context().Err() != nil {
				cancel()
				done = true
				break
			}
			if st, msg, ok := hw.streamFailed(); ok {
				call.Status, call.Error = st, c.p.Name+": "+msg
			}
			try := Try{ID: c.rest, Start: began, Retry: n, Done: true, Status: call.Status, Millis: time.Since(began).Milliseconds(), Error: call.Error}
			setTry := func() { s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try }) }
			st, held, failed := hw.heldFailure()
			if failed && !last && retryable(st, held) {
				rest := s.restAfter(c, st, hw.header, held)
				try.Fail, try.Rest, try.Then = rest.Why, &rest, thenFallback
				setTry()
				skipped = append(skipped, c.label()+": "+call.Error)
				break // on to the next candidate
			}
			if failed && retry {
				if d, ok := retryDelay(st, hw.header, n); ok {
					try.Fail, try.Then, try.Wait = failure(st, held), thenRetry, d.Milliseconds()
					setTry()
					call.Retries++
					if retrySleep(r.Context(), d) != nil {
						cancel()
						done = true
						break
					}
					continue
				}
			}
			hw.release()
			if call.Status < 400 {
				clearProviderRest(c.rest) // it answered: whatever it rested for is over
				served(c.rest, call.Usage.Input+call.Usage.Output+call.Usage.CacheRead+call.Usage.CacheWrite)
				if aff != nil {
					answered(stuck, c.rest, aff.Turn, call.Usage.CacheRead)
				}
			} else {
				try.Fail = failure(call.Status, []byte(call.Error))
			}
			setTry()
			done = true
			break
		}
	}
	if len(skipped) > 0 {
		call.Fallback = strings.Join(skipped, "; ")
	}
	call.Millis = time.Since(start).Milliseconds()
	finishCapture()
	s.trace.update(tr, func(t *Route) {
		t.Done, t.Status, t.Error, t.Millis = true, call.Status, call.Error, call.Millis
		t.Tokens = call.Usage.Input + call.Usage.Output + call.Usage.CacheRead + call.Usage.CacheWrite
	})
	s.record(call)
	if call.To != "" {
		usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: call.Provider, Model: model,
			Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
			CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
	}
}

// attempt sends a request to one provider. call.To stays empty when the
// provider has no endpoint to send it to.
func (s *Server) attempt(w http.ResponseWriter, r *http.Request, from provider.Protocol, p provider.Provider, model string, body []byte, call *Call) (int, string) {
	// A Claude Code subscription must run through the genuine binary. Direct
	// OAuth HTTP requests are content-classified as third-party traffic when
	// they carry another agent's harness (Pi, OpenCode, and others).
	if p.Account != nil && p.Account.Agent == "claude" {
		call.To = provider.Anthropic
		return s.serveClaudeSubscription(w, r, from, p, model, body, &call.Usage)
	}
	// Cursor's API belongs to its own clients: its CLI does the talking
	if p.Account != nil && p.Account.Agent == "cursor" {
		call.To = from
		start := func(ctx context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
			return s.subscription.startCursor(ctx, req, model)
		}
		return s.serveSubscription(w, r, from, "Cursor", model, body, &call.Usage, start)
	}
	// and so does Grok's
	if p.Account != nil && p.Account.Agent == "grok" {
		call.To = from
		start := func(ctx context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
			return s.subscription.startGrok(ctx, req, model, p.Account.Home)
		}
		return s.serveSubscription(w, r, from, "Grok", model, body, &call.Usage, start)
	}
	tos := s.endpoints(p, model, from)
	if len(tos) == 0 {
		msg := p.Name + " has no endpoint configured"
		return writeError(w, from, 502, msg), msg
	}
	// each endpoint the model may be served on in turn, while the provider
	// says it isn't served on the one tried and nothing has been written
	for i, to := range tos {
		more := i < len(tos)-1
		call.To = to
		var status int
		var msg string
		var moved bool
		// a backend that only streams gets a non-streaming request translated
		// (the provider is always streamed on that path) rather than relayed
		if to == from && (p.Account == nil || !p.Account.Stream || streamOf(body)) {
			status, msg, moved = s.passthrough(w, r, p, from, model, body, &call.Usage, more)
		} else {
			status, msg, moved = s.translate(w, r, p, from, to, model, body, &call.Usage, more)
		}
		if !moved {
			return status, msg
		}
	}
	// not reached: the last endpoint never moves on
	return call.Status, call.Error
}

// endpoints are the APIs a request for model goes to at p, in the order to
// try them. Those the provider's model list declares for the model, when it
// does, else every one it has an endpoint for; less those it turned the
// model away from before. The client's own goes first when it is among them,
// relayed as-is; the rest are translated to, in the order preferredOrder says.
func (s *Server) endpoints(p provider.Provider, model string, from provider.Protocol) []provider.Protocol {
	usable := s.usable(p, model)
	var out []provider.Protocol
	if slices.Contains(usable, from) {
		out = append(out, from)
	}
	for _, pr := range preferredOrder(p, model) {
		if pr != from && slices.Contains(usable, pr) {
			out = append(out, pr)
		}
	}
	return out
}

// usable is endpoints, unordered: what p speaks, narrowed to what its list
// declares for model, less those remembered as not serving it. When the
// narrowing would leave nothing, the wider list stands.
func (s *Server) usable(p provider.Provider, model string) []provider.Protocol {
	speaks := p.Speaks()
	if apis := p.APIs(model); len(apis) > 0 {
		var declared []provider.Protocol
		for _, pr := range speaks {
			if slices.Contains(apis, pr) {
				declared = append(declared, pr)
			}
		}
		if len(declared) > 0 {
			speaks = declared
		}
	}
	var left []provider.Protocol
	for _, pr := range speaks {
		if !s.isUnserved(p.ID, model, pr) {
			left = append(left, pr)
		}
	}
	if len(left) == 0 {
		return speaks
	}
	return left
}

// servesOn reports whether a request for model may go to p's endpoint for
// proto: p has one, the model list doesn't rule it out, and p hasn't turned
// the model away there.
func (s *Server) servesOn(p provider.Provider, model string, proto provider.Protocol) bool {
	if p.Base(proto) == "" || s.isUnserved(p.ID, model, proto) {
		return false
	}
	apis := p.APIs(model)
	return len(apis) == 0 || slices.Contains(apis, proto)
}

// preferredOrder is the order to translate in: OpenAI's own API, and
// OpenAI's models on Copilot, are best served by Responses; every other
// vendor by Chat Completions first.
func preferredOrder(p provider.Provider, model string) []provider.Protocol {
	if prefersResponses(p, model) {
		return []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	}
	return []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
}

func prefersResponses(p provider.Provider, model string) bool {
	for _, pr := range provider.Protocols {
		if u := p.Base(pr); u != "" && provider.HostOf(u) == "api.openai.com" {
			return true
		}
	}
	return p.Account != nil && p.Account.Agent == "copilot" && openAIModel(model)
}

// openAIModel is a model of OpenAI's by its name — gpt-*, codex*, o1, o4-mini
// and the like — a vendor/ prefix aside.
func openAIModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "codex") ||
		len(m) > 1 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9'
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

// passthrough relays a request the provider understands as-is, with the
// model name swapped for the provider's own. The token counts the reply
// carries are read on the way past into u.
//
// When the provider says the model isn't served on this endpoint and another
// is left to try (more), nothing is written and moved is true.
func (s *Server) passthrough(w http.ResponseWriter, r *http.Request, p provider.Provider, proto provider.Protocol, model string, body []byte, u *Usage, more bool) (status int, failed string, moved bool) {
	body = rewriteModel(body, model)
	if proto == provider.Chat {
		body = developerAsSystem(body)
	}
	res, err := s.forward(r.Context(), p, proto, pathOf(proto), p.Prepare(body), r.Header)
	if err != nil {
		return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), false
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if s.turnedAway(p, model, proto, res.StatusCode, b) && more {
			return 0, "", true
		}
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		keepRetry(w.Header(), res.Header)
		return writeError(w, proto, res.StatusCode, msg), msg, false
	}
	rd, sse := eventStream(res)
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

func unservedKey(providerID, model string, proto provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(proto)
}

func (s *Server) isUnserved(providerID, model string, proto provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unserved[unservedKey(providerID, model, proto)]
}

func (s *Server) markUnserved(providerID, model string, proto provider.Protocol) {
	s.mu.Lock()
	s.unserved[unservedKey(providerID, model, proto)] = true
	s.mu.Unlock()
}

// responseOnlyModel reports a model Chat Completions turned away, so only
// Responses is left for it (kept for the callers of the older name).
func (s *Server) responseOnlyModel(providerID, model string) bool {
	return s.isUnserved(providerID, model, provider.Chat)
}

// markResponseOnly remembers that Chat Completions turned a model away.
func (s *Server) markResponseOnly(providerID, model string) {
	s.markUnserved(providerID, model, provider.Chat)
}

// turnedAway reports, and remembers, a provider's answer that the model is
// not served on the endpoint the request went to.
func (s *Server) turnedAway(p provider.Provider, model string, proto provider.Protocol, status int, body []byte) bool {
	if !wrongEndpoint(status, body) {
		return false
	}
	s.markUnserved(p.ID, model, proto)
	return true
}

// wrongEndpointPhrases are how vendors say a model is served on another
// endpoint than the one asked: OpenAI, Copilot, and the servers that copy
// their wording.
var wrongEndpointPhrases = []string{
	"not a chat model",
	"not accessible via the",
	"unsupported_api_for_model",
	"not supported in the v1/chat/completions",
	"not supported in /v1/chat/completions",
	"not supported in the /v1/chat/completions",
	"not supported in the v1/responses",
	"not supported in /v1/responses",
	"not supported in the /v1/responses",
	"not supported in the v1/messages",
	"not supported in /v1/messages",
	"not supported with the responses api",
	"not supported with the chat completions api",
	"only supported in v1/responses",
	"only supported in /v1/responses",
	"only supported in v1/chat/completions",
	"only supported in /v1/chat/completions",
	"use v1/completions",
	"use /v1/completions",
	"use v1/responses",
	"use /v1/responses",
	"use v1/chat/completions",
	"use /v1/chat/completions",
}

// wrongEndpoint recognizes a 4xx saying the model isn't served on the
// endpoint asked, but may be on another. Any other status — a server error
// that happens to word it so, a success — says nothing of the kind.
func wrongEndpoint(status int, body []byte) bool {
	if status < 400 || status >= 500 {
		return false
	}
	msg := strings.ToLower(string(body))
	for _, phrase := range wrongEndpointPhrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// notChatModel recognizes the errors used by OpenAI-compatible servers when a
// model can't be called through /chat/completions.
func notChatModel(status int, body []byte) bool { return wrongEndpoint(status, body) }

// translate serves a client API the provider lacks by speaking another
// one to it. The provider is always streamed; the client gets whichever
// it asked for.
//
// When the provider says the model isn't served on this endpoint and another
// is left to try (more), nothing is written and moved is true.
func (s *Server) translate(w http.ResponseWriter, r *http.Request, p provider.Provider, from, to provider.Protocol, model string, body []byte, u *Usage, more bool) (int, string, bool) {
	request, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error(), false
	}
	stream := request.Stream
	request.Stream = true
	upBody := build(to, request, model, p.Host(), p.RejectsTemperature(model))
	res, err := s.forward(r.Context(), p, to, pathOf(to), p.Prepare(upBody), r.Header)
	if err != nil {
		return writeError(w, from, 502, p.Name+": "+err.Error()), err.Error(), false
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if s.turnedAway(p, model, to, res.StatusCode, b) && more {
			return 0, "", true
		}
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		keepRetry(w.Header(), res.Header)
		return writeError(w, from, res.StatusCode, msg), msg, false
	}
	dec := decoder(to)
	rd, sse := eventStream(res)
	if !sse {
		// the provider ignored stream:true; read the whole reply as one
		// event stream would be wrong, so give up cleanly
		b, _ := io.ReadAll(io.LimitReader(rd, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeError(w, from, 502, msg), msg, false
	}
	if stream {
		enc := encoder(from, newSSEWriter(w), request.Model)
		var failed string
		readSSE(rd, func(_, data string) error {
			return dec(data, func(ev Event) {
				switch ev.Kind {
				case KError:
					failed = ev.Text
				case KStart, KUsage:
					u.add(ev.Usage)
				}
				enc.event(ev)
			})
		})
		enc.finish()
		return 200, failed, false
	}
	var col collector
	readSSE(rd, func(_, data string) error {
		return dec(data, col.add)
	})
	if col.err != "" && len(col.res.Parts) == 0 {
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
