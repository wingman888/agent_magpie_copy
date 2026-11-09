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

// ocRoundTrip captures the upstream request it is handed and answers 200.
type ocRoundTrip struct {
	header http.Header
	body   []byte
}

func (rt *ocRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.header = r.Header.Clone()
	rt.body, _ = io.ReadAll(r.Body)
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Request:    r,
	}, nil
}

// ocForward sends one body through the gateway's forward path to p and
// returns what the upstream saw.
func ocForward(t *testing.T, p provider.Provider, to provider.Protocol, body string, in http.Header) *ocRoundTrip {
	t.Helper()
	rt := &ocRoundTrip{}
	s := New()
	s.client = &http.Client{Transport: rt}
	res, err := s.forward(context.Background(), p, to, "/x", []byte(body), in)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return rt
}

func ocChatBody(messages ...string) string {
	return `{"model":"m","messages":[` + strings.Join(messages, ",") + `]}`
}

func TestOCDeveloperRoleBecomesSystem(t *testing.T) {
	in := `{"model":"m","stream":true,"temperature":0.5,"messages":[{"role":"developer","content":"be terse"},{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}]}`
	out := developerToSystem([]byte(in))

	var got, want map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(strings.Replace(in, `"developer"`, `"system"`, 1)), &want); err != nil {
		t.Fatal(err)
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if !bytes.Equal(gj, wj) {
		t.Errorf("rewrite changed more than the role:\n got %s\nwant %s", gj, wj)
	}
	if bytes.Contains(out, []byte(`"developer"`)) {
		t.Errorf("developer role left in %s", out)
	}
}

func TestOCBodyWithoutDeveloperIsUnchanged(t *testing.T) {
	for _, in := range []string{
		`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`,
		// the word alone, in content, is not a role
		`{"model":"m","messages":[{"role":"user","content":"a \"developer\" note"}]}`,
		`{"model":"m","input":"hi"}`,
	} {
		if out := developerToSystem([]byte(in)); !bytes.Equal(out, []byte(in)) {
			t.Errorf("body changed:\n got %s\nwant %s", out, in)
		}
	}
}

func TestOCChatForwardRewritesDeveloperEndToEnd(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"x","choices":[]}`}
	setup(t, provider.Chat, f)
	code, _ := post(t, "/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"developer","content":"rules"},{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if bytes.Contains(f.got, []byte(`"developer"`)) || !bytes.Contains(f.got, []byte(`"role":"system"`)) {
		t.Errorf("upstream got %s", f.got)
	}
}

func TestOCSessionHeaderAgentValueWins(t *testing.T) {
	p := provider.Provider{ID: "oc", Name: "OC", Key: "k", Chat: "https://opencode.ai/zen/v1"}
	in := http.Header{}
	in.Set("x-session-id", "agent-123")
	in.Set("session_id", "other")
	rt := ocForward(t, p, provider.Chat, ocChatBody(`{"role":"user","content":"hi"}`), in)
	if got := rt.header.Get("x-opencode-session"); got != "agent-123" {
		t.Errorf("x-opencode-session = %q, want the agent's x-session-id", got)
	}
}

func TestOCSessionHeaderDerivedAndStable(t *testing.T) {
	p := provider.Provider{ID: "oc", Name: "OC", Key: "k", Chat: "https://opencode.ai/zen/v1"}
	first := ocChatBody(`{"role":"user","content":"write a parser"}`)
	later := ocChatBody(
		`{"role":"user","content":"write a parser"}`,
		`{"role":"assistant","content":"ok"}`,
		`{"role":"user","content":"now tests"}`)
	other := ocChatBody(`{"role":"user","content":"something else"}`)

	a := ocForward(t, p, provider.Chat, first, nil).header.Get("x-opencode-session")
	b := ocForward(t, p, provider.Chat, later, nil).header.Get("x-opencode-session")
	c := ocForward(t, p, provider.Chat, other, nil).header.Get("x-opencode-session")

	if !strings.HasPrefix(a, "magpie-") || len(a) <= len("magpie-") {
		t.Fatalf("derived session %q, want magpie-<hex>", a)
	}
	if a != b {
		t.Errorf("later turn got %q, first turn %q; a conversation must keep one id", b, a)
	}
	if a == c {
		t.Errorf("different conversations share session %q", a)
	}
}

func TestOCSessionHeaderResponsesInput(t *testing.T) {
	p := provider.Provider{ID: "oc", Name: "OC", Key: "k", Responses: "https://opencode.ai/zen/v1"}
	str := ocForward(t, p, provider.Responses, `{"model":"m","input":"hello there"}`, nil).header.Get("x-opencode-session")
	arr := ocForward(t, p, provider.Responses, `{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"hello there"}]},{"role":"assistant","content":"x"}]}`, nil).header.Get("x-opencode-session")
	if !strings.HasPrefix(str, "magpie-") {
		t.Fatalf("string input: session %q", str)
	}
	if str != arr {
		t.Errorf("string input %q and item input %q should give the same session for the same first message", str, arr)
	}
}

func TestOCSessionHeaderOnlyForOpenCode(t *testing.T) {
	p := provider.Provider{ID: "other", Name: "Other", Key: "k", Chat: "https://api.example.com/v1"}
	in := http.Header{}
	in.Set("x-session-id", "agent-123")
	rt := ocForward(t, p, provider.Chat, ocChatBody(`{"role":"user","content":"hi"}`), in)
	if v, ok := rt.header["X-Opencode-Session"]; ok {
		t.Errorf("non-OpenCode upstream got x-opencode-session %v", v)
	}
}
