package provider

// CanonicalModel is the one name a model goes by across vendors: the same
// model under another spelling, a vendor's prefix, a dotted version or a
// date suffix, or in other case, comes out the same. A colon variant such as
// ":batch" is a model of its own, and dots that aren't between version
// numbers (v1.beta) are left as they are.

import (
	"regexp"
	"strings"
)

var (
	// canonDate is an eight-digit date at the end of a model's name.
	canonDate = regexp.MustCompile(`-\d{8}$`)
	// canonVersionDot is a dot between two version numbers: 5.5, 4.1.
	canonVersionDot = regexp.MustCompile(`(\d)\.(\d)`)
)

// CanonicalModel is the canonical form of a model name, for telling that two
// vendors' names are the same model.
func CanonicalModel(name string) string {
	m := strings.ToLower(strings.TrimSpace(name))
	// the vendor's prefix: everything before the first slash
	if i := strings.Index(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	// a colon variant is kept as it is, and makes the model another one
	variant := ""
	if i := strings.Index(m, ":"); i >= 0 {
		m, variant = m[:i], m[i:]
	}
	// each pass takes every other dot of a run like 1.2.3, so repeat
	for {
		next := canonVersionDot.ReplaceAllString(m, "$1-$2")
		if next == m {
			break
		}
		m = next
	}
	m = canonDate.ReplaceAllString(m, "")
	return m + variant
}
