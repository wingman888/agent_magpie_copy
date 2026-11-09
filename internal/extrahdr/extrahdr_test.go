package extrahdr

import (
	"net/http"
	"testing"
)

func TestHdrtSetOverridesAndKeepsCase(t *testing.T) {
	h := http.Header{}
	h.Set("User-Agent", "magpie")
	h.Set("Authorization", "Bearer k")
	Set(h, map[string]string{"user-agent": "acme-cli", "X-Org-Id": "acme"})

	if _, ok := h["User-Agent"]; ok {
		t.Errorf("default User-Agent should be replaced, got %v", h)
	}
	if got := h["user-agent"]; len(got) != 1 || got[0] != "acme-cli" {
		t.Errorf("user-agent = %v, want [acme-cli] under the typed case", got)
	}
	if got := h["X-Org-Id"]; len(got) != 1 || got[0] != "acme" {
		t.Errorf("X-Org-Id = %v", got)
	}
	if h.Get("Authorization") != "Bearer k" {
		t.Errorf("auth header touched: %v", h)
	}
}

func TestHdrtSetNilIsNoop(t *testing.T) {
	h := http.Header{}
	h.Set("Accept", "application/json")
	Set(h, nil)
	if len(h) != 1 || h.Get("Accept") != "application/json" {
		t.Errorf("header changed: %v", h)
	}
}
