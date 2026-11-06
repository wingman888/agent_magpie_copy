package gateway

import (
	"fmt"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// TEMPORARY probe: behavioural dump for baseline comparison. Delete before finishing.

func zzDump(sb *strings.Builder, label string, f *fake, method, path, body string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "probe-client/1.0")
	New().Handler().ServeHTTP(rec, req)
	fmt.Fprintf(sb, "== %s: %s %s\n", label, method, path)
	fmt.Fprintf(sb, "status=%d\n", rec.Code)
	keys := []string{}
	for k := range rec.Header() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(sb, "hdr %s: %s\n", k, strings.Join(rec.Header()[k], ","))
	}
	fmt.Fprintf(sb, "body=%s\n", rec.Body.String())
	if f != nil {
		fmt.Fprintf(sb, "upstream path=%s body=%s\n", f.path, f.got)
	}
}

func TestZZProbe(t *testing.T) {
	var sb strings.Builder
	chatSSE := sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"hello"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
		`data: [DONE]`)
	chatJSON := `{"id":"c1","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`

	// each case gets a fresh upstream (fake records what it received)
	run := func(label string, reply, ctype, method, path, body string) {
		f := &fake{t: t, reply: reply, ctype: ctype}
		setup(t, provider.Chat, f)
		zzDump(&sb, label, f, method, path, body)
	}
	run("root", chatSSE, "", "GET", "/", "")
	run("v1-models", chatSSE, "", "GET", "/v1/models", "")
	run("v1-models-id", chatSSE, "", "GET", "/v1/models/m1", "")
	run("v1-models-id-missing", chatSSE, "", "GET", "/v1/models/nope", "")
	run("no-such-path", chatSSE, "", "GET", "/no-such-path", "")
	run("chat-nonstream", chatJSON, "application/json", "POST", "/v1/chat/completions",
		`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	run("chat-stream", chatSSE, "", "POST", "/v1/chat/completions",
		`{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	run("responses-nonstream", chatSSE, "", "POST", "/v1/responses",
		`{"model":"m1","input":"hi"}`)
	run("responses-stream", chatSSE, "", "POST", "/v1/responses",
		`{"model":"m1","stream":true,"input":"hi"}`)
	run("messages-nonstream", chatSSE, "", "POST", "/v1/messages",
		`{"model":"m1","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	run("messages-stream", chatSSE, "", "POST", "/v1/messages",
		`{"model":"m1","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	run("chat-unknown-model", chatSSE, "", "POST", "/v1/chat/completions",
		`{"model":"nope/x","messages":[{"role":"user","content":"hi"}]}`)
	run("responses-unknown-model", chatSSE, "", "POST", "/v1/responses",
		`{"model":"nope/x","input":"hi"}`)
	run("messages-unknown-model", chatSSE, "", "POST", "/v1/messages",
		`{"model":"nope/x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	// unknown-model via the non-JSON path (missing model field)
	run("chat-no-model", chatSSE, "", "POST", "/v1/chat/completions",
		`{"messages":[{"role":"user","content":"hi"}]}`)

	t.Fatalf("PROBE-GATEWAY-DUMP\n%s", sb.String())
}
