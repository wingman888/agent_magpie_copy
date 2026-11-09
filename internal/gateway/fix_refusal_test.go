package gateway

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// TestFixRefusedModelDoesNotRest: a 400 saying the model does not exist moves
// the request on, but the provider that said it does not rest, so the same
// provider still serves its other models.
func TestFixRefusedModelDoesNotRest(t *testing.T) {
	s3Env(t)
	var mu sync.Mutex
	hits := 0
	a := &s3Upstream{respond: func(n int, w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		first := hits == 1
		mu.Unlock()
		if first {
			s3Fail(400, "model fixa does not exist")(n, w, r)
			return
		}
		s3ChatOK("a")(n, w, r)
	}}
	b := &s3Upstream{respond: s3ChatOK("b")}
	s3Serve(t, provider.Provider{ID: "fixpa", Name: "FA", Models: []string{"fixa", "fixb"}, Chat: "x"}, a)
	s3Serve(t, provider.Provider{ID: "fixpb", Name: "FB", Models: []string{"fixa"}, Chat: "x"}, b)
	model := s3Group(t, "fixg", "fixpa/fixa", "fixpb/fixa")

	code, body := s3Ask(New().Handler(), "/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from-b") {
		t.Fatalf("group: %d %s", code, body)
	}
	restingUntil.Lock()
	for k := range restingUntil.m {
		if strings.Contains(k, "fixpa") {
			restingUntil.Unlock()
			t.Fatalf("provider fixpa rests after a model-level refusal: %s", k)
		}
	}
	restingUntil.Unlock()
	routed.Lock()
	for k := range routed.failures {
		if strings.Contains(k, "fixpa") {
			routed.Unlock()
			t.Fatalf("provider fixpa counted a failure for a model-level refusal: %s", k)
		}
	}
	routed.Unlock()

	// the other model of the same provider is still chosen and answered by it
	code, body = s3Ask(New().Handler(), "/v1/chat/completions", `{"model":"fixpa/fixb","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from-a") {
		t.Fatalf("other model: %d %s", code, body)
	}
	if a.count() != 2 {
		t.Fatalf("provider a asked %d times, want 2", a.count())
	}
}

func TestFixModelRefusalClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `model fixa does not exist`, true},
		{400, `{"error":{"message":"model x is not accessible via the /chat/completions endpoint"}}`, true},
		{400, `{"error":{"message":"模型不存在"}}`, true},
		{400, `{"error":{"message":"Insufficient Balance"}}`, false},
		{400, `{"error":{"message":"messages: field required"}}`, false},
		{429, `model fixa does not exist`, false},
		{500, `use v1/responses`, false},
	} {
		if got := modelRefusal(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("modelRefusal(%d, %s) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

func TestFixGptOssPreference(t *testing.T) {
	copilot := provider.Provider{ID: "copilot", Chat: "https://api.githubcopilot.com", Responses: "https://api.githubcopilot.com", Anthropic: "https://api.githubcopilot.com"}
	openai := provider.Provider{ID: "fixpref-openai", Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}
	chatFirst := []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
	respFirst := []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	for _, tc := range []struct {
		p     provider.Provider
		model string
		want  []provider.Protocol
	}{
		{copilot, "openai/gpt-oss-120b", chatFirst},
		{openai, "openai/gpt-oss-120b", chatFirst},
		{copilot, "gpt-6-sol", respFirst},
		{copilot, "o4-mini", respFirst},
		{openai, "gpt-6-sol", respFirst},
		{openai, "o4-mini", respFirst},
	} {
		got := preference(tc.p, tc.model)
		if len(got) != len(tc.want) || got[0] != tc.want[0] || got[1] != tc.want[1] {
			t.Errorf("preference(%s, %q) = %v, want %v", tc.p.ID, tc.model, got, tc.want)
		}
	}
}

