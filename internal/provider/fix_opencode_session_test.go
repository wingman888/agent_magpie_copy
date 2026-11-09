package provider

import (
	"net/http"
	"strings"
	"testing"
)

// TestFixOpenCodeImageOnlySessions: conversations whose first user message has
// no text must still get distinct, stable session ids, for both Chat and
// Responses request shapes.
func TestFixOpenCodeImageOnlySessions(t *testing.T) {
	img := func(u string) string {
		return `{"type":"image_url","image_url":{"url":"` + u + `"}}`
	}
	chatA := `{"model":"m","messages":[{"role":"user","content":[` + img("data:a") + `]},{"role":"assistant","content":"hi"},{"role":"user","content":"more"}]}`
	chatA2 := `{"messages":[{"role":"user","content":[` + img("data:a") + `]}],"model":"m"}`
	chatB := `{"model":"m","messages":[{"role":"user","content":[` + img("data:b") + `]}]}`
	respA := `{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:a"}]},{"role":"assistant","content":"hi"},{"role":"user","content":"more"}]}`
	respB := `{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:b"}]}]}`
	respAString := `{"model":"m","input":"hello"}`
	respBString := `{"model":"m","input":"world"}`

	sid := func(body string) string { return OpenCodeSession(http.Header{}, []byte(body)) }

	if !strings.HasPrefix(sid(chatA), "magpie-") {
		t.Fatalf("missing magpie- prefix: %q", sid(chatA))
	}
	if sid(chatA) == sid(chatB) {
		t.Error("two image-only Chat conversations share a session id")
	}
	if sid(chatA) != sid(chatA2) {
		t.Error("same Chat conversation with different formatting got different session ids")
	}
	if sid(respA) == sid(respB) {
		t.Error("two image-only Responses conversations share a session id")
	}
	if sid(respAString) == sid(respBString) {
		t.Error("two Responses string conversations share a session id")
	}
	// the same conversation keeps its id as turns are added
	if sid(respA) != sid(strings.Replace(respA, `"more"`, `"even more"`, 1)) {
		t.Error("Responses id changed across turns of the same conversation")
	}
	// a body with no messages or input at all still yields a magpie- id
	if !strings.HasPrefix(sid(`{"model":"m"}`), "magpie-") {
		t.Error("body without messages did not yield a magpie- id")
	}
}
