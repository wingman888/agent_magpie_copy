package gui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Editing a custom provider from the GUI must not drop its custom headers
// when the form sends no headers field.
func TestSaveKeepsHeadersWhenFormOmitsThem(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// the vendor answers nothing useful; a failed fetch does not fail the save
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer up.Close()

	mux := http.NewServeMux()
	providerRoutes(mux, nil, nil)
	post := func(body string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/provider/save", bytes.NewBufferString(body)))
		return rec.Code
	}

	// first save carries the headers
	if code := post(`{"id":"relay","name":"Relay","chat":"` + up.URL + `/v1","key":"k","headers":{"X-Org-Id":"acme"}}`); code != 200 {
		t.Fatalf("first save: %d", code)
	}
	// second save edits the name only: no headers field at all
	if code := post(`{"id":"relay","name":"Relay 2","chat":"` + up.URL + `/v1","key":"k"}`); code != 200 {
		t.Fatalf("second save: %d", code)
	}
	got, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Relay 2" {
		t.Errorf("name = %q, edit not saved", got.Name)
	}
	if got.Headers["X-Org-Id"] != "acme" || len(got.Headers) != 1 {
		t.Errorf("headers lost on edit: %v", got.Headers)
	}

	// a form that sends an empty headers object clears them on purpose
	if code := post(`{"id":"relay","name":"Relay 2","chat":"` + up.URL + `/v1","key":"k","headers":{}}`); code != 200 {
		t.Fatalf("third save: %d", code)
	}
	got, _ = provider.Find("relay")
	if len(got.Headers) != 0 {
		t.Errorf("explicit empty headers not applied: %v", got.Headers)
	}
}

func TestInheritHeaders(t *testing.T) {
	old := &provider.Provider{Headers: map[string]string{"X-A": "1"}}
	in := provider.Provider{}
	inheritHeaders(&in, old)
	if in.Headers["X-A"] != "1" {
		t.Errorf("nil headers not inherited: %v", in.Headers)
	}
	in = provider.Provider{Headers: map[string]string{}}
	inheritHeaders(&in, old)
	if len(in.Headers) != 0 {
		t.Errorf("explicit empty headers overridden: %v", in.Headers)
	}
	in = provider.Provider{}
	inheritHeaders(&in, nil)
	if in.Headers != nil {
		t.Errorf("new provider got headers: %v", in.Headers)
	}
}
