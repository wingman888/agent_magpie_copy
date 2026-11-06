package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLookupStripsVendorPrefixAcrossCatalogs(t *testing.T) {
	writeCatalog(t, `{
	  "openai": {"models": {"gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5",
	              "reasoning_options":[{"type":"effort","values":["low","high"]}]}}},
	  "deepseek": {"models": {"deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat"}}}
	}`)
	if got := ProviderList("openai, deepseek"); len(got) != 2 {
		t.Fatalf("ProviderList: %+v", got)
	}
	cats := []string{"openai", "deepseek"}
	if m, ok := Lookup(cats, "deepseek/deepseek-chat"); !ok || m.Name != "DeepSeek Chat" {
		t.Fatalf("prefixed id: %+v %v", m, ok)
	}
	if m, ok := Lookup(cats, "gpt-5.5"); !ok || len(m.Efforts) != 2 {
		t.Fatalf("plain id: %+v %v", m, ok)
	}
	if _, ok := Lookup(cats, "vendor/unknown"); ok {
		t.Fatal("an unknown model was found")
	}
	if _, ok := Lookup([]string{"deepseek"}, "gpt-5.5"); ok {
		t.Fatal("a model from a catalog not listed was found")
	}

	live := []Model{{ID: "deepseek/deepseek-chat"}, {ID: "gpt-5.5"}, {ID: "mystery/x"}}
	out := Decorate(live, ProviderList("openai,deepseek"))
	if out[0].Name != "DeepSeek Chat" || out[1].Name != "GPT-5.5" || len(out[1].Efforts) != 2 {
		t.Fatalf("decorated: %+v", out)
	}
	if out[2].Name != "" || out[2].Efforts != nil {
		t.Fatalf("an unmatched model gained a name or levels: %+v", out[2])
	}
}

func TestFetchWithSendsHeaders(t *testing.T) {
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()
	ms, err := FetchWith(context.Background(), srv.URL+"/v1", "sk-1", false, map[string]string{"X-Org-Id": "acme"})
	if err != nil || len(ms) != 1 {
		t.Fatalf("FetchWith: %v %+v", err, ms)
	}
	if seen.Get("X-Org-Id") != "acme" || seen.Get("Authorization") != "Bearer sk-1" {
		t.Fatalf("request headers: %v", seen)
	}
}
