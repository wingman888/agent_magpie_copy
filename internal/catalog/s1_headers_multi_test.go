package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestS1FetchWithSendsCustomHeaders(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	Reset()
	t.Cleanup(Reset)
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("X-Org-Id")+"|"+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"s1-model"}]}`))
	}))
	defer srv.Close()

	ms, err := FetchWith(context.Background(), srv.URL+"/v1", "sk-s1", false,
		map[string]string{"X-Org-Id": "acme", "authorization": "Token override"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != "s1-model" {
		t.Fatalf("models: %+v", ms)
	}
	if len(got) == 0 || got[0] != "acme|Token override" {
		t.Fatalf("headers seen upstream: %v", got)
	}
}

func TestS1DecorateMatchesRelayPrefixedIDs(t *testing.T) {
	no := false
	known := []Model{{ID: "deepseek-chat", Name: "DeepSeek Chat", Temperature: &no}}
	live := []Model{{ID: "deepseek/deepseek-chat"}, {ID: "vendor/unknown"}}
	out := Decorate(live, known)
	if out[0].ID != "deepseek/deepseek-chat" || out[0].Name != "DeepSeek Chat" || out[0].Temperature == nil {
		t.Errorf("prefixed model not decorated: %+v", out[0])
	}
	if out[1].ID != "vendor/unknown" || out[1].Name != "" {
		t.Errorf("unmatched model should pass through: %+v", out[1])
	}
}

func TestS1ApplyHeadersReplacesAnyCase(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://relay.example/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk")
	ApplyHeaders(req, map[string]string{"authorization": "Token x"})
	if _, ok := req.Header["Authorization"]; ok {
		t.Fatal("default Authorization survived")
	}
	if got := req.Header["authorization"]; len(got) != 1 || got[0] != "Token x" {
		t.Fatalf("authorization: %v", got)
	}
}
