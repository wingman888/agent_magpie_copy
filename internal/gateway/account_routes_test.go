package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// copilotRouteBackend is a Copilot account whose chat backend answers with an
// event stream whenever the request asks for one, as the real one does. It
// records what it received so the routing can be checked.
func copilotRouteBackend(t *testing.T) (gotStream *bool, gotInitiator *string) {
	t.Helper()
	isolateHome(t)
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	apps := `{"github.com:octo":{"user":"octo","oauth_token":"gho_route"}}`
	if err := os.WriteFile(filepath.Join(dir, "apps.json"), []byte(apps), 0o600); err != nil {
		t.Fatal(err)
	}

	stream, initiator := new(bool), new(string)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"token":"tid_route","expires_at":%d,"endpoints":{"api":%q}}`,
			time.Now().Unix()+60, srv.URL+"/copilot")
	})
	mux.HandleFunc("/copilot/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Stream bool `json:"stream"`
		}
		json.Unmarshal(raw, &body)
		*stream, *initiator = body.Stream, r.Header.Get("X-Initiator")
		if !body.Stream {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"claude-sonnet-4.5","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"claude-sonnet-4.5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"claude-sonnet-4.5\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	})
	old := provider.CopilotTokenURL
	provider.CopilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { provider.CopilotTokenURL = old })
	return stream, initiator
}

func TestCopilotAnthropicMessagesIsTranslated(t *testing.T) {
	gotStream, gotInitiator := copilotRouteBackend(t)

	rec := gatewayPost(t, "/v1/messages", `{"model":"copilot/claude-sonnet-4.5","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "pong") {
		t.Fatalf("reply carries no text: %s", rec.Body)
	}
	if !*gotStream {
		t.Errorf("the Copilot backend was not streamed")
	}
	if *gotInitiator != "user" {
		t.Errorf("X-Initiator = %q, want user", *gotInitiator)
	}
}

func TestCopilotChatCompletionsStreamIsRelayed(t *testing.T) {
	gotStream, gotInitiator := copilotRouteBackend(t)

	rec := gatewayPost(t, "/v1/chat/completions", `{"model":"copilot/claude-sonnet-4.5","stream":true,"messages":[{"role":"user","content":"ping"},{"role":"assistant","content":"ok"},{"role":"tool","tool_call_id":"t1","content":"r"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "pong") {
		t.Fatalf("stream carries no text: %s", rec.Body)
	}
	if !*gotStream {
		t.Errorf("stream flag was lost on the way to the backend")
	}
	if *gotInitiator != "agent" {
		t.Errorf("X-Initiator = %q, want agent for a tool reply", *gotInitiator)
	}
}
