package provider

import (
	"net/http"
	"strings"
	"testing"
)

func TestIsOpenCodeHost(t *testing.T) {
	for host, want := range map[string]bool{
		"opencode.ai":          true,
		"api.opencode.ai":      true,
		"OPENCODE.AI":          true,
		"notopencode.ai":       false,
		"opencode.ai.evil.com": false,
		"example.com":          false,
		"":                     false,
	} {
		if got := IsOpenCodeHost(host); got != want {
			t.Errorf("IsOpenCodeHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestProviderIsOpenCode(t *testing.T) {
	if !(Provider{Chat: "https://opencode.ai/zen/go/v1"}).IsOpenCode() {
		t.Error("opencode.ai base should be OpenCode")
	}
	if !(Provider{Anthropic: "https://opencode.ai/zen"}).IsOpenCode() {
		t.Error("anthropic opencode.ai base should be OpenCode")
	}
	if (Provider{Chat: "https://notopencode.ai/v1"}).IsOpenCode() {
		t.Error("notopencode.ai must not be OpenCode")
	}
	if (Provider{Chat: "https://api.deepseek.com/v1"}).IsOpenCode() {
		t.Error("deepseek is not OpenCode")
	}
}

func TestOpenCodeSessionPriority(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	for i, h := range sessionHeaders {
		in := http.Header{}
		// the first header in the list wins over the ones after it
		for _, later := range sessionHeaders[i:] {
			in.Set(later, "v-"+later)
		}
		if got := OpenCodeSession(in, body); got != "v-"+h {
			t.Errorf("with %s and later headers: got %q, want %q", h, got, "v-"+h)
		}
	}
}

func TestOpenCodeSessionBlankHeaderIgnored(t *testing.T) {
	in := http.Header{}
	in.Set("x-opencode-session", "   ")
	in.Set("x-session-id", "abc")
	if got := OpenCodeSession(in, nil); got != "abc" {
		t.Errorf("blank header should be skipped, got %q", got)
	}
}

func TestOpenCodeSessionDerivedStable(t *testing.T) {
	chatFirst := []byte(`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"write a parser"},{"role":"assistant","content":"ok"},{"role":"user","content":"and tests"}]}`)
	chatLater := []byte(`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"write a parser"},{"role":"assistant","content":"ok"},{"role":"user","content":"more"},{"role":"assistant","content":"sure"},{"role":"user","content":"again"}]}`)
	other := []byte(`{"model":"m","messages":[{"role":"user","content":"a different conversation"}]}`)

	a := OpenCodeSession(nil, chatFirst)
	if !strings.HasPrefix(a, "magpie-") || len(a) <= len("magpie-") {
		t.Fatalf("derived session %q lacks magpie- prefix", a)
	}
	if b := OpenCodeSession(nil, chatLater); b != a {
		t.Errorf("later turn changed session: %q vs %q", b, a)
	}
	if c := OpenCodeSession(nil, other); c == a {
		t.Errorf("different conversation shares session %q", a)
	}
}

func TestOpenCodeSessionResponsesShapes(t *testing.T) {
	asString := []byte(`{"model":"m","input":"write a parser"}`)
	asArray := []byte(`{"model":"m","input":[{"role":"user","content":"write a parser"}]}`)
	asParts := []byte(`{"model":"m","input":[{"role":"system","content":"s"},{"role":"user","content":[{"type":"input_text","text":"write "},{"type":"input_text","text":"a parser"}]}]}`)
	later := []byte(`{"model":"m","input":[{"role":"user","content":"write a parser"},{"role":"assistant","content":"ok"},{"role":"user","content":"more"}]}`)

	want := OpenCodeSession(nil, asString)
	if !strings.HasPrefix(want, "magpie-") {
		t.Fatalf("got %q", want)
	}
	for name, body := range map[string][]byte{"array": asArray, "parts": asParts, "later": later} {
		if got := OpenCodeSession(nil, body); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}

func TestSetOpenCodeHeaders(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	in := http.Header{}
	in.Set("x-session-id", "from-agent")

	req, _ := http.NewRequest(http.MethodPost, "https://opencode.ai/zen/go/v1/chat/completions", nil)
	SetOpenCodeHeaders(req, in, body)
	if got := req.Header.Get("x-opencode-session"); got != "from-agent" {
		t.Errorf("opencode host: got %q", got)
	}

	req, _ = http.NewRequest(http.MethodPost, "https://api.deepseek.com/v1/chat/completions", nil)
	SetOpenCodeHeaders(req, in, body)
	if _, ok := req.Header["X-Opencode-Session"]; ok {
		t.Error("non-OpenCode host received x-opencode-session")
	}

	req, _ = http.NewRequest(http.MethodPost, "https://notopencode.ai/v1", nil)
	SetOpenCodeHeaders(req, in, body)
	if _, ok := req.Header["X-Opencode-Session"]; ok {
		t.Error("notopencode.ai received x-opencode-session")
	}
}
