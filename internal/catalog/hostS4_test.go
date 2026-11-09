package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHostS4FetchWithSendsHost checks that FetchWith sends a user Host header
// on the wire (req.Host), and that the server sees the header's exact value.
func TestHostS4FetchWithSendsHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "tenant.example.com" {
			http.Error(w, "wrong host "+r.Host, http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"data":[{"id":"deepseek-chat"}]}`))
	}))
	defer srv.Close()

	ms, err := FetchWith(context.Background(), srv.URL+"/v1", "sk", false,
		map[string]string{"Host": "tenant.example.com"})
	if err != nil {
		t.Fatalf("FetchWith: %v", err)
	}
	if len(ms) != 1 || ms[0].ID != "deepseek-chat" {
		t.Fatalf("models = %+v", ms)
	}
}

// TestHostS4FetchWithoutHostUnchanged checks the plain Fetch path still
// reaches the server with its own host (no custom headers).
func TestHostS4FetchWithoutHostUnchanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"deepseek-chat"}]}`))
	}))
	defer srv.Close()

	ms, err := Fetch(context.Background(), srv.URL+"/v1", "sk", false)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("models = %+v", ms)
	}
}
