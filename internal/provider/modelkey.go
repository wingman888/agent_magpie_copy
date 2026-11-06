package provider

import (
	"regexp"
	"strings"
)

// versionDot is a dot between two version numbers: 5.5 in claude-opus-5.5.
var versionDot = regexp.MustCompile(`(\d)\.(\d)`)

// dateSuffix is the 8-digit date some vendors put on the end: -20260801.
var dateSuffix = regexp.MustCompile(`-\d{8}$`)

// normalizeModelKey is the spelling a model is known by whichever vendor
// names it, so the same model from two providers can be matched: case is
// ignored, the vendor's prefix (everything up to the first /) is dropped,
// dots between version numbers read as hyphens, and a date on the end
// goes. A variant after a colon (:batch) makes a different model, so it is
// kept as it is, and so is a dot that isn't between numbers (v1.beta).
func normalizeModelKey(model string) string {
	m := strings.ToLower(model)
	if i := strings.Index(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	base, variant := m, ""
	if i := strings.Index(m, ":"); i >= 0 {
		base, variant = m[:i], m[i:]
	}
	// a replacement may use up the digit a dot after it needs (5.5.1), so
	// go over it again until none is left
	for {
		next := versionDot.ReplaceAllString(base, "$1-$2")
		if next == base {
			break
		}
		base = next
	}
	return dateSuffix.ReplaceAllString(base, "") + variant
}
