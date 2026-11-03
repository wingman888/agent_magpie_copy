package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestApplyPairsHeaders(t *testing.T) {
	var p provider.Provider
	err := applyPairs(&p, []string{
		"url=https://relay.example.com/v1",
		"key=sk-1",
		"HEADER.X-ORG-id =  padded  ",
		"header.X-Org-Id=acme",
		"Header.X-Org-Id=acme2",
		"header.=dropped",
		"header.   =dropped too",
		"header.Authorization=Token x=y",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "sk-1" || p.Chat != "https://relay.example.com/v1" {
		t.Errorf("plain keys lost: %+v", p)
	}
	want := map[string]string{
		"X-ORG-id":      "padded",
		"X-Org-Id":      "acme2",
		"Authorization": "Token x=y",
	}
	if len(p.Headers) != len(want) {
		t.Fatalf("Headers = %v, want %v", p.Headers, want)
	}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("Headers[%q] = %q, want %q", k, p.Headers[k], v)
		}
	}
}

func TestApplyPairsRejectsUnknownKey(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{"colour=red"}); err == nil {
		t.Error("unknown key accepted")
	}
}

func TestShowProviderListsHeadersSorted(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-1234567890",
		Headers: map[string]string{"X-Org-Id": "acme", "X-ORG-id": "upper", "Authorization": "Token t"}}

	out := captureStdout(t, func() {
		if err := showProvider(p); err != nil {
			t.Fatal(err)
		}
	})
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "header") {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	want := []string{
		"header  Authorization: Token t",
		"header  X-ORG-id: upper",
		"header  X-Org-Id: acme",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("header lines:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestShowPresetHasNoHeaderLines(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	p := provider.Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Chat: "https://api.deepseek.com/v1",
		Key: "sk-1234567890", Headers: map[string]string{"X-Org-Id": "acme"}}
	out := captureStdout(t, func() {
		if err := showProvider(p); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, "header") {
		t.Errorf("preset shows headers:\n%s", out)
	}
}

// captureStdout returns what f writes to standard output.
func captureStdout(t *testing.T, f func()) string {
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
	func() {
		defer func() { os.Stdout = old }()
		f()
		w.Close()
	}()
	return <-done
}
