package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// upstream is a Chat Completions vendor that answers every request the same
// way and remembers how many it got and the last body.
type upstream struct {
	code  int
	reply string
	hits  int
	body  string
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.hits++
	u.body = string(b)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Vendor", "yes")
	if u.code != 0 {
		w.WriteHeader(u.code)
	}
	io.WriteString(w, u.reply)
}

const okReply = `{"id":"c","object":"chat.completion","model":"m2","choices":[{"index":0,"message":{"role":"assistant","content":"backup answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`

// pair sets up plan (primary) and bak (fallback), each backed by its own fake.
func pair(t *testing.T, primary, backup *upstream, fallback ...string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	a, b := httptest.NewServer(primary), httptest.NewServer(backup)
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "k", Chat: a.URL + "/v1", Models: []string{"m1"}, Fallback: fallback}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "bak", Name: "Bak", Key: "k", Chat: b.URL + "/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
}

func ask(s *Server) (int, string, http.Header) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"plan/m1","messages":[{"role":"user","content":"hi"}]}`))
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header()
}

func TestFallbackOnQuotaErrors(t *testing.T) {
	for _, c := range []struct {
		code int
		body string
	}{
		{429, `{"error":"slow down"}`},
		{402, `{"error":"pay"}`},
		{529, `{"error":"busy"}`},
		{403, `{"error":"套餐额度已用完"}`},
		{401, `{"error":"Insufficient Balance"}`},
	} {
		primary, backup := &upstream{code: c.code, reply: c.body}, &upstream{reply: okReply}
		pair(t, primary, backup, "nowhere/x", "bak/m2")
		code, body, _ := ask(New())
		if code != 200 || !strings.Contains(body, "backup answer") || strings.Contains(body, c.body) {
			t.Fatalf("%d: %d %s", c.code, code, body)
		}
		if modelOf([]byte(backup.body)) != "m2" {
			t.Fatalf("%d: backup got %s", c.code, backup.body)
		}
	}
}

func TestPlainErrorsStay(t *testing.T) {
	primary, backup := &upstream{code: 400, reply: "messages: field required"}, &upstream{reply: okReply}
	pair(t, primary, backup, "bak/m2")
	code, body, _ := ask(New())
	if code != 400 || !strings.Contains(body, "messages: field required") || backup.hits != 0 {
		t.Fatalf("%d %s hits=%d", code, body, backup.hits)
	}
}

func TestLastErrorPassesThrough(t *testing.T) {
	primary, backup := &upstream{code: 429, reply: `{"error":"first"}`}, &upstream{code: 503, reply: `{"error":"second down"}`}
	pair(t, primary, backup, "bak/m2")
	code, body, _ := ask(New())
	if code != 503 || !strings.Contains(body, "second down") || strings.Contains(body, "first") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestNoFallbackConfigured(t *testing.T) {
	primary, backup := &upstream{code: 429, reply: `{"error":"limited"}`}, &upstream{reply: okReply}
	pair(t, primary, backup)
	code, body, _ := ask(New())
	if code != 429 || !strings.Contains(body, "limited") || backup.hits != 0 {
		t.Fatalf("%d %s", code, body)
	}
}

func TestFallbackRecordAndCooldown(t *testing.T) {
	primary, backup := &upstream{code: 429, reply: `{"error":"limited"}`}, &upstream{reply: okReply}
	pair(t, primary, backup, "bak/m2")
	s := New()
	if code, _, _ := ask(s); code != 200 {
		t.Fatal(code)
	}
	calls := s.Recent()
	if len(calls) != 1 || calls[0].Provider != "bak" || !strings.HasPrefix(calls[0].Fallback, "plan: ") {
		t.Fatalf("%+v", calls)
	}
	if primary.hits != 1 {
		t.Fatalf("primary hits %d", primary.hits)
	}
	code, body, _ := ask(s)
	if code != 200 || !strings.Contains(body, "backup answer") || primary.hits != 1 {
		t.Fatalf("%d %s primary hits %d", code, body, primary.hits)
	}
	if c := s.Recent()[0]; c.Fallback != "" || c.Provider != "bak" {
		t.Fatalf("%+v", c)
	}
}
