package gateway

import (
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestDeveloperAsSystemRewritesOnlyRoleL3x(t *testing.T) {
	in := `{"model":"m1","stream":true,"temperature":0.2,"messages":[{"role":"developer","content":"be <brief> & exact"},{"role":"user","content":"hi"}]}`
	out := developerAsSystemL3x([]byte(in))
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("not JSON: %v: %s", err, out)
	}
	msgs := got["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be <brief> & exact" {
		t.Errorf("developer message: %v", first)
	}
	if msgs[1].(map[string]any)["role"] != "user" {
		t.Errorf("user message changed: %v", msgs[1])
	}
	if got["model"] != "m1" || got["stream"] != true || got["temperature"] != 0.2 {
		t.Errorf("other fields changed: %v", got)
	}
}

func TestDeveloperAsSystemNoDeveloperIsByteIdenticalL3x(t *testing.T) {
	for _, in := range []string{
		`{"model":"m1","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`,
		`{"model":"m1","messages":[]}`,
		`{"model":"m1"}`,
		`not json`,
	} {
		if out := developerAsSystemL3x([]byte(in)); string(out) != in {
			t.Errorf("changed body without developer:\n in: %s\nout: %s", in, out)
		}
	}
}

func TestChatPassthroughSendsSystemNotDeveloperL3x(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"ok"}}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/chat/completions", `{"model":"fake/m1","stream":true,
	  "messages":[{"role":"developer","content":"rules"},{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up struct {
		Messages []map[string]any `json:"messages"`
		Stream   bool             `json:"stream"`
	}
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("upstream body: %v: %s", err, f.got)
	}
	if len(up.Messages) != 2 || up.Messages[0]["role"] != "system" || up.Messages[0]["content"] != "rules" {
		t.Errorf("upstream messages: %v", up.Messages)
	}
	if up.Messages[1]["role"] != "user" || !up.Stream {
		t.Errorf("upstream rest changed: %s", f.got)
	}
}
