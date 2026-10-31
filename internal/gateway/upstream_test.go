package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestDeveloperRoleBecomesSystem(t *testing.T) {
	f := &fake{t: t, reply: sse(`data: {"choices":[{"delta":{"content":"hi"},"index":0}]}`, `data: [DONE]`)}
	setup(t, provider.Chat, f)
	body := `{"model":"fake/m1","stream":true,"temperature":0.5,"messages":[{"role":"developer","content":"be brief"},{"role":"user","content":"hi"}]}`
	if code, out := post(t, "/v1/chat/completions", body); code != 200 {
		t.Fatal(code, out)
	}
	var got struct {
		Temperature float64
		Messages    []map[string]any
	}
	if err := json.Unmarshal(f.got, &got); err != nil {
		t.Fatal(err)
	}
	if got.Messages[0]["role"] != "system" || got.Messages[0]["content"] != "be brief" || got.Messages[1]["role"] != "user" || got.Temperature != 0.5 {
		t.Fatalf("%s", f.got)
	}
}

func TestDeveloperRewriteLeavesOthersAlone(t *testing.T) {
	b := []byte(`{"messages":[{"role":"user","content":"I am a developer"}]}`)
	if string(developerToSystem(b)) != string(b) {
		t.Fatal("changed")
	}
}

func TestOpenCodeSession(t *testing.T) {
	h := http.Header{}
	chat := []byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hello"},{"role":"assistant","content":"a"},{"role":"user","content":"more"}]}`)
	chat2 := []byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hello"}]}`)
	other := []byte(`{"messages":[{"role":"user","content":"different"}]}`)
	a, b, c := opencodeSession(h, chat), opencodeSession(h, chat2), opencodeSession(h, other)
	if !strings.HasPrefix(a, "magpie-") || a != b || a == c {
		t.Fatal(a, b, c)
	}
	resp := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	resp2 := []byte(`{"input":"hello"}`)
	if r1, r2 := opencodeSession(h, resp), opencodeSession(h, resp2); r1 != r2 || r1 != a {
		t.Fatal(r1, r2, a)
	}
	h.Set("x-claude-code-session-id", "cc")
	if opencodeSession(h, chat) != "cc" {
		t.Fatal("claude id")
	}
	h.Set("session_id", "s1")
	if opencodeSession(h, chat) != "s1" {
		t.Fatal("order")
	}
	h.Set("x-opencode-session", "oc")
	if opencodeSession(h, chat) != "oc" {
		t.Fatal("own")
	}
}

func TestNoSessionHeaderForOthers(t *testing.T) {
	f := &fake{t: t, reply: sse(`data: [DONE]`)}
	setup(t, provider.Chat, f)
	post(t, "/v1/chat/completions", `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if f.head.Get("x-opencode-session") != "" {
		t.Fatal("leaked")
	}
}

func TestIsOpenCode(t *testing.T) {
	for u, want := range map[string]bool{"https://opencode.ai/zen/v1": true, "https://api.opencode.ai/x": true, "https://notopencode.ai/v1": false, "https://example.com/opencode.ai": false} {
		if got := (provider.Provider{Chat: u}).IsOpenCode(); got != want {
			t.Fatal(u, got)
		}
	}
}
