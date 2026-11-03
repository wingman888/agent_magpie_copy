package provider

import (
	"regexp"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// modelDateSuffix is the 8-digit release date some vendors append to a model id.
var modelDateSuffix = regexp.MustCompile(`-\d{8}$`)

// NormalizeModelKey reduces a model id to the form under which the same model
// is known across vendors, so that `claude-opus-5-5`, `anthropic/claude-opus-5.5`,
// `Claude-Opus-5.5` and `claude-opus-5-5-20260801` all give `claude-opus-5-5`.
//
// The rules: case is ignored; a vendor prefix (`anthropic/`) is dropped; a dot
// between two digits is a version separator and reads as a hyphen (`5.5` is
// `5-5`); a trailing 8-digit date is dropped. A colon starts a variant
// (`:batch`) that makes a different model, so the variant is kept verbatim and
// the rules above apply only before it. Other dots (`v1.beta`) are kept.
func NormalizeModelKey(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	variant := ""
	if i := strings.Index(s, ":"); i >= 0 {
		s, variant = s[:i], s[i:]
	}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	s = versionDotsToHyphens(s)
	s = modelDateSuffix.ReplaceAllString(s, "")
	return s + variant
}

// versionDotsToHyphens turns each dot lying between two digits into a hyphen.
// It reads the original string throughout, so `1.2.3` gives `1-2-3`.
func versionDotsToHyphens(s string) string {
	b := []byte(s)
	for i := 1; i+1 < len(s); i++ {
		if s[i] == '.' && isASCIIDigit(s[i-1]) && isASCIIDigit(s[i+1]) {
			b[i] = '-'
		}
	}
	return string(b)
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

// OfficialModelName is the name to show for a model several vendors list: the
// first one that has a real display name, not just its id; the first id if
// none has.
func OfficialModelName(ms []catalog.Model) string {
	for _, m := range ms {
		if n := strings.TrimSpace(m.Name); n != "" && n != m.ID {
			return n
		}
	}
	if len(ms) > 0 {
		return ms[0].ID
	}
	return ""
}
