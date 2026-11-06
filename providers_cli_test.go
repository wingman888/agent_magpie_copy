package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// isolate points config and home at fresh temp dirs so tests never touch the
// real providers.json or agent configs.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	ferr := fn()
	w.Close()
	os.Stdout = old
	return <-done, ferr
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func pngBytes(tag string) []byte {
	return []byte("\x89PNG\r\n\x1a\n" + tag)
}

// fakeUpstream answers every request with 404 so saveNew's model fetch fails fast.
func fakeUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestApplyPairsHeaders(t *testing.T) {
	isolate(t)
	p := provider.Provider{Name: "My Relay"}
	err := applyPairs(&p, []string{
		"url=https://relay.example.com/v1",
		"key=sk",
		"HEADER.X-ORG-id =  padded  ",
		"header.X-Org-Id=acme",
		"Header.Authorization=Bearer abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-ORG-id": "padded", "X-Org-Id": "acme", "Authorization": "Bearer abc"}
	if len(p.Headers) != len(want) {
		t.Fatalf("headers = %v, want %v", p.Headers, want)
	}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("header %q = %q, want %q", k, p.Headers[k], v)
		}
	}
}

func TestAddCustomWithHeaderSavesAndShows(t *testing.T) {
	isolate(t)
	srv := fakeUpstream(t)
	if _, err := captureStdout(t, func() error {
		return addProvider([]string{"My Relay", "url=" + srv.URL + "/v1", "key=sk", "header.X-Org-Id=acme", "header.X-Org-Id=acme"})
	}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("my-relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Headers["X-Org-Id"] != "acme" {
		t.Fatalf("saved headers = %v", p.Headers)
	}
	out, err := captureStdout(t, func() error { return showProvider(*p) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "header  X-Org-Id: acme") {
		t.Errorf("show output missing header line:\n%s", out)
	}
}

func TestShowHeadersSortedBytewise(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{
		ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk",
		Headers: map[string]string{"X-Org-Id": "acme", "X-ORG-id": "b", "Authorization": "Bearer x"},
	}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return showProvider(*p) })
	if err != nil {
		t.Fatal(err)
	}
	a := strings.Index(out, "header  Authorization: Bearer x")
	b := strings.Index(out, "header  X-ORG-id: b")
	c := strings.Index(out, "header  X-Org-Id: acme")
	if a < 0 || b < 0 || c < 0 || !(a < b && b < c) {
		t.Errorf("headers missing or unsorted (%d,%d,%d):\n%s", a, b, c, out)
	}
}

func TestAddWithRejectedIconCreatesNothing(t *testing.T) {
	isolate(t)
	srv := fakeUpstream(t)
	text := writeTemp(t, "logo.txt", []byte("just some text"))
	_, err := captureStdout(t, func() error {
		return addProvider([]string{"My Relay", "url=" + srv.URL + "/v1", "key=sk", "icon=" + text})
	})
	if err == nil {
		t.Fatal("expected error for text icon")
	}
	if _, err := provider.Find("my-relay"); err == nil {
		t.Fatal("provider was created despite rejected icon")
	}
}

func TestProviderIconFileBuiltinAndReset(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk"}); err != nil {
		t.Fatal(err)
	}
	img := writeTemp(t, "logo.png", pngBytes("logo-one"))

	run := func(args ...string) error {
		_, err := captureStdout(t, func() error { return providerCmd(append([]string{"provider", "icon"}, args...)) })
		return err
	}
	iconOf := func() string {
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		return p.Icon
	}

	if err := run("relay", img); err != nil {
		t.Fatal(err)
	}
	ref := iconOf()
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") {
		t.Fatalf("icon = %q, want file:*.png", ref)
	}
	if err := run("relay", img); err != nil {
		t.Fatal(err)
	}
	if got := iconOf(); got != ref {
		t.Fatalf("second set gave %q, want %q", got, ref)
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(provider.Path()), "icons"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("icons dir has %d files, want 1", len(entries))
	}

	if err := run("relay", "deepseek-color"); err != nil {
		t.Fatal(err)
	}
	if got := iconOf(); got != "deepseek-color" {
		t.Fatalf("built-in icon = %q", got)
	}

	if err := run("relay", ""); err != nil {
		t.Fatal(err)
	}
	if got := iconOf(); got != "" {
		t.Fatalf("reset icon = %q, want empty", got)
	}
}

func TestProviderIconRejectedKeepsOld(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk", Icon: "deepseek-color"}); err != nil {
		t.Fatal(err)
	}
	empty := writeTemp(t, "empty.png", nil)
	text := writeTemp(t, "notes.txt", []byte("hello world"))
	big := writeTemp(t, "big.png", append(pngBytes("big"), make([]byte, provider.MaxIcon)...))
	for _, bad := range []string{empty, text, big, filepath.Join(t.TempDir(), "missing.png")} {
		if _, err := captureStdout(t, func() error { return providerCmd([]string{"provider", "icon", "relay", bad}) }); err == nil {
			t.Errorf("icon %s: expected error", bad)
		}
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		if p.Icon != "deepseek-color" {
			t.Errorf("icon %s changed the icon to %q", bad, p.Icon)
		}
	}
}

func TestProviderIconSVGAccepted(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk"}); err != nil {
		t.Fatal(err)
	}
	svg := writeTemp(t, "logo.txt", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))
	if _, err := captureStdout(t, func() error { return providerCmd([]string{"provider", "icon", "relay", svg}) }); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("relay")
	if !strings.HasPrefix(p.Icon, "file:") || !strings.HasSuffix(p.Icon, ".svg") {
		t.Fatalf("icon = %q, want file:*.svg", p.Icon)
	}
}

func TestProviderIconRefusesPresetAndAccount(t *testing.T) {
	isolate(t)
	pr, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	pr.Key = "sk"
	if err := provider.Save(pr); err != nil {
		t.Fatal(err)
	}
	before, _ := provider.Find("deepseek")
	_, err = captureStdout(t, func() error { return providerCmd([]string{"provider", "icon", "deepseek", "generic"}) })
	if err == nil {
		t.Fatal("expected error for a preset")
	}
	if !strings.Contains(err.Error(), "only custom providers can have an icon") {
		t.Errorf("error %q lacks the custom-only message", err)
	}
	after, _ := provider.Find("deepseek")
	if after.Icon != before.Icon {
		t.Errorf("preset icon changed from %q to %q", before.Icon, after.Icon)
	}
}

func TestAddPresetIconRefused(t *testing.T) {
	isolate(t)
	_, err := captureStdout(t, func() error {
		return addProvider([]string{"deepseek", "sk", "icon=deepseek-color"})
	})
	if err == nil || !strings.Contains(err.Error(), "only custom providers can have an icon") {
		t.Fatalf("err = %v, want custom-only refusal", err)
	}
}
