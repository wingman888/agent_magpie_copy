package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The connectivity test and the model list fetch both reach the upstream with
// the custom provider's headers; a preset's requests carry none.
func TestProbeAndFetchCarryCustomHeaders(t *testing.T) {
	isolateConfig(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	var mu sync.Mutex
	var seen []string // "path org-id" per request
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.Header.Get("X-Org-Id"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer up.Close()

	custom := Provider{ID: "org", Name: "Org", Chat: up.URL + "/v1", Key: "sk-test", Models: []string{"m1"},
		Headers: map[string]string{"X-Org-Id": "acme"}}
	res := custom.Test(context.Background())
	if len(res) == 0 || !res[0].OK {
		t.Fatalf("connectivity test failed: %+v", res)
	}
	custom.Fetch(context.Background())

	mu.Lock()
	got := append([]string(nil), seen...)
	seen = nil
	mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("upstream saw %v, want the test and the model list request", got)
	}
	for _, s := range got {
		if !strings.HasSuffix(s, " acme") {
			t.Fatalf("request %q did not carry the custom header", s)
		}
	}

	preset := custom
	preset.Preset = "deepseek"
	preset.ID = "deepseek"
	preset.Catalog = ""
	preset.Models = []string{"m1"}
	preset.Test(context.Background())
	preset.Fetch(context.Background())
	mu.Lock()
	got = append([]string(nil), seen...)
	mu.Unlock()
	if len(got) == 0 {
		t.Fatal("the preset made no requests")
	}
	for _, s := range got {
		if strings.HasSuffix(s, " acme") {
			t.Fatalf("preset request %q carried a custom header", s)
		}
	}
}
