package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/provider"
	"github.com/yetone/dial/internal/usage"
)

// DefaultAddr is where the gateway listens unless DIAL_ADDR says otherwise.
const DefaultAddr = "127.0.0.1:3425"

// Token is the bearer token agents are told to use. The gateway only
// listens on loopback and accepts anything, but agents insist on one.
const Token = "dial"

// Version is set by main.
var Version = "dev"

// Addr is the listen address.
func Addr() string {
	if a := os.Getenv("DIAL_ADDR"); a != "" {
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
	return bytes.Contains(b, []byte(`"dial"`))
}

// Call is one request the gateway handled, for the status views.
type Call struct {
	Time     time.Time         `json:"time"`
	Agent    string            `json:"agent"` // who called, from the client's User-Agent
	Model    string            `json:"model"`
	Provider string            `json:"provider"`
	From     provider.Protocol `json:"from"`
	To       provider.Protocol `json:"to"`
	Status   int               `json:"status"`
	Millis   int64             `json:"ms"`
	Error    string            `json:"error,omitempty"`
	Usage    Usage             `json:"usage"`
}

// Server is the gateway.
type Server struct {
	client *http.Client
	mu     sync.Mutex
	recent []Call
	debug  bool
}

// New makes a gateway.
func New() *Server {
	return &Server{
		client: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 10 * time.Minute,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
		}},
		debug: os.Getenv("DIAL_DEBUG") != "",
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
// another dial is already serving, which is fine for the caller to ignore.
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

// Handler routes the three APIs.
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
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, provider.Chat, http.StatusNotFound, "dial serves /v1/chat/completions, /v1/responses and /v1/messages")
	})
	return mux
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"name": "dial", "version": Version, "models": len(provider.Catalog()),
		"apis": []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"}})
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
	n := len(req.System)
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Name)
		}
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	writeJSON(w, 200, map[string]any{"input_tokens": n / 4})
}

// handle is the request path of one client API.
func (s *Server) handle(from provider.Protocol) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if err != nil {
			writeError(w, from, 400, err.Error())
			return
		}
		start := time.Now()
		call := Call{Time: start, From: from, Model: modelOf(body), Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
		p, model, ok := provider.Resolve(call.Model)
		if !ok {
			call.Status, call.Error = 404, "unknown model"
			s.record(call)
			msg := fmt.Sprintf("dial knows no model %q", call.Model)
			if ids := provider.IDs(); len(ids) > 0 {
				msg += "; it has " + strings.Join(ids, ", ")
			} else {
				msg += "; add a provider in dial first"
			}
			writeError(w, from, 404, msg)
			return
		}
		call.Provider = p.ID
		if p.Base(from) != "" {
			call.To = from
			call.Status, call.Error = s.passthrough(w, r, p, from, model, body, &call.Usage)
		} else {
			to := p.Speaks()
			if len(to) == 0 {
				writeError(w, from, 502, p.Name+" has no endpoint configured")
				return
			}
			call.To = to[0]
			call.Status, call.Error = s.translate(w, r, p, from, to[0], model, body, &call.Usage)
		}
		call.Millis = time.Since(start).Milliseconds()
		s.record(call)
		usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: model,
			Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
			CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
	}
}

// forward sends a request to the provider.
func (s *Server) forward(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Base(to)+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", "dial/"+Version)
	if to == provider.Anthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
		for _, h := range []string{"anthropic-version", "anthropic-beta"} {
			if v := in.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
	}
	for k, v := range provider.AuthHeaders(p, to) {
		req.Header.Set(k, v)
	}
	return s.client.Do(req)
}

// passthrough relays a request the provider understands as-is, with the
// model name swapped for the provider's own. The token counts the reply
// carries are read on the way past into u.
func (s *Server) passthrough(w http.ResponseWriter, r *http.Request, p provider.Provider, proto provider.Protocol, model string, body []byte, u *Usage) (int, string) {
	res, err := s.forward(r.Context(), p, proto, pathOf(proto), rewriteModel(body, model), r.Header)
	if err != nil {
		return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, proto, res.StatusCode, msg), msg
	}
	h := w.Header()
	for _, k := range []string{"Content-Type", "Request-Id", "X-Request-Id"} {
		if v := res.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(res.StatusCode)
	f, _ := w.(http.Flusher)
	sniff := newSniffer(proto, res.Header.Get("Content-Type"))
	defer func() { u.add(sniff.usage()) }()
	buf := make([]byte, 32<<10)
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			sniff.write(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				return res.StatusCode, ""
			}
			if f != nil {
				f.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	return res.StatusCode, ""
}

// translate serves a client API the provider lacks by speaking another
// one to it. The provider is always streamed; the client gets whichever
// it asked for.
func (s *Server) translate(w http.ResponseWriter, r *http.Request, p provider.Provider, from, to provider.Protocol, model string, body []byte, u *Usage) (int, string) {
	req, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	stream := req.Stream
	req.Stream = true
	res, err := s.forward(r.Context(), p, to, pathOf(to), build(to, req, model, p.Host()), r.Header)
	if err != nil {
		return writeError(w, from, 502, p.Name+": "+err.Error()), err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, from, res.StatusCode, msg), msg
	}
	dec := decoder(to)
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		// the provider ignored stream:true; read the whole reply as one
		// event stream would be wrong, so give up cleanly
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeError(w, from, 502, msg), msg
	}
	if stream {
		enc := encoder(from, newSSEWriter(w), req.Model)
		var failed string
		readSSE(res.Body, func(_, data string) error {
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
		return 200, failed
	}
	var col collector
	readSSE(res.Body, func(_, data string) error {
		return dec(data, col.add)
	})
	if col.err != "" && len(col.res.Parts) == 0 {
		return writeError(w, from, 502, p.Name+": "+col.err), col.err
	}
	res2 := col.finish()
	u.add(res2.Usage)
	out := render(from, res2, req.Model)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(out)
	return 200, col.err
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
	}
	return parseAnthropic(body)
}

func build(proto provider.Protocol, r *Request, model, host string) []byte {
	switch proto {
	case provider.Chat:
		return buildChat(r, model, host)
	case provider.Responses:
		return buildResponses(r, model)
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
	}
	return &anthropicEncoder{w: w, model: model}
}

func render(proto provider.Protocol, res Result, model string) []byte {
	switch proto {
	case provider.Chat:
		return renderChat(res, model)
	case provider.Responses:
		return renderResponses(res, model)
	}
	return renderAnthropic(res, model)
}

// ---- small helpers ------------------------------------------------------------

func modelOf(body []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &v)
	return v.Model
}

// rewriteModel swaps the model field, keeping every other field as it was.
func rewriteModel(body []byte, model string) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return body
	}
	m["model"] = model
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
	if proto == provider.Anthropic {
		v = map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}}
	} else {
		v = map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": nil, "param": nil}}
	}
	writeJSON(w, status, v)
	return status
}
