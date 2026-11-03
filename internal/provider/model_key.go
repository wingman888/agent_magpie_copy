package provider

import (
	"regexp"
	"strings"
)

// Same-model recognition: magpie groups the models several providers serve
// under one name only when they are the same model once written alike. A
// vendor prefix, case, a dot between version numbers and a trailing date
// don't make a different model; a colon variant (":batch") does.

var (
	versionDot = regexp.MustCompile(`(\d)\.(\d)`)
	dateSuffix = regexp.MustCompile(`-\d{8}$`)
	slugBad    = regexp.MustCompile(`[^a-z0-9-]`)
	slugDashes = regexp.MustCompile(`-{2,}`)
)

// canonicalModel is the form a model id is compared by.
func canonicalModel(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	// Replace one dot at a time so "1.2.3" becomes "1-2-3".
	for {
		n := versionDot.ReplaceAllString(s, "$1-$2")
		if n == s {
			break
		}
		s = n
	}
	return dateSuffix.ReplaceAllString(s, "")
}

// autoGroupID is the id of the group automatically made for a canonical
// model: "auto-" and the model in lowercase letters, digits and hyphens.
func autoGroupID(canon string) string {
	s := strings.ToLower(canon)
	s = slugBad.ReplaceAllString(s, "-")
	s = slugDashes.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return "auto-" + s
}

// modelRef is one provider's model that a group is named from.
type modelRef struct {
	Provider string // the provider's id
	ID       string // the model's id there
	Name     string // its official display name, may be empty
}

// groupName names a group of the same model: the official display name of
// the first member that has one (one that differs from its id), otherwise
// the canonical form with its words capitalized, e.g. "Claude Opus 5-5".
func groupName(canon string, refs []modelRef) string {
	for _, r := range refs {
		if n := strings.TrimSpace(r.Name); n != "" && n != r.ID {
			return n
		}
	}
	parts := strings.Split(canon, "-")
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			// Between two numbers the hyphen stays ("5-5"); elsewhere a space.
			if isDigits(parts[i-1]) && isDigits(p) {
				b.WriteString("-")
			} else {
				b.WriteString(" ")
			}
		}
		if p != "" {
			b.WriteString(strings.ToUpper(p[:1]) + p[1:])
		}
	}
	return b.String()
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
