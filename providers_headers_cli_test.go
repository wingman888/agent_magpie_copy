package main

import (
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestApplyPairsReadsHeaders(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{
		"url=https://relay.example.com/v1",
		"HEADER.X-ORG-id =  padded  ",
		"header.X-Org-Id=acme",
		"Header.Authorization=Bearer a=b",
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-ORG-id": "padded", "X-Org-Id": "acme", "Authorization": "Bearer a=b"}
	if !reflect.DeepEqual(p.Headers, want) {
		t.Errorf("headers = %v, want %v", p.Headers, want)
	}
	if p.Chat != "https://relay.example.com/v1" {
		t.Errorf("url not read alongside headers: %q", p.Chat)
	}
}

func TestApplyPairsRejectsBareHeaderPrefix(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{"header.=x"}); err == nil {
		t.Error("an empty header name should be an error, not a silent header")
	}
}

func TestShowProviderListsHeadersSorted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	showErr := showProvider(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.example.com/v1",
		Headers: map[string]string{"X-Org-Id": "acme", "X-ORG-id": "padded", "Authorization": "Bearer x"}})
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if showErr != nil {
		t.Fatal(showErr)
	}

	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "  header  ") {
			lines = append(lines, l)
		}
	}
	want := []string{
		"  header  Authorization: Bearer x",
		"  header  X-ORG-id: padded",
		"  header  X-Org-Id: acme",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("header lines = %q, want %q", lines, want)
	}
}
