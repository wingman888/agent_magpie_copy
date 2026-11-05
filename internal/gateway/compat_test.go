package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestDeveloperToSystemChat(t *testing.T) {
	in := `{"model":"m","stream":true,"messages":[{"role":"developer","content":"be terse <b>"},{"role":"user","content":"hi"}]}`
	out := developerToSystem(provider.Chat, []byte(in))
	want := `{"messages":[{"content":"be terse <b>","role":"system"},{"content":"hi","role":"user"}],"model":"m","stream":true}`
	if string(out) != want {
		t.Errorf("rewritten = %s\nwant      %s", out, want)
	}
}

func TestDeveloperUnchangedWithoutDeveloper(t *testing.T) {
	in := []byte(`{"model":"m",  "messages":[{"role":"system","content":"x"},{"role":"user","content":"hi"}]}`)
	if out := developerToSystem(provider.Chat, in); string(out) != string(in) {
		t.Errorf("body changed: %s", out)
	}
}

func TestDeveloperNotRewrittenForOtherProtocols(t *testing.T) {
	in := []byte(`{"model":"m","input":[{"role":"developer","content":"x"}]}`)
	if out := developerToSystem(provider.Responses, in); string(out) != string(in) {
		t.Errorf("Responses body changed: %s", out)
	}
	in = []byte(`{"model":"m","messages":[{"role":"developer","content":"x"}]}`)
	if out := developerToSystem(provider.Anthropic, in); string(out) != string(in) {
		t.Errorf("Anthropic body changed: %s", out)
	}
}

// The forwarding path rewrites developer messages for Chat upstreams only.
func TestForwardRewritesDeveloperForChatUpstream(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{}`}
	up := httptest.NewServer(f)
	defer up.Close()
	s := New()
	p := provider.Provider{ID: "ds", Name: "DS", Key: "k", Chat: up.URL + "/v1"}
	body := []byte(`{"model":"m","messages":[{"role":"developer","content":"d"},{"role":"user","content":"hi"}]}`)
	res, err := s.forward(context.Background(), p, provider.Chat, "/chat/completions", body, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if string(f.got) == string(body) || !bytes.Contains(f.got, []byte(`"role":"system"`)) || bytes.Contains(f.got, []byte(`"developer"`)) {
		t.Errorf("upstream got %s", f.got)
	}
	if f.head.Get("x-opencode-session") != "" {
		t.Errorf("non-OpenCode upstream received x-opencode-session")
	}
}

func TestForwardKeepsDeveloperForResponsesUpstream(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{}`}
	up := httptest.NewServer(f)
	defer up.Close()
	s := New()
	p := provider.Provider{ID: "ds", Name: "DS", Key: "k", Responses: up.URL + "/v1"}
	body := []byte(`{"model":"m","input":[{"role":"developer","content":"d"}]}`)
	res, err := s.forward(context.Background(), p, provider.Responses, "/responses", body, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if string(f.got) != string(body) {
		t.Errorf("Responses upstream got %s", f.got)
	}
}
