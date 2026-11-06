package catalog

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestL1FetchWithSendsCustomHeaders(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("X-Org-Id") + " " + r.URL.Path
		io.WriteString(w, `{"data":[{"id":"gpt-x"}]}`)
	}))
	defer srv.Close()
	ms, err := FetchWith(context.Background(), srv.URL+"/v1", "k", false, map[string]string{"X-Org-Id": "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != "gpt-x" {
		t.Fatalf("models = %+v", ms)
	}
	if g := <-got; g != "acme /v1/models" {
		t.Fatalf("request saw %q", g)
	}
	// Fetch keeps its signature and sends no extra headers
	if _, err := Fetch(context.Background(), srv.URL+"/v1", "k", false); err != nil {
		t.Fatal(err)
	}
	if g := <-got; g != " /v1/models" {
		t.Fatalf("plain Fetch saw %q", g)
	}
}

func TestL1DecorateMatchesPrefixedIDByLastPart(t *testing.T) {
	known := []Model{{ID: "deepseek-chat", Name: "DeepSeek Chat"}}
	out := Decorate([]Model{{ID: "deepseek/deepseek-chat"}, {ID: "unknown/x"}}, known)
	if out[0].ID != "deepseek/deepseek-chat" || out[0].Name != "DeepSeek Chat" {
		t.Fatalf("prefixed: %+v", out[0])
	}
	if out[1].Name != "" {
		t.Fatalf("unmatched gained a name: %+v", out[1])
	}
}
