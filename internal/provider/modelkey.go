package provider

import (
	"regexp"
	"strings"
)

// NormalizeModelKey is the form two vendors' names for one model share, so
// that they count as the same model: case is ignored, the vendor prefix
// ("anthropic/…", everything up to the last slash) is dropped, a dot between two numbers is a hyphen
// ("5.5" is "5-5"), and a trailing 8-digit date ("-20260801") is dropped.
// A colon variant (":batch") is a different model and stays as it is, as
// does a dot that isn't between two numbers ("v1.beta").
func NormalizeModelKey(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	for digitDot.MatchString(s) {
		s = digitDot.ReplaceAllString(s, "$1-$2")
	}
	return dateSuffix.ReplaceAllString(s, "")
}

var (
	digitDot   = regexp.MustCompile(`(\d)\.(\d)`)
	dateSuffix = regexp.MustCompile(`-\d{8}$`)
)
