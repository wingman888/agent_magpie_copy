package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchWithDecorateSeesEveryRequest(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("x-test-hook"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	ms, err := FetchWith(context.Background(), srv.URL, "k", false, func(req *http.Request) {
		req.Header.Set("x-test-hook", "yes")
	})
	if err != nil {
		t.Fatalf("FetchWith: %v", err)
	}
	if len(ms) != 1 || ms[0].ID != "m1" {
		t.Fatalf("models: %+v", ms)
	}
	if len(seen) == 0 {
		t.Fatal("no request reached the server")
	}
	for i, v := range seen {
		if v != "yes" {
			t.Errorf("request %d lacked the hook header: %q", i, v)
		}
	}
}

func TestFetchWithoutDecorateAddsNothing(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("x-test-hook")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()

	if _, err := Fetch(context.Background(), srv.URL, "k", false); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got != "" {
		t.Errorf("Fetch sent unexpected header %q", got)
	}
}
