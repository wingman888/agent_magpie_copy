package provider

// Vendors name the same model differently: "claude-opus-5-5" here,
// "anthropic/claude-opus-5.5" there, with or without a date. CanonicalModelKey
// is the one name they are compared by, to tell that they are the same model.

import (
	"regexp"
	"strings"
)

// modelDateRE is the 8-digit date some vendors append to a model's name.
var modelDateRE = regexp.MustCompile(`-\d{8}$`)

// CanonicalModelKey is a model name in the form it is compared by: lower
// case; without a vendor prefix ("anthropic/"); with a version's dot as a
// hyphen ("5.5" is "5-5"); without an 8-digit date suffix. A variant after a
// colon (":batch") makes a different model, so it is kept as it is, and a dot
// that does not stand between two digits ("v1.beta") is kept too.
func CanonicalModelKey(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	base, variant := s, ""
	if i := strings.IndexByte(s, ':'); i >= 0 {
		base, variant = s[:i], s[i:]
	}
	if i := strings.IndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	base = modelDateRE.ReplaceAllString(base, "")
	return dotsToHyphens(base) + variant
}

// dotsToHyphens turns each dot between two digits into a hyphen.
func dotsToHyphens(s string) string {
	b := []byte(s)
	for i := 1; i+1 < len(b); i++ {
		if b[i] == '.' && isASCIIDigit(b[i-1]) && isASCIIDigit(b[i+1]) {
			b[i] = '-'
		}
	}
	return string(b)
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }
