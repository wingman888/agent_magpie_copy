package main

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestKeyProtocolArg(t *testing.T) {
	for in, want := range map[string]provider.Protocol{"any": "", "Anthropic": provider.Anthropic, " chat ": provider.Chat, "responses": provider.Responses} {
		got, err := keyProtocolArg(in)
		if err != nil || got != want {
			t.Errorf("keyProtocolArg(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := keyProtocolArg("gemini"); err == nil {
		t.Error("gemini accepted as a key protocol")
	}
}

func TestKeyCmdArgumentErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"use"}, {"rm", "relay"}, {"relay", "a", "b"}} {
		err := keyCmd(args)
		if err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("keyCmd(%q) = %v; want a usage error", args, err)
		}
	}
}
