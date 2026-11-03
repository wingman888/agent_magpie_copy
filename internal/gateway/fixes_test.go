package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestRetriableStatusesAndQuotaText(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"internal error is not retried", 500, "boom", false},
		{"bad gateway is retried", 502, "", true},
		{"service unavailable is retried", 503, "", true},
		{"gateway timeout is retried", 504, "", true},
		{"overloaded is retried", 529, "", true},
		{"payment required is retried", 402, "", true},
		{"rate limit is retried", 429, "", true},
		{"403 with a quota message", 403, `{"error":"quota exceeded"}`, true},
		{"403 with a balance message", 403, `{"error":"余额不足"}`, true},
		{"403 that only says unavailable", 403, `{"error":"service unavailable in your region"}`, false},
		{"403 that says capacity", 403, `{"error":"no capacity for this model"}`, false},
		{"plain 403", 403, `{"error":"forbidden"}`, false},
		{"400 is not retried", 400, `{"error":"quota in the request"}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := retriable(c.status, []byte(c.body)); got != c.want {
				t.Errorf("retriable(%d, %q) = %v, want %v", c.status, c.body, got, c.want)
			}
		})
	}
}

func TestStreamTextThatMentionsLimitsIsNotAnError(t *testing.T) {
	ordinary := []byte("event: content_block_delta\n" +
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"the rate limit and quota are fine"}}` + "\n\n")
	if ok, _, _ := streamError(ordinary, "p"); ok {
		t.Error("an ordinary text event was judged a failure")
	}
	unavailable := []byte("event: error\n" +
		`data: {"type":"error","error":{"type":"api_error","message":"service unavailable"}}` + "\n\n")
	if ok, _, _ := streamError(unavailable, "p"); ok {
		t.Error("an error that names no quota or rate limit moved the request on")
	}
	limited := []byte("event: error\n" +
		`data: {"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}` + "\n\n")
	ok, status, _ := streamError(limited, "p")
	if !ok || status != 429 {
		t.Errorf("rate limit error: ok %v status %d", ok, status)
	}
}

func TestResting_ProviderIsSkippedWhenAnotherCanTakeTheRequest(t *testing.T) {
	tries := []attempt{
		{prov: "a", provResting: true},
		{prov: "b"},
		{prov: "a", provResting: true, group: 1},
	}
	out := skipResting(tries)
	if len(out) != 1 || out[0].prov != "b" {
		t.Fatalf("kept %+v, want only provider b", out)
	}
}

func TestResting_LoneProviderIsStillTried(t *testing.T) {
	tries := []attempt{{prov: "a", provResting: true}, {prov: "a", provResting: true, group: 1}}
	if out := skipResting(tries); len(out) != 2 {
		t.Fatalf("kept %d tries, want both of the only provider's", len(out))
	}
}

func TestCallRecordsTheKeyThatServed(t *testing.T) {
	keyHome(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer sk-personal" {
			w.Header().Set("Content-Type", jsonType)
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"slow down"}}`)
			return
		}
		w.Header().Set("Content-Type", streamType)
		io.WriteString(w, chatReply)
	}))
	t.Cleanup(up.Close)
	saveProvider(t, provider.Provider{ID: "main", Name: "Main", Chat: up.URL + "/v1", Key: "sk-personal", KeyName: "personal",
		Keys: []provider.ExtraKey{{Name: "team", Key: "sk-team"}}, Models: []string{"m1"}})
	s := New()
	code, body := callGateway(t, s.Handler(), "/v1/chat/completions", chatBody)
	if code != 200 || !strings.Contains(body, "ok") {
		t.Fatalf("status %d: %s", code, body)
	}
	rec := s.Recent()[0]
	if rec.Key != "team" {
		t.Errorf("key recorded %q, want the serving key's name team", rec.Key)
	}
	if rec.Account != "" {
		t.Errorf("account recorded %q on a keyed call", rec.Account)
	}
	if rec.Fallback != "main (personal)" {
		t.Errorf("fallback %q", rec.Fallback)
	}
}
