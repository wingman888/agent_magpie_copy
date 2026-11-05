package catalog

import (
	"net/http"
	"reflect"
	"testing"
)

func TestMatchStripsProviderPrefix(t *testing.T) {
	known := []Model{{ID: "gpt-5.5", Name: "GPT-5.5"}, {ID: "deepseek-chat", Name: "DeepSeek Chat"}}
	for id, want := range map[string]string{
		"deepseek/deepseek-chat": "deepseek-chat",
		"gpt-5.5":                "gpt-5.5",
		"openai/gpt-5.5":         "gpt-5.5",
	} {
		m, ok := Match(known, id)
		if !ok || m.ID != want {
			t.Errorf("Match(%q) = %+v, %v; want %q", id, m, ok, want)
		}
	}
	if _, ok := Match(known, "unknown/thing"); ok {
		t.Error("unknown model matched")
	}
	if _, ok := Match(known, "deepseek/"); ok {
		t.Error("empty tail matched")
	}
}

func TestEffortsFollowPrefixedID(t *testing.T) {
	models := []Model{{ID: "gpt-5.5", Efforts: []string{"low", "high"}}}
	if got := Efforts(models, "openai/gpt-5.5"); !reflect.DeepEqual(got, []string{"low", "high"}) {
		t.Errorf("Efforts(prefixed) = %q", got)
	}
	if got := Efforts(models, "gpt-5.5"); !reflect.DeepEqual(got, []string{"low", "high"}) {
		t.Errorf("Efforts(exact) = %q", got)
	}
	if got := Efforts(models, "other/x"); got != nil {
		t.Errorf("Efforts(unmatched) = %q", got)
	}
}

func TestSetHeadersKeepsCaseAndReplaces(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer default")
	SetHeaders(h, map[string]string{"x-org-id": "acme", "authorization": "Bearer custom"})
	if got := h["x-org-id"]; !reflect.DeepEqual(got, []string{"acme"}) {
		t.Errorf("x-org-id = %q", got)
	}
	if got := h["authorization"]; !reflect.DeepEqual(got, []string{"Bearer custom"}) {
		t.Errorf("authorization = %q", got)
	}
	if _, ok := h["Authorization"]; ok {
		t.Errorf("default Authorization kept: %v", h)
	}
}
