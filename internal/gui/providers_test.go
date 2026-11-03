package gui

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A preset provider's save keeps the fallback list the editor sent, keeps
// the saved one when the form leaves the key out, and clears it on [].
func TestPresetSaveKeepsFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)

	// the key is already stored, so the save does not ask the vendor for models
	pr, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	pr.Key = "sk-test"
	if err := provider.Save(pr); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	providerRoutes(mux, nil, nil)
	save := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("save %s: status %d: %s", body, rec.Code, rec.Body.String())
		}
	}
	fallback := func() []string {
		t.Helper()
		p, err := provider.Find("deepseek")
		if err != nil {
			t.Fatal(err)
		}
		return p.Fallback
	}

	save(`{"id":"deepseek","preset":"deepseek","key":"sk-test","models":["deepseek-chat"],"fallback":["openai/gpt-4o"," ","openai/gpt-4o"]}`)
	if want := []string{"openai/gpt-4o"}; !reflect.DeepEqual(fallback(), want) {
		t.Fatalf("preset save dropped fallback: got %q, want %q", fallback(), want)
	}

	save(`{"id":"deepseek","preset":"deepseek","key":"sk-test","models":["deepseek-chat"]}`)
	if want := []string{"openai/gpt-4o"}; !reflect.DeepEqual(fallback(), want) {
		t.Fatalf("save without fallback key wiped it: got %q, want %q", fallback(), want)
	}

	save(`{"id":"deepseek","preset":"deepseek","key":"sk-test","models":["deepseek-chat"],"fallback":[]}`)
	if got := fallback(); len(got) != 0 {
		t.Fatalf("explicit empty fallback not cleared: got %q", got)
	}
}
