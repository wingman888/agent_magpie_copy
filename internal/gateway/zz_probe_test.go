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

func zzTrunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "...(cut)"
	}
	return s
}

func zzDump(sb *strings.Builder, label string, f *fake, method, path, body string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "probe-client/1.0")
	New().Handler().ServeHTTP(rec, req)
	fmt.Fprintf(sb, "== %s: %s %s status=%d\n", label, method, path, rec.Code)
	keys := []string{}
	for k := range rec.Header() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(sb, "hdr %s: %s\n", k, strings.Join(rec.Header()[k], ","))
	}
	fmt.Fprintf(sb, "body=%s\n", zzTrunc(rec.Body.String(), 400))
	if f != nil {
		fmt.Fprintf(sb, "upstream path=%s body=%s\n", f.path, zzTrunc(string(f.got), 200))
	}
}

var zzChatSSE = sse(
	`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"hello"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
	`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
	`data: [DONE]`)

const zzChatJSON = `{"id":"c1","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`

// each case gets a fresh upstream (fake records what it received)
func zzRun(t *testing.T, sb *strings.Builder, label, reply, ctype, method, path, body string) {
	f := &fake{t: t, reply: reply, ctype: ctype}
	setup(t, provider.Chat, f)
	zzDump(sb, label, f, method, path, body)
}

func TestZZProbeA(t *testing.T) {
	var sb strings.Builder
	zzRun(t, &sb, "root", zzChatSSE, "", "GET", "/", "")
	zzRun(t, &sb, "v1-models", zzChatSSE, "", "GET", "/v1/models", "")
	zzRun(t, &sb, "v1-models-id", zzChatSSE, "", "GET", "/v1/models/m1", "")
	zzRun(t, &sb, "v1-models-id-missing", zzChatSSE, "", "GET", "/v1/models/nope", "")
	zzRun(t, &sb, "no-such-path", zzChatSSE, "", "GET", "/no-such-path", "")
	zzRun(t, &sb, "chat-nonstream", zzChatJSON, "application/json", "POST", "/v1/chat/completions",
		`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	zzRun(t, &sb, "chat-stream", zzChatSSE, "", "POST", "/v1/chat/completions",
		`{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	zzRun(t, &sb, "chat-unknown-model", zzChatSSE, "", "POST", "/v1/chat/completions",
		`{"model":"nope/x","messages":[{"role":"user","content":"hi"}]}`)
	zzEmit(t, "GATEWAY-A "+sb.String())
}

func TestZZProbeB(t *testing.T) {
	var sb strings.Builder
	zzRun(t, &sb, "responses-nonstream", zzChatSSE, "", "POST", "/v1/responses",
		`{"model":"m1","input":"hi"}`)
	zzRun(t, &sb, "responses-stream", zzChatSSE, "", "POST", "/v1/responses",
		`{"model":"m1","stream":true,"input":"hi"}`)
	zzRun(t, &sb, "messages-nonstream", zzChatSSE, "", "POST", "/v1/messages",
		`{"model":"m1","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	zzRun(t, &sb, "messages-stream", zzChatSSE, "", "POST", "/v1/messages",
		`{"model":"m1","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	zzRun(t, &sb, "responses-unknown-model", zzChatSSE, "", "POST", "/v1/responses",
		`{"model":"nope/x","input":"hi"}`)
	zzRun(t, &sb, "messages-unknown-model", zzChatSSE, "", "POST", "/v1/messages",
		`{"model":"nope/x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	zzRun(t, &sb, "chat-no-model", zzChatSSE, "", "POST", "/v1/chat/completions",
		`{"messages":[{"role":"user","content":"hi"}]}`)
	zzEmit(t, "GATEWAY-B "+sb.String())
}

// zzEmit reports the dump through assert annotations: one line per chunk,
// quotes and backslashes removed so the remote log keeps each chunk whole.
func zzEmit(t *testing.T, s string) {
	s = strings.NewReplacer("\"", "'", "\\", "/", "\n", " | ", "\t", " ").Replace(s)
	const max = 800
	for n := 0; len(s) > 0; n++ {
		c := s
		if len(c) > max {
			c = s[:max]
		}
		s = s[len(c):]
		t.Errorf("PROBE%02d %s", n, c)
	}
}
