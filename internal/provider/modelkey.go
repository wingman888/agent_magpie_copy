package provider

import (
	"regexp"
	"strings"
)

// NormalizeModelKey is the form a model's name takes when it is the same
// model in another vendor's name: case doesn't count, a vendor prefix
// (anthropic/…) doesn't, a version's dot is a hyphen (5.5 is 5-5), and an
// 8-digit date at the end doesn't. A colon variant (:batch) is another
// model, kept as it is; so is a dot that isn't a version's (v1.beta).
func NormalizeModelKey(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if i := strings.Index(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	base, variant, hasVariant := strings.Cut(name, ":")
	for {
		next := versionDot.ReplaceAllString(base, "$1-$2")
		if next == base {
			break
		}
		base = next
	}
	base = dateSuffix.ReplaceAllString(base, "")
	if hasVariant {
		return base + ":" + variant
	}
	return base
}

var (
	// versionDot is a dot between two digits: a version's, in 5.5 or 4.1.
	versionDot = regexp.MustCompile(`(\d)\.(\d)`)
	// dateSuffix is a release date at the end: -20260801.
	dateSuffix = regexp.MustCompile(`-\d{8}$`)
)
