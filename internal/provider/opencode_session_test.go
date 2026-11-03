package provider

import (
	"strings"
	"testing"
)

// An empty first message must not make every such conversation share one
// session: the message's own raw JSON stands in for its text.
func TestOpenCodeSessionEmptyFirstMessage(t *testing.T) {
	emptyA := []byte(`{"model":"m","messages":[{"role":"user","content":""}]}`)
	emptyB := []byte(`{"model":"m","messages":[{"role":"user","content":"   "}]}`)
	imageOnly := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:a"}}]}]}`)
	imageOther := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:b"}}]}]}`)

	a := OpenCodeSession(nil, emptyA)
	if !hasMagpiePrefix(a) {
		t.Fatalf("empty message: %q", a)
	}
	if again := OpenCodeSession(nil, emptyA); again != a {
		t.Errorf("same empty conversation changed: %q vs %q", a, again)
	}
	if b := OpenCodeSession(nil, emptyB); b == a {
		t.Errorf("blank and empty first messages share session %q", a)
	}
	x := OpenCodeSession(nil, imageOnly)
	if !hasMagpiePrefix(x) {
		t.Fatalf("image-only message: %q", x)
	}
	if y := OpenCodeSession(nil, imageOther); y == x {
		t.Errorf("different image-only conversations share session %q", x)
	}
	if x == a {
		t.Errorf("image-only and empty conversations share session %q", x)
	}
}

func TestOpenCodeSessionEmptyResponsesInput(t *testing.T) {
	emptyString := []byte(`{"model":"m","input":""}`)
	blankString := []byte(`{"model":"m","input":"  "}`)
	emptyItem := []byte(`{"model":"m","input":[{"role":"user","content":""}]}`)
	imageItem := []byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:a"}]}]}`)

	a := OpenCodeSession(nil, emptyString)
	if !hasMagpiePrefix(a) {
		t.Fatalf("empty input: %q", a)
	}
	if OpenCodeSession(nil, blankString) == a {
		t.Errorf("blank and empty input share session %q", a)
	}
	b := OpenCodeSession(nil, emptyItem)
	c := OpenCodeSession(nil, imageItem)
	if !hasMagpiePrefix(b) || !hasMagpiePrefix(c) || b == c || b == a || c == a {
		t.Errorf("empty item %q, image item %q, empty input %q should all differ", b, c, a)
	}
}

func hasMagpiePrefix(s string) bool {
	return strings.HasPrefix(s, "magpie-") && len(s) > len("magpie-")
}
