package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Headers must be the last struct field: an empty map is not written, and a
// filled one appears after every other field.
func TestF04HeadersJSONLayout(t *testing.T) {
	empty, err := json.Marshal(Provider{ID: "r", Name: "R", Icon: "generic", Key: "k", Chat: "https://e.x/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), `"headers"`) {
		t.Errorf("empty headers written: %s", empty)
	}
	want := `{"id":"r","name":"R","icon":"generic","key":"k","chat":"https://e.x/v1"}`
	if string(empty) != want {
		t.Errorf("json = %s\nwant   %s", empty, want)
	}

	full, err := json.Marshal(Provider{ID: "r", Name: "R", Key: "k", Chat: "https://e.x/v1",
		Headers: map[string]string{"X-A": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(full), `,"headers":{"X-A":"v"}}`) {
		t.Errorf("headers not last: %s", full)
	}
}

// A provider removed with Delete triggers the same pruning as Save.
func TestF04PruneIconsOnDelete(t *testing.T) {
	f04IsolateIcons(t)
	old := time.Now().Add(-2 * time.Hour)

	used, err := StoreIcon(append([]byte("\x89PNG\r\n\x1a\n"), []byte("used")...))
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "r", Name: "R", Chat: "https://e.x/v1", Key: "k", Icon: used}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "s", Name: "S", Chat: "https://e.x/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	stale, err := StoreIcon(append([]byte("\x89PNG\r\n\x1a\n"), []byte("stale")...))
	if err != nil {
		t.Fatal(err)
	}
	usedPath := filepath.Join(iconsDir(), strings.TrimPrefix(used, "file:"))
	stalePath := filepath.Join(iconsDir(), strings.TrimPrefix(stale, "file:"))
	os.Chtimes(usedPath, old, old)
	os.Chtimes(stalePath, old, old)

	if err := Delete("s"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("unreferenced old icon survived Delete")
	}
	if _, err := os.Stat(usedPath); err != nil {
		t.Errorf("referenced old icon removed by Delete: %v", err)
	}
}

// Built-in icon names and an empty icon are not references to a stored image.
func TestF04BuiltinIconIsNotAReference(t *testing.T) {
	f04IsolateIcons(t)
	ref, err := StoreIcon(append([]byte("\x89PNG\r\n\x1a\n"), []byte("orphan")...))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(iconsDir(), strings.TrimPrefix(ref, "file:"))
	os.Chtimes(path, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour))
	if err := Save(Provider{ID: "t", Name: "T", Chat: "https://e.x/v1", Key: "k", Icon: "deepseek-color"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("orphan icon kept although only a built-in name references the provider")
	}
}
