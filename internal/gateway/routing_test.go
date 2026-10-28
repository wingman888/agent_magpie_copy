package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Tests don't ask vendors how much of an allowance is used: the gateway
// would, behind a request, of whatever upstream a test serves.
func init() { allowanceUsed = func(string) map[string]float64 { return nil } }

func restsOf(cs []candidate) string {
	s := ""
	for _, c := range cs {
		s += c.rest + " "
	}
	return s
}

func TestRouting(t *testing.T) {
	cs := []candidate{{rest: "r#a"}, {rest: "r#b"}, {rest: "r#c"}}
	p := provider.Provider{ID: "r"}
	if got := restsOf(route(p, cs, "m", provider.Chat)); got != "r#a r#b r#c " {
		t.Fatalf("smart: %s", got)
	}

	p.Routing = provider.Rotate
	var firsts string
	for range 4 {
		firsts += route(p, cs, "m", provider.Chat)[0].rest + " "
	}
	if firsts != "r#a r#b r#c r#a " {
		t.Fatalf("in turn: %s", firsts)
	}
	if restsOf(cs) != "r#a r#b r#c " {
		t.Fatal("rotating changed the list it was given")
	}

	p.Routing = provider.Ordered
	if got := restsOf(route(p, cs, "m", provider.Chat)); got != "r#a r#b r#c " {
		t.Fatalf("in order: %s", got)
	}

	p.ID, p.Routing = "u", provider.LeastUsed
	cs = []candidate{{rest: "u#a"}, {rest: "u#b"}, {rest: "u#c"}}
	served("u#a", 5000)
	served("u#c", 10)
	if got := restsOf(route(p, cs, "m", provider.Chat)); got != "u#b u#c u#a " {
		t.Fatalf("least used: %s", got)
	}
}

// Smart routing keeps the first while it has quota to spare, then goes to
// whichever has the most; a failure rests as long as it says.
func TestSmartRouting(t *testing.T) {
	old := allowanceUsed
	defer func() { allowanceUsed = old }()
	share := map[string]float64{"a": 10, "b": 50, "c": 20}
	allowanceUsed = func(string) map[string]float64 { return share }
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{Account: &provider.Account{Agent: "x", User: user}}, rest: "s#" + user}
	}
	p := provider.Provider{ID: "s", Account: &provider.Account{Agent: "x"}}
	cs := []candidate{acct("a"), acct("b"), acct("c")}
	if got := restsOf(route(p, cs, "m", provider.Chat)); got != "s#a s#b s#c " {
		t.Fatalf("all fine: %s", got)
	}
	share["a"], share["b"] = 99, 93
	if got := restsOf(route(p, cs, "m", provider.Chat)); got != "s#c s#b s#a " {
		t.Fatalf("a used up, b low: %s", got)
	}

	for _, c := range []struct {
		status int
		body   string
		want   string
	}{
		{402, `{}`, failCredit},
		{400, `{"error":{"message":"Your credit balance is too low"}}`, failCredit},
		{429, `{"error":{"code":"insufficient_quota"}}`, failCredit},
		{403, `账户余额不足`, failCredit},
		{429, `{"error":{"message":"usage limit reached for your plan"}}`, failQuota},
		{429, `{"error":{"message":"Too many requests"}}`, failRate},
		{503, `overloaded`, failOther},
	} {
		if got := failure(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%d %s: %s, want %s", c.status, c.body, got, c.want)
		}
	}

	s := &Server{}
	until := func(id string) time.Duration {
		restingUntil.Lock()
		defer restingUntil.Unlock()
		return time.Until(restingUntil.m[id]).Round(time.Minute)
	}
	s.restAfter("t#credit", 402, http.Header{}, nil)
	h := http.Header{}
	h.Set("Retry-After", "300")
	s.restAfter("t#rate", 429, h, []byte("slow down"))
	s.restAfter("t#fail", 500, http.Header{}, nil)
	s.restAfter("t#fail", 500, http.Header{}, nil)
	if until("t#credit") != creditRest || until("t#rate") != 5*time.Minute || until("t#fail") != 2*time.Minute {
		t.Fatalf("rests: %v %v %v", until("t#credit"), until("t#rate"), until("t#fail"))
	}
	served("t#fail", 1)
	s.restAfter("t#fail", 500, http.Header{}, nil)
	if until("t#fail") != time.Minute {
		t.Fatalf("answering again starts over: %v", until("t#fail"))
	}
	h = http.Header{}
	h.Set("X-Ratelimit-Reset-Tokens", "6m0s")
	if d := retryAfter(h, time.Now()); d != 6*time.Minute {
		t.Fatalf("reset header: %v", d)
	}
}
