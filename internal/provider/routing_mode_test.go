package provider

import "testing"

// Routing values are normalized: the three names, and anything else
// (including "ordered" and the older "usage") smart.
func TestRmodeNormalizeRouting(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"order":       "order",
		"rotate":      "rotate",
		"least-used":  "least-used",
		" Rotate ":    "rotate",
		"usage":       "",
		"USAGE":       "",
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

// A value saved by an older magpie as "usage" is least-used now; migrateRouting
// does that rename where configuration is loaded and saved.
func TestRmodeMigrateRouting(t *testing.T) {
	cases := map[string]string{
		"usage":      "least-used",
		" USAGE ":    "least-used",
		"least-used": "least-used",
		"rotate":     "rotate",
		"":           "",
		"ordered":    "ordered", // not a name magpie ever wrote; NormalizeRouting drops it
	}
	for in, want := range cases {
		if got := migrateRouting(in); got != want {
			t.Errorf("migrateRouting(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NormalizeRouting(migrateRouting("usage")); got != "least-used" {
		t.Errorf("usage saved: %q, want least-used", got)
	}
}
