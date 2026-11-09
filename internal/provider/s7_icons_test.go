package provider

import "testing"

// Only a document whose first element is <svg> is an SVG picture; text that
// merely contains an <svg> element is refused.
func TestS7SVGDetectionIsStrict(t *testing.T) {
	s4Home(t)
	for _, doc := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg"/>`,
		"\xEF\xBB\xBF  <?xml version=\"1.0\"?>\n<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>",
		`<!-- icon --><svg xmlns="http://www.w3.org/2000/svg"></svg>`,
		`<!DOCTYPE svg><svg xmlns="http://www.w3.org/2000/svg"></svg>`,
	} {
		if !rsiIsSVG([]byte(doc)) {
			t.Errorf("valid SVG refused: %q", doc)
		}
		if _, err := StoreIcon([]byte(doc)); err != nil {
			t.Errorf("valid SVG not stored: %q: %v", doc, err)
		}
	}
	for _, doc := range []string{
		"just words",
		"hello <svg></svg>",
		"hello <svg>",
		`<html><svg></svg></html>`,
	} {
		if rsiIsSVG([]byte(doc)) {
			t.Errorf("not an SVG accepted: %q", doc)
		}
		if _, err := StoreIcon([]byte(doc)); err == nil {
			t.Errorf("not an SVG stored: %q", doc)
		}
	}
}
