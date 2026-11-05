package provider

import (
	"context"
	"net/http"
	"testing"
)

func TestCleanHeadersTrimsAndDropsEmptyNames(t *testing.T) {
	p := normalize(Provider{ID: "x", Chat: "https://x.example.com/v1", Headers: map[string]string{
		" X-Org-Id ": " acme ",
		"":           "dropped",
		"   ":        "dropped too",
		"x-Gw":       "v",
	}})
	want := map[string]string{"X-Org-Id": "acme", "x-Gw": "v"}
	if len(p.Headers) != len(want) {
		t.Fatalf("Headers = %v, want %v", p.Headers, want)
	}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("Headers[%q] = %q, want %q", k, p.Headers[k], v)
		}
	}

	empty := normalize(Provider{ID: "y", Chat: "https://y.example.com/v1", Headers: map[string]string{"  ": "x"}})
	if empty.Headers != nil {
		t.Errorf("a map with only empty names should normalise to nil, got %v", empty.Headers)
	}
}

func TestCustomHeadersKeepCaseAndOverrideAuth(t *testing.T) {
	p := Provider{ID: "x", Key: "sk-test", Chat: "https://api.example.com/v1",
		Headers: map[string]string{"x-org-id": "acme", "Authorization": "Custom token"}}
	req, err := http.NewRequest(http.MethodPost, "https://api.example.com/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if v := req.Header["x-org-id"]; len(v) != 1 || v[0] != "acme" {
		t.Errorf(`req.Header["x-org-id"] = %v, want [acme] sent with its own case`, v)
	}
	if _, ok := req.Header["X-Org-Id"]; ok {
		t.Error("header name should not be canonicalised")
	}
	if v := req.Header.Values("Authorization"); len(v) != 1 || v[0] != "Custom token" {
		t.Errorf("Authorization = %v, want the custom header to override the key", v)
	}
}

func TestPresetsAndAccountsIgnoreHeaders(t *testing.T) {
	hdrs := map[string]string{"X-Org-Id": "acme"}
	preset := Provider{ID: "deepseek", Preset: "deepseek", Key: "sk", Chat: "https://api.deepseek.com/v1", Headers: hdrs}
	if got := preset.ExtraHeaders(); got != nil {
		t.Errorf("preset ExtraHeaders = %v, want none", got)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.deepseek.com/v1/chat/completions", nil)
	if err := preset.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Header["X-Org-Id"]; ok {
		t.Error("a preset must not send custom headers")
	}
	account := Provider{ID: "acct", Headers: hdrs, Account: &Account{}}
	if got := account.ExtraHeaders(); got != nil {
		t.Errorf("account ExtraHeaders = %v, want none", got)
	}
	custom := Provider{ID: "custom", Headers: hdrs}
	if got := custom.ExtraHeaders(); got["X-Org-Id"] != "acme" {
		t.Errorf("custom ExtraHeaders = %v, want X-Org-Id", got)
	}
}

func TestCatalogIDsSplitsList(t *testing.T) {
	got := CatalogIDs(" openai, deepseek ,,")
	if len(got) != 2 || got[0] != "openai" || got[1] != "deepseek" {
		t.Errorf("CatalogIDs = %v, want [openai deepseek]", got)
	}
	if n := normalize(Provider{ID: "c", Chat: "https://c.example.com/v1", Catalog: " openai ,deepseek"}).Catalog; n != "openai,deepseek" {
		t.Errorf("normalised catalog = %q, want openai,deepseek", n)
	}
}
