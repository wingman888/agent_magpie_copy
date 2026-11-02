package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// upstream is a vendor whose replies are scripted per request.
type foUpstream struct {
	mu    sync.Mutex
	hits  int
	paths []string
	reply func(n int, w http.ResponseWriter, r *http.Request)
}

func (u *foUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	u.mu.Lock()
	u.hits++
	n := u.hits
	u.paths = append(u.paths, r.URL.Path)
	u.mu.Unlock()
	u.reply(n, w, r)
}

func (u *foUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits
}

func foJSONErr(code int, msg string, hdr ...string) func(int, http.ResponseWriter, *http.Request) {
	return func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for i := 0; i+1 < len(hdr); i += 2 {
			w.Header().Set(hdr[i], hdr[i+1])
		}
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"`+msg+`"}}`)
	}
}

const foOKChat = `{"id":"from-b","choices":[{"index":0,"message":{"role":"assistant","content":"hello from b"},"finish_reason":"stop"}]}`

func foOKReply(_ int, w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, foOKChat)
}

// pair makes providers plan (models m1) and spare (m2), plan falling back to
// spare; both speak Chat and Anthropic at the same upstream.
func foPair(t *testing.T, a, b *foUpstream) {
	t.Helper()
	foFresh(t)
	for _, x := range []struct {
		p provider.Provider
		u *foUpstream
	}{
		{provider.Provider{ID: "plan", Name: "Plan", Key: "k", Models: []string{"m1"}, Fallback: []string{"spare/m2"}}, a},
		{provider.Provider{ID: "spare", Name: "Spare", Key: "k", Models: []string{"m2"}}, b},
	} {
		srv := httptest.NewServer(x.u)
		t.Cleanup(srv.Close)
		x.p.Chat, x.p.Anthropic = srv.URL+"/v1", srv.URL
		if err := provider.Save(x.p); err != nil {
			t.Fatal(err)
		}
	}
}

func foServer() *Server {
	s := New()
	s.retryBase = time.Millisecond
	return s
}

func foSend(s *Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func TestFailoverByStatusAndMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		msg  string
		move bool
	}{
		{"endpoint refusal", 400, "model m1 is not accessible via the /chat/completions endpoint", true},
		{"unauthorized", 401, "bad key", true},
		{"not found", 404, "nope", true},
		{"model missing in Chinese", 400, "模型不存在", true},
		{"model not supported", 400, "The model `m1` does not exist or you do not have access to it", true},
		{"balance", 400, "Insufficient balance", true},
		{"missing field", 400, "messages: field required", false},
		{"context too long", 400, "This model's maximum context length is 8192 tokens, your request exceeded it", false},
		{"prompt too long", 400, "prompt is too long: 250000 tokens, over the 200000 maximum", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &foUpstream{reply: foJSONErr(tc.code, tc.msg)}
			b := &foUpstream{reply: okReply}
			foPair(t, a, b)
			code, body := foSend(foServer(), "/v1/chat/completions", chatReq)
			if tc.move {
				if code != 200 || !strings.Contains(body, "hello from b") || strings.Contains(body, tc.msg) {
					t.Fatalf("%d %s", code, body)
				}
			} else if code != 400 || !strings.Contains(body, tc.msg) || b.count() != 0 {
				t.Fatalf("%d %s (b hit %d)", code, body, b.count())
			}
		})
	}
}

func foAnthStream(lines ...string) func(int, http.ResponseWriter, *http.Request) {
	return func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			io.WriteString(w, l+"\n\n")
			w.(http.Flusher).Flush()
		}
	}
}

const (
	foMsgStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_A\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"x\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}"
	foPing     = "event: ping\ndata: {\"type\":\"ping\"}"
	foOverload = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}"
	foBlockBeg = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}"
	foTextA    = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}"
	foReqAnth  = `{"model":"plan/m1","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
)

func foSpareStream(_ int, w http.ResponseWriter, _ *http.Request) {
	foAnthStream(
		strings.Replace(foMsgStart, "msg_A", "msg_B", 1), foBlockBeg,
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"from B\"}}",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}")(0, w, nil)
}

