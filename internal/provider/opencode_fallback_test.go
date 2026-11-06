package provider

import (
	"strings"
	"testing"
)

// A first user message with no text (an image alone) must still tell
// conversations apart, and stay the same across the turns of one conversation.
func TestOpenCodeSessionImageOnlyFirstMessage(t *testing.T) {
	imgA := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`)
	imgB := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,BBBB"}}]}]}`)
	imgAFollow := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]},{"role":"assistant","content":"ok"},{"role":"user","content":"what is it"}]}`)

	a := OpenCodeSession(nil, imgA)
	if !strings.HasPrefix(a, "magpie-") || len(a) <= len("magpie-") {
		t.Fatalf("fallback id %q", a)
	}
	if b := OpenCodeSession(nil, imgB); b == a {
		t.Errorf("different image-only conversations share id %q", a)
	}
	if c := OpenCodeSession(nil, imgAFollow); c != a {
		t.Errorf("later turn of the same image conversation changed id: %q vs %q", c, a)
	}
	// the same holds for Responses input arrays
	respA := []byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`)
	respB := []byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,BBBB"}]}]}`)
	if OpenCodeSession(nil, respA) == OpenCodeSession(nil, respB) {
		t.Errorf("Responses image-only conversations share an id")
	}
	// text-bearing messages keep the text-based id
	txt := []byte(`{"model":"m","messages":[{"role":"user","content":"fix the bug"}]}`)
	if got := firstUserText(txt); got != "fix the bug" {
		t.Errorf("firstUserText text case = %q", got)
	}
}
