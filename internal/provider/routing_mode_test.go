package provider

import "testing"

// Routing values are normalized: the three names, "usage" as the older
// least-used, and anything else (including "ordered") smart.
func TestNormalizeRouting(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"order":       "order",
		"rotate":      "rotate",
		"least-used":  "least-used",
		"usage":       "least-used",
		" Rotate ":    "rotate",
		"USAGE":       "least-used",
		"ordered":     "",
		"round-robin": "",
	}
	for in, want := range cases {
		if got := NormalizeRouting(in); got != want {
			t.Errorf("NormalizeRouting(%q) = %q, want %q", in, got, want)
		}
	}
	if LeastUsed != "least-used" || Ordered != "order" || Rotate != "rotate" {
		t.Fatalf("constants: %q %q %q", Ordered, Rotate, LeastUsed)
	}
}
