package gui

import (
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// TestFixFinalKeepSavedCustom: a custom provider keeps its saved headers and
// icon when the editor sends neither.
func TestFixFinalKeepSavedCustom(t *testing.T) {
	old := &provider.Provider{ID: "relay", Icon: "file:0123456789abcdef.png",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	in := provider.Provider{ID: "relay", Name: "Relay"}
	keepSaved(&in, old)
	if in.Icon != old.Icon {
		t.Errorf("icon = %q, want %q", in.Icon, old.Icon)
	}
	if !reflect.DeepEqual(in.Headers, old.Headers) {
		t.Errorf("headers = %v, want %v", in.Headers, old.Headers)
	}
}

// TestFixFinalKeepSavedSentValues: values the editor did send are not overwritten.
func TestFixFinalKeepSavedSentValues(t *testing.T) {
	old := &provider.Provider{ID: "relay", Icon: "file:0123456789abcdef.png",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	in := provider.Provider{ID: "relay", Icon: "deepseek-color",
		Headers: map[string]string{"X-Other": "v"}}
	keepSaved(&in, old)
	if in.Icon != "deepseek-color" {
		t.Errorf("icon = %q, want deepseek-color", in.Icon)
	}
	if !reflect.DeepEqual(in.Headers, map[string]string{"X-Other": "v"}) {
		t.Errorf("headers = %v, want only the sent ones", in.Headers)
	}
}

// TestFixFinalKeepSavedSkipsPresetAndAccount: presets and signed-in accounts
// are left to their own rules, so nothing is copied onto the incoming value.
func TestFixFinalKeepSavedSkipsPresetAndAccount(t *testing.T) {
	cases := map[string]*provider.Provider{
		"preset":  {ID: "deepseek", Preset: "deepseek", Icon: "deepseek-color", Headers: map[string]string{"X-A": "1"}},
		"account": {ID: "codex", Account: &provider.Account{}, Icon: "generic"},
	}
	for name, old := range cases {
		in := provider.Provider{ID: old.ID}
		keepSaved(&in, old)
		if in.Icon != "" || in.Headers != nil {
			t.Errorf("%s: got icon %q headers %v, want both untouched", name, in.Icon, in.Headers)
		}
	}
}

// TestFixFinalKeepSavedNoOld: a brand-new provider is not touched.
func TestFixFinalKeepSavedNoOld(t *testing.T) {
	in := provider.Provider{ID: "new"}
	keepSaved(&in, nil)
	if in.Icon != "" || in.Headers != nil {
		t.Errorf("got icon %q headers %v, want none", in.Icon, in.Headers)
	}
}
