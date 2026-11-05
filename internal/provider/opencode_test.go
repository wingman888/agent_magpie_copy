package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenCodePresets(t *testing.T) {
	go_ := Preset("opencode-go")
	if go_ == nil {
		t.Fatal("no opencode-go preset")
	}
	if go_.Name != "OpenCode Go" || go_.Chat != "https://opencode.ai/zen/go/v1" || go_.Responses != "https://opencode.ai/zen/go/v1" ||
		go_.Anthropic != "https://opencode.ai/zen/go" || go_.Website != "https://opencode.ai/docs/go" || go_.KeysURL != "https://opencode.ai/auth" {
		t.Errorf("opencode-go preset: %+v", go_)
	}
	zen := Preset("opencode-zen")
	if zen == nil {
		t.Fatal("no opencode-zen preset")
	}
	if zen.Name != "OpenCode Zen" || zen.Chat != "https://opencode.ai/zen/v1" || zen.Responses != "https://opencode.ai/zen/v1" ||
		zen.Anthropic != "https://opencode.ai/zen" || zen.Website != "https://opencode.ai/docs/zen" || zen.KeysURL != "https://opencode.ai/auth" {
		t.Errorf("opencode-zen preset: %+v", zen)
	}
	p, err := FromPreset("opencode-go")
	if err != nil || !p.IsOpenCode() {
		t.Errorf("FromPreset opencode-go: %v, IsOpenCode=%v", err, p.IsOpenCode())
	}
}

func TestIsOpenCode(t *testing.T) {
	cases := map[string]bool{
		"https://opencode.ai/zen/v1":         true,
		"https://api.opencode.ai/v1":         true,
		"https://OPENCODE.AI/zen":            true,
		"https://notopencode.ai/v1":          false,
		"https://opencode.ai.example.com/v1": false,
		"https://api.openai.com/v1":          false,
	}
	for base, want := range cases {
		if got := (Provider{Chat: base}).IsOpenCode(); got != want {
			t.Errorf("IsOpenCode(%s) = %v, want %v", base, got, want)
		}
	}
	if got := (Provider{Anthropic: "https://opencode.ai/zen"}).IsOpenCode(); !got {
		t.Error("anthropic-only opencode base should count")
	}
	if (Provider{}).IsOpenCode() {
		t.Error("empty provider is not OpenCode")
	}
}

func TestOpenCodeSessionHeaderWins(t *testing.T) {
	h := http.Header{}
	h.Set("X-Session-Id", "from-agent")
	h.Set("x-opencode-session", "first")
	if got := OpenCodeSession(h, []byte(`{"messages":[{"role":"user","content":"hi"}]}`)); got != "first" {
		t.Errorf("got %q, want the x-opencode-session header", got)
	}
	h = http.Header{}
	h.Set("x-claude-code-session-id", "cc")
	h.Set("session-id", "sid")
	if got := OpenCodeSession(h, nil); got != "sid" {
		t.Errorf("got %q, want session-id ahead of x-claude-code-session-id", got)
	}
	h = http.Header{}
	h.Set("x-opencode-session", "  ")
	if got := OpenCodeSession(h, nil); !strings.HasPrefix(got, "magpie-") {
		t.Errorf("blank header should be ignored, got %q", got)
	}
}

func TestOpenCodeSessionStablePerConversation(t *testing.T) {
	chat1 := []byte(`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"first question"},{"role":"assistant","content":"a"},{"role":"user","content":"second"}]}`)
	chat2 := []byte(`{"model":"m","messages":[{"role":"user","content":"first question"},{"role":"assistant","content":"other"},{"role":"user","content":"more"}]}`)
	other := []byte(`{"model":"m","messages":[{"role":"user","content":"a different chat"}]}`)
	a := OpenCodeSession(nil, chat1)
	if !strings.HasPrefix(a, "magpie-") || len(a) <= len("magpie-") {
		t.Fatalf("session %q", a)
	}
	if b := OpenCodeSession(nil, chat2); b != a {
		t.Errorf("later turns differ: %q vs %q", a, b)
	}
	if c := OpenCodeSession(nil, other); c == a {
		t.Errorf("different conversations share %q", a)
	}
}

func TestOpenCodeSessionResponsesShapes(t *testing.T) {
	asString := []byte(`{"model":"m","input":"hello there"}`)
	asList := []byte(`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello there"}]},{"type":"message","role":"assistant","content":"x"}]}`)
	chatSame := []byte(`{"model":"m","messages":[{"role":"user","content":"hello there"}]}`)
	s := OpenCodeSession(nil, asString)
	if got := OpenCodeSession(nil, asList); got != s {
		t.Errorf("list input %q != string input %q", got, s)
	}
	if got := OpenCodeSession(nil, chatSame); got != s {
		t.Errorf("chat shape %q != responses shape %q", got, s)
	}
	if got := OpenCodeSession(nil, []byte(`not json`)); !strings.HasPrefix(got, "magpie-") {
		t.Errorf("unparseable body: %q", got)
	}
}

func TestProbeSendsOpenCodeSession(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	oc := Provider{ID: "oc", Key: "k", Chat: "https://opencode.ai/zen/v1"}
	if r := probe(context.Background(), oc, Chat, srv.URL, body, "m"); !r.OK {
		t.Fatalf("probe failed: %+v", r)
	}
	if !strings.HasPrefix(got.Get("x-opencode-session"), "magpie-") {
		t.Errorf("OpenCode probe lacks session header: %v", got)
	}

	other := Provider{ID: "ds", Key: "k", Chat: "https://api.deepseek.com/v1"}
	if r := probe(context.Background(), other, Chat, srv.URL, body, "m"); !r.OK {
		t.Fatalf("probe failed: %+v", r)
	}
	if v, ok := got["X-Opencode-Session"]; ok {
		t.Errorf("non-OpenCode probe got session header %v", v)
	}
}
