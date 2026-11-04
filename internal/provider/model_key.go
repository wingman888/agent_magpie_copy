package provider

import (
	"regexp"
	"strings"
)

// dateSuffix is a trailing 8-digit date, as vendors put on snapshots:
// claude-opus-5-5-20260801.
var dateSuffix = regexp.MustCompile(`-\d{8}$`)

// NormalizeModelKey says which models are the same one across vendors, for
// routing groups to be made of them: the case is ignored, a vendor prefix
// ("anthropic/") is dropped, a version's dot is read as a hyphen (5.5 is
// 5-5), and a trailing date snapshot (-20260801) is dropped. A colon variant
// (":batch") is a model of its own, so it is kept as it is, and so is a dot
// that doesn't sit between two digits (v1.beta).
func NormalizeModelKey(id string) string {
	id = strings.ToLower(id)
	base, variant := id, ""
	if i := strings.Index(id, ":"); i >= 0 {
		base, variant = id[:i], id[i:]
	}
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	var b strings.Builder
	for i := 0; i < len(base); i++ {
		c := base[i]
		if c == '.' && i > 0 && i+1 < len(base) && isDigit(base[i-1]) && isDigit(base[i+1]) {
			c = '-'
		}
		b.WriteByte(c)
	}
	base = dateSuffix.ReplaceAllString(b.String(), "")
	return base + variant
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
