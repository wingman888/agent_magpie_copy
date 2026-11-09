package provider

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// ktIsolate points magpie's config and home at temp dirs.
func ktIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// ktSave stores a plain provider.
func ktSave(t *testing.T, p Provider) {
	t.Helper()
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
}

// ktKeys returns the stored keys of a provider, current first, as "key:off".
func ktKeys(t *testing.T, id string) []string {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range p.AllKeys() {
		s := k.Ref
		if k.Off {
			s += ":off"
		}
		out = append(out, s)
	}
	return out
}

// ktRef is the fingerprint of a key, to keep assertions short.
func ktRef(key string) string { return KeyRef(key) }

func ktFind(t *testing.T, id string) *Provider {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var ktHex10 = regexp.MustCompile(`^[0-9a-f]{10}$`)

func TestKtKeyRefFingerprint(t *testing.T) {
	a := KeyRef("sk-one")
	if !ktHex10.MatchString(a) {
		t.Fatalf("KeyRef = %q, want 10 lower-case hex characters", a)
	}
	if a != KeyRef("sk-one") {
		t.Fatal("KeyRef is not stable")
	}
	if a == KeyRef("sk-two") {
		t.Fatal("different keys share a fingerprint")
	}
}

func TestKtAddKeyOrderAndRejects(t *testing.T) {
	ktIsolate(t)
	ktSave(t, Provider{ID: "kt-add", Name: "kt-add", Chat: "https://a.example/v1", Key: "kt-k1"})

	if err := AddKey("kt-add", "team", "kt-k2", ""); err != nil {
		t.Fatal(err)
	}
	if got := ktKeys(t, "kt-add"); len(got) != 2 || got[0] != ktRef("kt-k1") || got[1] != ktRef("kt-k2") {
		t.Fatalf("keys = %v, want current then the new one", got)
	}
	if err := AddKey("kt-add", "", "kt-k2", ""); err == nil {
		t.Fatal("duplicate key accepted")
	}
	if err := AddKey("kt-add", "", "kt-k1", ""); err == nil {
		t.Fatal("duplicate of the current key accepted")
	}
	if err := AddKey("kt-add", "", "kt-k3", Protocol("gemini")); err == nil {
		t.Fatal("gemini accepted as a key protocol")
	}
	if err := AddKey("kt-add", "", "kt-k3", Protocol("bogus")); err == nil {
		t.Fatal("unknown key protocol accepted")
	}
	if err := AddKey("kt-add", "", "  ", ""); err == nil {
		t.Fatal("empty key accepted")
	}
	if err := AddKey("no-such-provider", "", "kt-k9", ""); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestKtAddKeyBecomesFirstWhenNone(t *testing.T) {
	ktIsolate(t)
	ktSave(t, Provider{ID: "kt-local", Name: "kt-local", Chat: "http://localhost:9/v1"})
	if err := AddKey("kt-local", "", "kt-first", ""); err != nil {
		t.Fatal(err)
	}
	if p := ktFind(t, "kt-local"); p.Key != "kt-first" {
		t.Fatalf("current key = %q, want the first one added", p.Key)
	}
}

func TestKtSetKeyProtocol(t *testing.T) {
	ktIsolate(t)
	ktSave(t, Provider{ID: "kt-proto", Name: "kt-proto", Chat: "https://a.example/v1", Anthropic: "https://a.example", Key: "kt-p1"})
	if err := AddKey("kt-proto", "", "kt-p2", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("kt-proto", ktRef("kt-p2"), Anthropic); err != nil {
		t.Fatal(err)
	}
	p := ktFind(t, "kt-proto")
	if p.Keys[0].Protocol != Anthropic {
		t.Fatalf("protocol = %q, want anthropic", p.Keys[0].Protocol)
	}
	if err := SetKeyProtocol("kt-proto", ktRef("kt-p2"), Protocol("nope")); err == nil {
		t.Fatal("bogus protocol accepted")
	}
	if err := SetKeyProtocol("kt-proto", "ffffffffff", Chat); err == nil {
		t.Fatal("unknown key ref accepted")
	}
	if err := SetKeyProtocol("kt-proto", ktRef("kt-p2"), ""); err != nil {
		t.Fatal(err)
	}
	if p := ktFind(t, "kt-proto"); p.Keys[0].Protocol != "" {
		t.Fatalf("protocol not cleared: %q", p.Keys[0].Protocol)
	}
}

func TestKtUseKeyKeepsOthers(t *testing.T) {
	ktIsolate(t)
	ktSave(t, Provider{ID: "kt-use", Name: "kt-use", Chat: "https://a.example/v1", Key: "kt-u1", KeyName: "first", KeyProtocol: Chat})
	if err := AddKey("kt-use", "second", "kt-u2", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("kt-use", "third", "kt-u3", ""); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("kt-use", ktRef("kt-u3")); err != nil {
		t.Fatal(err)
	}
	p := ktFind(t, "kt-use")
	if p.Key != "kt-u3" || p.KeyName != "third" || p.KeyProtocol != "" {
		t.Fatalf("current = %q %q %q, want the third key", p.Key, p.KeyName, p.KeyProtocol)
	}
	if len(p.Keys) != 2 {
		t.Fatalf("keys = %+v, want the two others", p.Keys)
	}
	if p.Keys[0].Key != "kt-u1" || p.Keys[0].Name != "first" || p.Keys[0].Protocol != Chat {
		t.Fatalf("previous current key lost its name or limit: %+v", p.Keys[0])
	}
	if p.Keys[1].Key != "kt-u2" || p.Keys[1].Name != "second" || p.Keys[1].Protocol != Anthropic {
		t.Fatalf("other key lost its name or limit: %+v", p.Keys[1])
	}
	// using the current key again changes nothing
	if err := UseKey("kt-use", ktRef("kt-u3")); err != nil {
		t.Fatal(err)
	}
	if q := ktFind(t, "kt-use"); q.Key != "kt-u3" || len(q.Keys) != 2 {
		t.Fatalf("using the current key again changed things: %+v", q)
	}
}

func TestKtSetKeyOnLastEnabledAndCurrent(t *testing.T) {
	ktIsolate(t)
	ktSave(t, Provider{ID: "kt-on", Name: "kt-on", Chat: "https://a.example/v1", Key: "kt-o1"})
	if err := AddKey("kt-on", "", "kt-o2", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("kt-on", ktRef("kt-o2"), false); err != nil {
		t.Fatal(err)
	}
	// kt-o1 is now the last enabled key: refuse, and change nothing
	before := ktKeys(t, "kt-on")
	if err := SetKeyOn("kt-on", ktRef("kt-o1"), false); err == nil {
		t.Fatal("disabled the last enabled key")
	}
	if got := ktKeys(t, "kt-on"); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Fatalf("refused change still changed keys: %v -> %v", before, got)
	}
	if err := SetKeyOn("kt-on", ktRef("kt-o2"), true); err != nil {
		t.Fatal(err)
	}
	// disabling the current key hands the role to the next enabled one
	if err := SetKeyOn("kt-on", ktRef("kt-o1"), false); err != nil {
		t.Fatal(err)
	}
	p := ktFind(t, "kt-on")
	if p.Key != "kt-o2" {
		t.Fatalf("current = %q, want kt-o2 to take over", p.Key)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "kt-o1" || !p.Keys[0].Off {
		t.Fatalf("old current key not kept as off: %+v", p.Keys)
	}
	// and it can come back
	if err := SetKeyOn("kt-on", ktRef("kt-o1"), true); err != nil {
		t.Fatal(err)
	}
	if q := ktFind(t, "kt-on"); q.Keys[0].Off {
		t.Fatal("re-enabled key still off")
	}
}

func TestKtRemoveKeyRules(t *testing.T) {
	ktIsolate(t)
	ktSave(t, Provider{ID: "kt-rm", Name: "kt-rm", Chat: "https://a.example/v1", Key: "kt-r1"})
	if err := AddKey("kt-rm", "", "kt-r2", ""); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("kt-rm", "", "kt-r3", ""); err != nil {
		t.Fatal(err)
	}
	// removing the current key hands the role to the next enabled one
	if err := RemoveKey("kt-rm", ktRef("kt-r1")); err != nil {
		t.Fatal(err)
	}
	if p := ktFind(t, "kt-rm"); p.Key != "kt-r2" || len(p.Keys) != 1 {
		t.Fatalf("after removing current: key %q, keys %+v", p.Key, p.Keys)
	}
	// disable the other one; now kt-r2 is the last enabled key
	if err := SetKeyOn("kt-rm", ktRef("kt-r3"), false); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("kt-rm", ktRef("kt-r2")); err == nil {
		t.Fatal("removed the last enabled key")
	}
	if p := ktFind(t, "kt-rm"); p.Key != "kt-r2" {
		t.Fatal("refused removal still removed the key")
	}
	// a disabled key can go
	if err := RemoveKey("kt-rm", ktRef("kt-r3")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("kt-rm", ktRef("kt-r9")); err == nil {
		t.Fatal("removed an unknown key")
	}
}

func TestKtRenameKey(t *testing.T) {
	ktIsolate(t)
	ktSave(t, Provider{ID: "kt-name", Name: "kt-name", Chat: "https://a.example/v1", Key: "kt-n1"})
	if err := AddKey("kt-name", "old", "kt-n2", ""); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("kt-name", ktRef("kt-n2"), "personal"); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("kt-name", ktRef("kt-n1"), "team"); err != nil {
		t.Fatal(err)
	}
	p := ktFind(t, "kt-name")
	if p.KeyName != "team" || p.Keys[0].Name != "personal" {
		t.Fatalf("names = %q / %q, want team / personal", p.KeyName, p.Keys[0].Name)
	}
	if err := RenameKey("kt-name", ktRef("kt-n2"), "   "); err == nil {
		t.Fatal("empty name accepted")
	}
}

func TestKtCandidatesOrderAndLimits(t *testing.T) {
	ktIsolate(t)
	// current key: OpenAI-only; then an Anthropic-only key; then an unlimited one
	ktSave(t, Provider{ID: "kt-cand", Name: "kt-cand", Chat: "https://o.example/v1", Anthropic: "https://a.example",
		Key: "kt-oa", KeyProtocol: Chat,
		Keys: []SubKey{{Key: "kt-an", Protocol: Anthropic}, {Key: "kt-any"}}})
	p := ktFind(t, "kt-cand")

	refs := func(cs []Candidate) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Ref)
		}
		return out
	}
	cases := []struct {
		name  string
		from  Protocol
		model string
		want  []string
	}{
		{"claude from anthropic prefers the anthropic key", Anthropic, "claude-sonnet-5-5", []string{ktRef("kt-an"), ktRef("kt-any"), ktRef("kt-oa")}},
		{"claude from chat prefers the anthropic key", Chat, "claude-sonnet-5-5", []string{ktRef("kt-an"), ktRef("kt-any"), ktRef("kt-oa")}},
		{"gpt prefers the openai key", Anthropic, "gpt-5.5", []string{ktRef("kt-oa"), ktRef("kt-any"), ktRef("kt-an")}},
		{"gpt through a provider prefix", Chat, "deepseek/gpt-4o", []string{ktRef("kt-oa"), ktRef("kt-any"), ktRef("kt-an")}},
		{"o-series prefers the openai key", Chat, "o3-mini", []string{ktRef("kt-oa"), ktRef("kt-any"), ktRef("kt-an")}},
		{"other models prefer the key that speaks the client's protocol", Anthropic, "deepseek-chat", []string{ktRef("kt-an"), ktRef("kt-any"), ktRef("kt-oa")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := refs(p.Candidates(c.from, c.model))
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("order = %v, want %v", got, c.want)
			}
		})
	}

	// the client's protocol decides where an unlimited key goes
	for _, c := range p.Candidates(Anthropic, "claude-sonnet-5-5") {
		if c.Ref == ktRef("kt-any") && c.To != Anthropic {
			t.Fatalf("unlimited key routed to %s, want the client's protocol", c.To)
		}
		if c.Ref == ktRef("kt-oa") && c.To != Chat {
			t.Fatalf("openai key routed to %s, want chat", c.To)
		}
	}
}

func TestKtCandidatesSkipDisabledAndMissingEndpoints(t *testing.T) {
	ktIsolate(t)
	ktSave(t, Provider{ID: "kt-skip", Name: "kt-skip", Anthropic: "https://a.example",
		Key: "kt-sa", KeyProtocol: Chat, // no chat endpoint: must not be used
		Keys: []SubKey{{Key: "kt-sb", Protocol: Anthropic}, {Key: "kt-sc", Off: true}}})
	p := ktFind(t, "kt-skip")
	cs := p.Candidates(Anthropic, "claude-sonnet-5-5")
	if len(cs) != 1 || cs[0].Ref != ktRef("kt-sb") {
		t.Fatalf("candidates = %+v, want only the anthropic key", cs)
	}
	for _, c := range cs {
		if c.Key == "kt-sc" {
			t.Fatal("disabled key was offered")
		}
	}
}

func TestKtCandidatesNoKey(t *testing.T) {
	ktIsolate(t)
	ktSave(t, Provider{ID: "kt-nokey", Name: "kt-nokey", Chat: "http://localhost:9/v1"})
	cs := ktFind(t, "kt-nokey").Candidates(Chat, "m")
	if len(cs) != 1 || cs[0].Key != "" || cs[0].To != Chat {
		t.Fatalf("no-key provider candidates = %+v", cs)
	}
}

func TestKtFamily(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5-5":        "claude",
		"anthropic/claude-3":     "claude",
		"gpt-5.5":                "openai",
		"openai/gpt-4o":          "openai",
		"o3-mini":                "openai",
		"o4":                     "openai",
		"gpt-5-codex":            "openai",
		"deepseek/deepseek-chat": "other",
		"gemini-2.5-pro":         "other",
	}
	for model, want := range cases {
		if got := family(model); got != want {
			t.Errorf("family(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestKtSaveKeepsKeysAndValidates(t *testing.T) {
	ktIsolate(t)
	ktSave(t, Provider{ID: "kt-save", Name: "kt-save", Chat: "https://a.example/v1", Key: "kt-s1"})
	if err := AddKey("kt-save", "b", "kt-s2", Responses); err != nil {
		t.Fatal(err)
	}
	// saving the same current key again keeps the others
	if err := Save(Provider{ID: "kt-save", Name: "kt-save", Chat: "https://a.example/v1", Key: "kt-s1"}); err != nil {
		t.Fatal(err)
	}
	if p := ktFind(t, "kt-save"); len(p.Keys) != 1 || p.Keys[0].Key != "kt-s2" || p.Keys[0].Protocol != Responses {
		t.Fatalf("re-save lost the other keys: %+v", p.Keys)
	}
	// saving a different current key replaces it, not the others
	if err := Save(Provider{ID: "kt-save", Name: "kt-save", Chat: "https://a.example/v1", Key: "kt-s3"}); err != nil {
		t.Fatal(err)
	}
	if p := ktFind(t, "kt-save"); p.Key != "kt-s3" || len(p.Keys) != 1 || p.Keys[0].Key != "kt-s2" {
		t.Fatalf("after replacing the current key: %q %+v", p.Key, p.Keys)
	}
	if err := Save(Provider{ID: "kt-bad", Name: "kt-bad", Chat: "https://a.example/v1", Key: "kt-b1", KeyProtocol: Protocol("gemini")}); err == nil {
		t.Fatal("gemini accepted as the current key's protocol")
	}
	bad := Provider{ID: "kt-bad", Name: "kt-bad", Chat: "https://a.example/v1", Key: "kt-b1",
		Keys: []SubKey{{Key: "kt-b2", Protocol: Protocol("bogus")}}}
	if err := Save(bad); err == nil {
		t.Fatal("unknown protocol on an extra key accepted")
	}
	dup := Provider{ID: "kt-bad", Name: "kt-bad", Chat: "https://a.example/v1", Key: "kt-b1",
		Keys: []SubKey{{Key: "kt-b1"}}}
	if err := Save(dup); err == nil {
		t.Fatal("the same key listed twice was accepted")
	}
}

func TestKtStoredFieldNames(t *testing.T) {
	ktIsolate(t)
	ktSave(t, Provider{ID: "kt-json", Name: "kt-json", Chat: "https://a.example/v1", Key: "kt-j1", KeyName: "me", KeyProtocol: Chat})
	if err := AddKey("kt-json", "team", "kt-j2", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("kt-json", "", "kt-j3", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("kt-json", ktRef("kt-j2"), false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	for _, want := range []string{`"keyName": "me"`, `"keyProtocol": "chat"`, `"keys": [`, `"name": "team"`, `"protocol": "anthropic"`, `"off": true`} {
		if !strings.Contains(raw, want) {
			t.Errorf("providers.json lacks %s:\n%s", want, raw)
		}
	}
}
