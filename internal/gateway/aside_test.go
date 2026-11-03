package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A provider with two keys that name no protocol and one made for Chat
// only. A GPT request's pool is the Chat key; the others are asides, tried
// after it. A Claude request's pool is the two unnamed keys, rotated; the
// Chat key is the aside.
func TestF09KeysMadeForAnotherProtocolAreAsides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	up := &byKey{limited: map[string]bool{}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	p := provider.Provider{ID: "tiers", Name: "Tiers", Chat: srv.URL + "/v1", Anthropic: srv.URL,
		Models: []string{"gpt-5.5", "claude-opus-5-5"}, Routing: provider.Rotate,
		Key: "sk-one", KeyName: "One",
		Keys: []provider.KeyAccount{{Name: "Two", Key: "sk-two"}, {Name: "Chat", Key: "sk-chat", Protocol: provider.Chat}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	saved, err := provider.Find("tiers")
	if err != nil {
		t.Fatal(err)
	}
	rest := func(key string) string { return "tiers#" + provider.KeyID(key) }
	s := &Server{}

	// GPT request: the Chat key alone is the pool, the unnamed keys follow.
	out, pl := s.plan(*saved, "gpt-5.5", provider.Chat)
	want := []struct {
		rest  string
		aside bool
	}{{rest("sk-chat"), false}, {rest("sk-one"), true}, {rest("sk-two"), true}}
	if len(out) != len(want) || len(pl.order) != len(want) {
		t.Fatalf("gpt: %d candidates, %d traced: %+v", len(out), len(pl.order), out)
	}
	for i, w := range want {
		if out[i].rest != w.rest || out[i].aside != w.aside {
			t.Fatalf("gpt candidate %d: %s aside=%v, want %s aside=%v", i, out[i].rest, out[i].aside, w.rest, w.aside)
		}
		if pl.order[i].ID != w.rest || pl.order[i].Aside != w.aside {
			t.Fatalf("gpt traced %d: %s aside=%v", i, pl.order[i].ID, pl.order[i].Aside)
		}
	}

	// Claude request from Chat: the two unnamed keys are the pool and rotate
	// turn by turn; the Chat key stays last, as an aside.
	var first []string
	for range 2 {
		out, _ = s.plan(*saved, "claude-opus-5-5", provider.Chat)
		if len(out) != 3 || !out[2].aside || out[2].rest != rest("sk-chat") || out[0].aside || out[1].aside {
			t.Fatalf("claude: %+v", out)
		}
		first = append(first, out[0].rest)
	}
	if first[0] == first[1] || !(first[0] == rest("sk-one") || first[0] == rest("sk-two")) {
		t.Fatalf("pool not rotated over the unnamed keys: %v", first)
	}

	// The Chat key answers, so no aside is tried.
	up.seen = nil
	if code, body := post(t, "/v1/chat/completions", `{"model":"tiers/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from-sk-chat") {
		t.Fatalf("pool answered: %d %s", code, body)
	}
	if strings.Join(up.seen, ",") != "sk-chat" {
		t.Fatalf("tried %v, want only the Chat key", up.seen)
	}
}

// When the whole pool fails, the asides are tried and can answer; the pool
// rests, so the next request goes to an unnamed key without trying it again.
func TestF09AsideAnswersWhenPoolFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	up := &byKey{limited: map[string]bool{"sk-chat": true}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	p := provider.Provider{ID: "asides", Name: "Asides", Chat: srv.URL + "/v1", Models: []string{"gpt-5.5"},
		Key: "sk-one", KeyName: "One",
		Keys: []provider.KeyAccount{{Name: "Chat", Key: "sk-chat", Protocol: provider.Chat}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", `{"model":"asides/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from-sk-one") {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Join(up.seen, ",") != "sk-chat,sk-one" {
		t.Fatalf("tried %v", up.seen)
	}
	up.seen = nil
	post(t, "/v1/chat/completions", `{"model":"asides/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	if strings.Join(up.seen, ",") != "sk-one" {
		t.Fatalf("second request tried %v, want the rested pool skipped", up.seen)
	}
}