func TestStreamErrorBeforeContentFailsOver(t *testing.T) {
	a := &foUpstream{reply: foAnthStream(foMsgStart, foPing, foOverload)}
	b := &foUpstream{reply: spareStream}
	foPair(t, a, b)
	s := foServer()
	code, body := foSend(s, "/v1/messages", foReqAnth)
	if code != 200 || !strings.Contains(body, "from B") || strings.Contains(body, "Overloaded") || strings.Contains(body, "msg_A") {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Count(body, "event: message_start") != 1 || strings.Contains(body, "event: ping") {
		t.Fatalf("A's preamble leaked: %s", body)
	}
	st := s.Trace(context.Background(), 0, 0)
	tries := st.Routes[0].Tries
	if len(tries) != 2 || tries[0].Status != 529 || tries[0].Rest == nil || tries[0].Kind != kindFailover {
		t.Fatalf("tries %+v", tries)
	}
	if !s.resting("plan") {
		t.Fatal("A should rest")
	}
	// the next request goes straight to B
	hits := a.count()
	foSend(s, "/v1/messages", foReqAnth)
	if a.count() != hits {
		t.Fatal("resting A was asked again")
	}
}

func TestStreamErrorAfterContentIsDelivered(t *testing.T) {
	a := &foUpstream{reply: foAnthStream(foMsgStart, foBlockBeg, foTextA, foOverload)}
	b := &foUpstream{reply: spareStream}
	foPair(t, a, b)
	code, body := foSend(foServer(), "/v1/messages", foReqAnth)
	if code != 200 || !strings.Contains(body, "partial") || !strings.Contains(body, "Overloaded") || b.count() != 0 {
		t.Fatalf("%d %s (b hit %d)", code, body, b.count())
	}
}

func TestStreamErrorOnLastCandidateIsDelivered(t *testing.T) {
	a := &foUpstream{reply: foAnthStream(foMsgStart, foPing, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"bad thing\"}}")}
	foPair(t, a, &foUpstream{reply: spareStream})
	s := foServer()
	// plan/m1 has a fallback, so ask the spare directly: it is the only one
	_ = s
	code, body := foSend(s, "/v1/messages", foReqAnth)
	if code != 200 || (!strings.Contains(body, "bad thing") && !strings.Contains(body, "from B")) {
		t.Fatalf("%d %s", code, body)
	}
}

func TestChatStreamErrorChunkFailsOver(t *testing.T) {
	a := &foUpstream{reply: func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"a\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n")
		io.WriteString(w, "data: {\"error\":{\"message\":\"server busy\",\"type\":\"server_error\"}}\n\n")
	}}
	b := &foUpstream{reply: func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"b\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"from B\"}}]}\n\ndata: [DONE]\n\n")
	}}
	foPair(t, a, b)
	code, body := foSend(foServer(), "/v1/chat/completions", `{"model":"plan/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from B") || strings.Contains(body, "server busy") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestSingleProviderRetries(t *testing.T) {
	single := func(t *testing.T, u *foUpstream) *Server {
		foFresh(t)
		srv := httptest.NewServer(u)
		t.Cleanup(srv.Close)
		if err := provider.Save(provider.Provider{ID: "solo", Name: "Solo", Key: "k", Models: []string{"m1"}, Chat: srv.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		return foServer()
	}
	const req = `{"model":"solo/m1","messages":[{"role":"user","content":"hi"}]}`

	t.Run("recovers", func(t *testing.T) {
		u := &foUpstream{reply: func(n int, w http.ResponseWriter, r *http.Request) {
			switch n {
			case 1:
				foJSONErr(503, "down")(n, w, r)
			case 2:
				foJSONErr(502, "bad gateway")(n, w, r)
			default:
				foOKReply(n, w, r)
			}
		}}
		s := single(t, u)
		code, body := foSend(s, "/v1/chat/completions", req)
		if code != 200 || u.count() != 3 || !strings.Contains(body, "hello from b") {
			t.Fatalf("%d %s hits %d", code, body, u.count())
		}
		tries := s.Trace(context.Background(), 0, 0).Routes[0].Tries
		if len(tries) != 3 || tries[0].Kind != kindRetry || tries[0].Wait != 1 || tries[1].Kind != kindRetry || tries[1].Wait != 2 || tries[2].Kind != "" {
			t.Fatalf("tries %+v", tries)
		}
		if s.resting("solo") {
			t.Fatal("rests after recovering")
		}
	})
	t.Run("keeps failing", func(t *testing.T) {
		u := &foUpstream{reply: foJSONErr(503, "still down")}
		code, body := foSend(single(t, u), "/v1/chat/completions", req)
		if code != 503 || u.count() != 3 || !strings.Contains(body, "still down") {
			t.Fatalf("%d %s hits %d", code, body, u.count())
		}
	})
	t.Run("plain 400", func(t *testing.T) {
		u := &foUpstream{reply: foJSONErr(400, "temperature: out of range")}
		if code, _ := foSend(single(t, u), "/v1/chat/completions", req); code != 400 || u.count() != 1 {
			t.Fatalf("%d hits %d", code, u.count())
		}
	})
	t.Run("429 without Retry-After", func(t *testing.T) {
		u := &foUpstream{reply: foJSONErr(429, "slow down")}
		if code, _ := foSend(single(t, u), "/v1/chat/completions", req); code != 429 || u.count() != 1 {
			t.Fatalf("%d hits %d", code, u.count())
		}
	})
	t.Run("long Retry-After", func(t *testing.T) {
		u := &foUpstream{reply: foJSONErr(503, "later", "Retry-After", "9")}
		if code, _ := foSend(single(t, u), "/v1/chat/completions", req); code != 503 || u.count() != 1 {
			t.Fatalf("%d hits %d", code, u.count())
		}
	})
	t.Run("429 with Retry-After", func(t *testing.T) {
		u := &foUpstream{reply: func(n int, w http.ResponseWriter, r *http.Request) {
			if n == 1 {
				foJSONErr(429, "slow down", "Retry-After", "0.01")(n, w, r)
				return
			}
			foOKReply(n, w, r)
		}}
		if code, _ := foSend(single(t, u), "/v1/chat/completions", req); code != 200 || u.count() != 2 {
			t.Fatalf("%d hits %d", code, u.count())
		}
	})
}

func TestCanceledRequestBlamesNobody(t *testing.T) {
	started := make(chan struct{}, 1)
	a := &foUpstream{reply: func(_ int, w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}}
	b := &foUpstream{reply: okReply}
	foPair(t, a, b)
	s := foServer()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)).WithContext(ctx)
	rec := httptest.NewRecorder()
	var done atomic.Bool
	go func() {
		s.Handler().ServeHTTP(rec, req)
		done.Store(true)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never received the request")
	}
	cancel()
	for i := 0; i < 500 && !done.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !done.Load() {
		t.Fatal("handler did not return")
	}
	if b.count() != 0 {
		t.Fatal("B was asked after the agent left")
	}
	if c := s.Recent()[0]; c.Status != 499 {
		t.Fatalf("recorded %+v", c)
	}
	rt := s.Trace(context.Background(), 0, 0).Routes[0]
	if rt.Status != 499 || len(rt.Tries) != 1 || rt.Tries[0].Fail != "canceled" || rt.Tries[0].Kind != kindCanceled || rt.Tries[0].Rest != nil {
		t.Fatalf("route %+v", rt)
	}
	if s.resting("plan") {
		t.Fatal("A rests after the agent left")
	}
}

func TestSuccessEndsRest(t *testing.T) {
	a := &foUpstream{reply: okReply}
	foPair(t, a, &foUpstream{reply: okReply})
	restingUntil.Lock()
	restingUntil.m["plan"] = time.Now().Add(time.Hour)
	restingUntil.Unlock()
	served("plan", 5)
	if foRestingFor("plan") {
		t.Fatal("still resting")
	}
}

func foRestingFor(id string) bool { _, ok := restOf(id); return ok }

func TestStreamHeldPastLimitPasses(t *testing.T) {
	old := streamHoldMax
	streamHoldMax = 64
	t.Cleanup(func() { streamHoldMax = old })
	big := "event: ping\ndata: {\"type\":\"ping\",\"pad\":\"" + strings.Repeat("x", 200) + "\"}"
	a := &foUpstream{reply: foAnthStream(foMsgStart, big, foOverload)}
	b := &foUpstream{reply: spareStream}
	foPair(t, a, b)
	_, body := foSend(foServer(), "/v1/messages", foReqAnth)
	if !strings.Contains(body, "Overloaded") || b.count() != 0 {
		t.Fatalf("%s (b hit %d)", body, b.count())
	}
}

// foFresh isolates storage and clears the package-level routing state, both
// now and when the test ends.
func foFresh(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	clear := func() {
		restingUntil.Lock()
		restingUntil.m = map[string]time.Time{}
		restingUntil.Unlock()
		sticks.Lock()
		sticks.m = map[string]stick{}
		sticks.Unlock()
		catalog.Reset()
	}
	clear()
	t.Cleanup(clear)
}
