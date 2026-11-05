package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// recordTransport answers every request itself and remembers what was sent.
type recordTransport struct {
	req  *http.Request
	body []byte
}

func (rt *recordTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.req = r
	rt.body, _ = io.ReadAll(r.Body)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
}

func TestDeveloperToSystem(t *testing.T) {
	in := []byte(`{"model":"m1","messages":[{"role":"developer","content":"be terse"},{"role":"user","content":"hi"}],"n":12345678901234567890}`)
	out := developerToSystem(in)
	var got struct {
		Messages []map[string]any `json:"messages"`
		N        json.Number      `json:"n"`
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Messages[0]["role"] != "system" || got.Messages[0]["content"] != "be terse" || got.Messages[1]["role"] != "user" {
		t.Errorf("messages: %s", out)
	}
	if got.N.String() != "12345678901234567890" {
		t.Errorf("other fields changed: %s", out)
	}

	plain := []byte(`{"model":"m1",  "messages":[{"role":"system","content":"s"}]}`)
	if !bytes.Equal(developerToSystem(plain), plain) {
		t.Errorf("body without developer must be unchanged")
	}
}

func TestChatPassthroughRewritesDeveloper(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"c1"}`}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"developer","content":"d"},{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if bytes.Contains(f.got, []byte(`"developer"`)) || !bytes.Contains(f.got, []byte(`"role":"system"`)) {
		t.Errorf("upstream got %s", f.got)
	}
	if !bytes.Contains(f.got, []byte(`"model":"m1"`)) {
		t.Errorf("model not rewritten: %s", f.got)
	}
}

func TestOpenCodeSessionHeaderOnlyForOpenCode(t *testing.T) {
	body := []byte(`{"model":"m1","messages":[{"role":"user","content":"hello"}]}`)
	in := http.Header{}
	in.Set("x-session-id", "agent-session")

	rt := &recordTransport{}
	s := New()
	s.client = &http.Client{Transport: rt}

	oc := provider.Provider{ID: "oc", Name: "OC", Key: "k", Chat: "https://opencode.ai/zen/go/v1"}
	if _, err := s.forward(context.Background(), oc, provider.Chat, "/chat/completions", body, in); err != nil {
		t.Fatal(err)
	}
	if got := rt.req.Header.Get("x-opencode-session"); got != "agent-session" {
		t.Errorf("opencode request header = %q", got)
	}

	if _, err := s.forward(context.Background(), oc, provider.Chat, "/chat/completions", body, http.Header{}); err != nil {
		t.Fatal(err)
	}
	first := rt.req.Header.Get("x-opencode-session")
	if !strings.HasPrefix(first, "magpie-") {
		t.Errorf("fallback session = %q", first)
	}

	other := provider.Provider{ID: "ds", Name: "DS", Key: "k", Chat: "https://api.deepseek.com/v1"}
	if _, err := s.forward(context.Background(), other, provider.Chat, "/chat/completions", body, in); err != nil {
		t.Fatal(err)
	}
	if got := rt.req.Header.Get("x-opencode-session"); got != "" {
		t.Errorf("other provider received x-opencode-session = %q", got)
	}
}

func TestDeveloperToSystemKeepsOtherBytes(t *testing.T) {
	in := []byte(`{"model":"m1",  "messages":[{"role":"developer","content":"be <terse> & short"},` +
		`{"role":"user",   "content":"y  z é"}],"n":12345678901234567890}`)
	out := developerToSystem(in)
	want := []byte(`{"model":"m1",  "messages":[{"role":"system","content":"be <terse> & short"},` +
		`{"role":"user",   "content":"y  z é"}],"n":12345678901234567890}`)
	if !bytes.Equal(out, want) {
		t.Errorf("got  %s\nwant %s", out, want)
	}

	plain := []byte(`{ "messages" : [ { "role" : "user" , "content" : "a & b" } ] }`)
	if !bytes.Equal(developerToSystem(plain), plain) {
		t.Errorf("body without developer must be byte-identical")
	}
	noMsgs := []byte(`{"input":"hi"}`)
	if !bytes.Equal(developerToSystem(noMsgs), noMsgs) {
		t.Errorf("body without messages must be byte-identical")
	}
}
