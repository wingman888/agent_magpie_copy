package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// recordTransport answers every request with a model list and remembers the
// headers it saw, per host.
type recordTransport struct {
	headers map[string]http.Header
}

func (rt *recordTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.headers[req.URL.Host] = req.Header.Clone()
	body := `{"data":[{"id":"m1"}]}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func TestFetchSendsOpenCodeSessionOnlyToOpenCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	rt := &recordTransport{headers: map[string]http.Header{}}
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rt
	defer func() { http.DefaultClient.Transport = old }()

	oc := Provider{ID: "oc-models-test", Name: "OC", Key: "k", Chat: "https://opencode.ai/zen/go/v1"}
	if _, err := oc.Fetch(context.Background()); err != nil {
		t.Fatalf("opencode Fetch: %v", err)
	}
	h := rt.headers["opencode.ai"]
	if h == nil {
		t.Fatal("no request reached opencode.ai")
	}
	sid := h.Get("x-opencode-session")
	if !strings.HasPrefix(sid, "magpie-") {
		t.Errorf("model list request session = %q, want magpie- prefix", sid)
	}
	// stable across calls
	if _, err := oc.Fetch(context.Background()); err != nil {
		t.Fatalf("second opencode Fetch: %v", err)
	}
	if again := rt.headers["opencode.ai"].Get("x-opencode-session"); again != sid {
		t.Errorf("session changed between fetches: %q then %q", sid, again)
	}

	other := Provider{ID: "other-models-test", Name: "Other", Key: "k", Chat: "https://api.example.com/v1"}
	if _, err := other.Fetch(context.Background()); err != nil {
		t.Fatalf("other Fetch: %v", err)
	}
	if v := rt.headers["api.example.com"].Get("x-opencode-session"); v != "" {
		t.Errorf("non-OpenCode provider received x-opencode-session %q", v)
	}
}

// The hook passed to catalog.FetchWith must reach every list request, and
// Fetch without a hook must send nothing extra.
func TestCatalogFetchWithHookSeesEveryRequest(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("x-test-hook"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()

	ms, err := catalog.FetchWith(context.Background(), srv.URL, "k", false, func(req *http.Request) {
		req.Header.Set("x-test-hook", "yes")
	})
	if err != nil || len(ms) != 1 || ms[0].ID != "m1" {
		t.Fatalf("FetchWith: %v %+v", err, ms)
	}
	if len(seen) == 0 {
		t.Fatal("no request reached the server")
	}
	for i, v := range seen {
		if v != "yes" {
			t.Errorf("request %d lacked the hook header: %q", i, v)
		}
	}

	seen = nil
	if _, err := catalog.Fetch(context.Background(), srv.URL, "k", false); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	for i, v := range seen {
		if v != "" {
			t.Errorf("Fetch request %d sent unexpected header %q", i, v)
		}
	}
}
