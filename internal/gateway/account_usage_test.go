package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// A non-streamed Codex reply whose backend sent no usage still carries a usage
// object, all zero.
func TestCodexNonStreamWithoutUsageReportsZeroUsage(t *testing.T) {
	home := isolateHome(t)
	writeCodexLogin(t, home)

	const sse = `event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}]}}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse)
	}))
	t.Cleanup(srv.Close)
	old := provider.CodexBase
	provider.CodexBase = srv.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })

	rec := gatewayPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("reply is not JSON: %v: %s", err, rec.Body)
	}
	if out["object"] != "response" {
		t.Errorf("object = %v, want response", out["object"])
	}
	u, ok := out["usage"].(map[string]any)
	if !ok {
		t.Fatalf("reply has no usage object: %s", rec.Body)
	}
	for _, k := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		if v, ok := u[k].(float64); !ok || v != 0 {
			t.Errorf("usage.%s = %v, want 0", k, u[k])
		}
	}
}
