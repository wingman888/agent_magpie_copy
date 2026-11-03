package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// recordTransport answers every request with a one-model list and keeps the
// requests it saw, so the model-list path can be checked without a network.
type recordTransport struct {
	headers []http.Header
	urls    []string
}

func (r *recordTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.urls = append(r.urls, req.URL.String())
	r.headers = append(r.headers, req.Header.Clone())
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"m1"}]}`)),
		Request:    req,
	}, nil
}

func TestFetchModelsOpenCodeSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	rt := &recordTransport{}
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rt
	defer func() { http.DefaultClient.Transport = old }()

	oc := Provider{ID: "opencode-go", Name: "OpenCode Go", Key: "k", Chat: "https://opencode.ai/zen/go/v1"}
	if _, err := oc.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := oc.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rt.headers) != 2 {
		t.Fatalf("requests: %v", rt.urls)
	}
	first := rt.headers[0].Get("x-opencode-session")
	if !strings.HasPrefix(first, "magpie-") || len(first) <= len("magpie-") {
		t.Fatalf("OpenCode model list request: session %q", first)
	}
	if again := rt.headers[1].Get("x-opencode-session"); again != first {
		t.Errorf("same provider got %q then %q", first, again)
	}

	other := Provider{ID: "opencode-zen", Name: "OpenCode Zen", Key: "k", Chat: "https://opencode.ai/zen/v1"}
	rt.headers = nil
	if _, err := other.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := rt.headers[0].Get("x-opencode-session"); got == first {
		t.Errorf("different provider shares session %q", got)
	}

	ds := Provider{ID: "ds", Name: "DeepSeek", Key: "k", Chat: "https://api.deepseek.com/v1"}
	rt.headers = nil
	if _, err := ds.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rt.headers) == 0 {
		t.Fatal("no request reached the transport")
	}
	for _, h := range rt.headers {
		if _, ok := h["X-Opencode-Session"]; ok {
			t.Errorf("non-OpenCode model list request received x-opencode-session: %v", h)
		}
	}
}
