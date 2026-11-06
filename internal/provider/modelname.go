package provider

import (
	"regexp"
	"strings"
)

// NormalizeModelName is the form one model is known by across vendors, so
// that the same model named differently by several of them is one model:
// case is ignored, the vendor prefix ("anthropic/") goes, the dot between
// version numbers counts as a dash ("5.5" is "5-5"), and so does an
// 8-digit date at the end ("-20260801"). A colon variant (":batch") is part
// of the name and kept; so is a dot that isn't between two numbers ("v1.beta").
func NormalizeModelName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	base, variant := s, ""
	if i := strings.Index(s, ":"); i >= 0 {
		base, variant = s[:i], s[i:]
	}
	base = versionDot.ReplaceAllString(base, "$1-$2")
	base = dateSuffix.ReplaceAllString(base, "")
	return base + variant
}

var (
	versionDot = regexp.MustCompile(`(\d)\.(\d)`)
	dateSuffix = regexp.MustCompile(`-\d{8}$`)
)
