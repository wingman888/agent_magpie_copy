package gateway

import (
	"net/http"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The OpenCode session header follows the host of each upstream request, so a
// provider that mixes opencode.ai with another host only sends it to opencode.ai.
func TestOCHostHeaderFollowsUpstreamURL(t *testing.T) {
	p := provider.Provider{ID: "mix", Name: "Mix", Key: "k",
		Chat:      "https://api.x.com/v1",
		Anthropic: "https://opencode.ai/zen"}
	in := http.Header{}
	in.Set("x-session-id", "agent-9")

	chat := ocForward(t, p, provider.Chat, ocChatBody(`{"role":"user","content":"hi"}`), in)
	if v, ok := chat.header["X-Opencode-Session"]; ok {
		t.Errorf("request to api.x.com got x-opencode-session %v", v)
	}

	anth := ocForward(t, p, provider.Anthropic, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, in)
	if got := anth.header.Get("x-opencode-session"); got != "agent-9" {
		t.Errorf("request to opencode.ai: x-opencode-session = %q, want agent-9", got)
	}
}
