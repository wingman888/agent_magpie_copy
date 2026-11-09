package provider

import (
	"regexp"
	"strings"
)

// dateSuffix is a trailing 8-digit date, as vendors add to snapshot names.
var dateSuffix = regexp.MustCompile(`-\d{8}$`)

// NormalizeModelKey is the form two vendors' spellings of one model meet
// in: `claude-opus-5-5`, `anthropic/claude-opus-5.5`, `Claude-Opus-5.5` and
// `claude-opus-5-5-20260801` are all `claude-opus-5-5`. Case is ignored, the
// vendor prefix (up to the last `/`) is dropped, a dot between two digits
// is a version point and reads as a hyphen, and a trailing 8-digit date is
// dropped. A colon variant such as `:batch` is part of the model, kept as
// it is; so is any other dot, as in `v1.beta`.
func NormalizeModelKey(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	base, variant := m, ""
	if i := strings.Index(m, ":"); i >= 0 {
		base, variant = m[:i], m[i:]
	}
	base = dateSuffix.ReplaceAllString(base, "")
	base = versionPoints(base)
	return base + variant
}

// versionPoints turns each dot that has a digit on both sides into a hyphen.
func versionPoints(s string) string {
	b := []byte(s)
	for i := 1; i+1 < len(b); i++ {
		if b[i] == '.' && modelKeyDigit(b[i-1]) && modelKeyDigit(b[i+1]) {
			b[i] = '-'
		}
	}
	return string(b)
}

func modelKeyDigit(c byte) bool { return c >= '0' && c <= '9' }
