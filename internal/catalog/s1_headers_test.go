package catalog

import (
	"net/http"
	"testing"
)

func TestS1SetRawHeadersKeepsCaseAndReplaces(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer a")
	SetRawHeaders(h, map[string]string{"authorization": "Bearer b", "X-Org-Id": "acme"})
	if got := h["authorization"]; len(got) != 1 || got[0] != "Bearer b" {
		t.Fatalf("authorization: %#v", got)
	}
	if _, ok := h["Authorization"]; ok {
		t.Fatal("the canonical Authorization header was left behind")
	}
	if got := h["X-Org-Id"]; len(got) != 1 || got[0] != "acme" {
		t.Fatalf("X-Org-Id: %#v", got)
	}
}

func TestS1DecorateMatchesPrefixedIDs(t *testing.T) {
	known := []Model{
		{ID: "deepseek-chat", Name: "DeepSeek Chat", Provider: "deepseek"},
		{ID: "dup", Name: "first"},
		{ID: "dup", Name: "second"},
	}
	live := []Model{
		{ID: "deepseek/deepseek-chat", Name: "deepseek/deepseek-chat"},
		{ID: "vendor/unknown", Name: "vendor/unknown"},
		{ID: "dup"},
	}
	out := Decorate(live, known)
	if out[0].ID != "deepseek/deepseek-chat" || out[0].Name != "DeepSeek Chat" || out[0].Provider != "deepseek" {
		t.Fatalf("prefixed id: %+v", out[0])
	}
	if out[1].ID != "vendor/unknown" || out[1].Name != "vendor/unknown" || out[1].Provider != "" {
		t.Fatalf("unmatched id should pass through: %+v", out[1])
	}
	if out[2].Name != "first" {
		t.Fatalf("the first catalog entry should win: %+v", out[2])
	}
}
