package gateway

import (
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// TestFixGem: a Gemini client's history thoughts go back to a non-DeepSeek
// Chat upstream as reasoning_content on the assistant message.
func TestFixGem(t *testing.T) {
	f := &fake{t: t, reply: gemSrvChatText()}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	  "contents":[
	    {"role":"user","parts":[{"text":"think about it"}]},
	    {"role":"model","parts":[
	      {"text":"earlier reasoning steps","thought":true},
	      {"text":"earlier answer"}]},
	    {"role":"user","parts":[{"text":"and now?"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	up := gemSrvJSON(t, f.got)
	msgs := gemSrvList(t, up, "messages")
	var asst map[string]any
	for _, m := range msgs {
		if mm := m.(map[string]any); mm["role"] == "assistant" {
			asst = mm
		}
	}
	if asst == nil {
		t.Fatalf("no assistant message in upstream request: %v", msgs)
	}
	if asst["reasoning_content"] != "earlier reasoning steps" {
		t.Fatalf("assistant reasoning_content = %v, want the history thought", asst["reasoning_content"])
	}
}
