package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// magS2Env points config, cache and home at a fresh temp dir.
func magS2Env(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// magS2Stored returns the stored entry for id, raw from providers.json.
func magS2Stored(t *testing.T, id string) Provider {
	t.Helper()
	for _, p := range load().Providers {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no stored provider %q", id)
	return Provider{}
}

func magS2Keys(p Provider) []string {
	var out []string
	for _, k := range p.Keys {
		out = append(out, k.Key)
	}
	return out
}

func TestS2KeyRefIsFirstFiveBytesOfSHA256(t *testing.T) {
	sum := sha256.Sum256([]byte("sk-s2-example"))
	want := hex.EncodeToString(sum[:5])
	if got := KeyRef("sk-s2-example"); got != want || len(got) != 10 {
		t.Fatalf("KeyRef = %q, want %q", got, want)
	}
}

func TestS2FirstKeyBecomesCurrent(t *testing.T) {
	magS2Env(t)
	// a local base needs no key, so the provider can exist without one
	if err := Save(Provider{ID: "s2first", Name: "S2 First", Chat: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("s2first", "work", "sk-s2-one", ""); err != nil {
		t.Fatal(err)
	}
	p := magS2Stored(t, "s2first")
	if p.Key != "sk-s2-one" || p.KeyName != "work" || len(p.Keys) != 0 {
		t.Fatalf("first key not current: %+v", p)
	}
}

func TestS2AddKeyRejectsDuplicateAndUnknownProtocol(t *testing.T) {
	magS2Env(t)
	if err := Save(Provider{ID: "s2dup", Name: "S2 Dup", Chat: "https://relay.example/v1", Key: "sk-s2-a"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("s2dup", "", "sk-s2-a", ""); err == nil {
		t.Fatal("duplicate of the current key was accepted")
	}
	if err := AddKey("s2dup", "", "sk-s2-b", ""); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("s2dup", "", "sk-s2-b", Anthropic); err == nil {
		t.Fatal("duplicate of an extra key was accepted")
	}
	if err := AddKey("s2dup", "", "sk-s2-c", Protocol("gemini")); err == nil {
		t.Fatal("unknown protocol was accepted")
	}
	if err := Save(Provider{ID: "s2dup", Name: "S2 Dup", Chat: "https://relay.example/v1", Key: "sk-s2-a", KeyProtocol: "bogus"}); err == nil {
		t.Fatal("Save accepted an unknown key protocol")
	}
}

func TestS2UseKeyKeepsOtherKeysAndLimits(t *testing.T) {
	magS2Env(t)
	if err := Save(Provider{ID: "s2use", Name: "S2 Use", Chat: "https://relay.example/v1", Key: "sk-s2-cur", KeyName: "cur"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("s2use", "team", "sk-s2-team", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("s2use", "spare", "sk-s2-spare", ""); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("s2use", KeyRef("sk-s2-team")); err != nil {
		t.Fatal(err)
	}
	p := magS2Stored(t, "s2use")
	if p.Key != "sk-s2-team" || p.KeyName != "team" || p.KeyProtocol != Anthropic {
		t.Fatalf("current key wrong: %+v", p)
	}
	want := []string{"sk-s2-cur", "sk-s2-spare"}
	if got := magS2Keys(p); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("extra keys = %v, want %v", got, want)
	}
	if p.Keys[0].Name != "cur" || p.Keys[0].Off {
		t.Fatalf("old current key lost its name or was switched off: %+v", p.Keys[0])
	}
}

func TestS2SetKeyOnHandsOverAndLastEnabledStays(t *testing.T) {
	magS2Env(t)
	if err := Save(Provider{ID: "s2on", Name: "S2 On", Chat: "https://relay.example/v1", Key: "sk-s2-first"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("s2on", "second", "sk-s2-second", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("s2on", KeyRef("sk-s2-first"), false); err != nil {
		t.Fatal(err)
	}
	p := magS2Stored(t, "s2on")
	if p.Key != "sk-s2-second" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-s2-first" || !p.Keys[0].Off {
		t.Fatalf("hand-over wrong: %+v", p)
	}
	if err := SetKeyOn("s2on", KeyRef("sk-s2-second"), false); err == nil {
		t.Fatal("the last enabled key was switched off")
	}
	if err := RemoveKey("s2on", KeyRef("sk-s2-second")); err == nil {
		t.Fatal("the last enabled key was removed")
	}
	if err := SetKeyOn("s2on", KeyRef("sk-s2-first"), true); err != nil {
		t.Fatal(err)
	}
	if p := magS2Stored(t, "s2on"); p.Keys[0].Off {
		t.Fatal("switching a key back on did not take")
	}
}

func TestS2RemoveCurrentHandsOverAndRenameWorks(t *testing.T) {
	magS2Env(t)
	if err := Save(Provider{ID: "s2rm", Name: "S2 Rm", Chat: "https://relay.example/v1", Key: "sk-s2-a", KeyName: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("s2rm", "b", "sk-s2-b", ""); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("s2rm", KeyRef("sk-s2-b"), "bee"); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("s2rm", KeyRef("sk-s2-a"), "ay"); err != nil {
		t.Fatal(err)
	}
	if p := magS2Stored(t, "s2rm"); p.KeyName != "ay" || p.Keys[0].Name != "bee" {
		t.Fatalf("rename wrong: %+v", p)
	}
	if err := RemoveKey("s2rm", KeyRef("sk-s2-a")); err != nil {
		t.Fatal(err)
	}
	p := magS2Stored(t, "s2rm")
	if p.Key != "sk-s2-b" || p.KeyName != "bee" || len(p.Keys) != 0 {
		t.Fatalf("removing the current key did not hand over: %+v", p)
	}
	if err := RemoveKey("s2rm", KeyRef("sk-s2-b")); err == nil {
		t.Fatal("the last key was removed")
	}
}

func TestS2SetKeyProtocolSetsAndClears(t *testing.T) {
	magS2Env(t)
	if err := Save(Provider{ID: "s2pr", Name: "S2 Pr", Chat: "https://relay.example/v1", Key: "sk-s2-a"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("s2pr", "", "sk-s2-b", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("s2pr", KeyRef("sk-s2-b"), Chat); err != nil {
		t.Fatal(err)
	}
	if got := magS2Stored(t, "s2pr").Keys[0].Protocol; got != Chat {
		t.Fatalf("protocol = %q, want chat", got)
	}
	if err := SetKeyProtocol("s2pr", KeyRef("sk-s2-b"), ""); err != nil {
		t.Fatal(err)
	}
	if got := magS2Stored(t, "s2pr").Keys[0].Protocol; got != "" {
		t.Fatalf("protocol not cleared: %q", got)
	}
	if err := SetKeyProtocol("s2pr", KeyRef("sk-s2-a"), Protocol("gemini")); err == nil {
		t.Fatal("gemini accepted as a key protocol")
	}
	if err := SetKeyProtocol("s2pr", "0000000000", Anthropic); err == nil {
		t.Fatal("unknown key reference accepted")
	}
}

func TestS2SaveKeepsKeysItDoesNotKnow(t *testing.T) {
	magS2Env(t)
	if err := Save(Provider{ID: "s2keep", Name: "S2 Keep", Chat: "https://relay.example/v1", Key: "sk-s2-a", KeyName: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("s2keep", "b", "sk-s2-b", Anthropic); err != nil {
		t.Fatal(err)
	}
	// a fresh struct, as the GUI or an import would save
	if err := Save(Provider{ID: "s2keep", Name: "S2 Keep", Chat: "https://relay.example/v1", Key: "sk-s2-a"}); err != nil {
		t.Fatal(err)
	}
	p := magS2Stored(t, "s2keep")
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-s2-b" || p.Keys[0].Protocol != Anthropic || p.KeyName != "a" {
		t.Fatalf("Save dropped keys: %+v", p)
	}
}

func TestS2KeyPlansOrderByModelAndProtocol(t *testing.T) {
	magS2Env(t)
	p := Provider{ID: "s2plan", Name: "S2 Plan", Chat: "https://relay.example/v1",
		Anthropic: "https://relay.example", Key: "sk-s2-any",
		Keys: []ExtraKey{
			{Key: "sk-s2-ant", Protocol: Anthropic},
			{Key: "sk-s2-chat", Protocol: Chat},
			{Key: "sk-s2-off", Protocol: Chat, Off: true},
		}}
	order := func(from Protocol, model string) []string {
		var out []string
		for _, pl := range p.KeyPlans(from, model) {
			out = append(out, pl.Provider.Key)
		}
		return out
	}
	if got := strings.Join(order(Anthropic, "claude-s2"), ","); got != "sk-s2-ant,sk-s2-any,sk-s2-chat" {
		t.Fatalf("claude order = %s", got)
	}
	if got := strings.Join(order(Chat, "gpt-s2"), ","); got != "sk-s2-chat,sk-s2-any,sk-s2-ant" {
		t.Fatalf("gpt order = %s", got)
	}
	if got := strings.Join(order(Chat, "deepseek-s2"), ","); got != "sk-s2-any,sk-s2-chat,sk-s2-ant" {
		t.Fatalf("other-model order = %s", got)
	}
	for _, pl := range p.KeyPlans(Chat, "deepseek-s2") {
		if pl.Provider.Key == "sk-s2-off" {
			t.Fatal("an off key was planned")
		}
		if pl.Protocol == Anthropic && (len(pl.Upstreams) != 1 || pl.Upstreams[0] != Anthropic) {
			t.Fatalf("anthropic key may reach %v", pl.Upstreams)
		}
	}
}

func TestS2KeyLimitedToMissingEndpointIsLeftOut(t *testing.T) {
	magS2Env(t)
	p := Provider{ID: "s2none", Name: "S2 None", Chat: "https://relay.example/v1", Key: "sk-s2-x", KeyProtocol: Anthropic}
	if plans := p.KeyPlans(Chat, "gpt-s2"); len(plans) != 0 {
		t.Fatalf("an anthropic-only key was planned on an OpenAI-only vendor: %d plans", len(plans))
	}
}
