// Package agent describes every coding agent magpie can drive: where its config
// lives, which fields matter (model, effort, …) and which values to offer.
package agent

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

// Option is one value the picker offers for a field.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"` // display name, when the value is an id
	Note  string `json:"note"`
	Icon  string `json:"icon,omitempty"`  // bundled icon name
	Group string `json:"group,omitempty"` // section header in the picker
	Ref   string `json:"ref,omitempty"`   // the catalog model, the same in every agent
}

// Field is one tunable setting of an agent. Set with an empty value puts
// the field back to the agent's own default: anything magpie wired in (the
// gateway as a provider, its model catalog) comes out and the key is removed.
type Field struct {
	Key     string
	Label   string
	Get     func() string
	Set     func(string) error
	Options func(cur map[string]string) []Option
	// Quiet fields are left out of listings while empty: they follow
	// another field until set (Claude Code's per-tier models).
	Quiet bool
}

// Agent is one supported coding agent.
type Agent struct {
	ID      string
	Name    string
	Icon    string // bundled icon name (internal/gui/assets/icons)
	Aliases []string
	Bin     string // executable name, used for detection
	Dir     string // config directory, used for detection
	Path    string // config file magpie edits
	Fields  []Field
	// Notice, if set, is advice worth showing after a change: agents that
	// read their config once at start-up need a restart to see it.
	Notice func() string
}

// Running reports whether a process whose command line matches any pattern
// (an extended regexp, as for pgrep -f) is alive. Unknown on Windows.
func Running(patterns ...string) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	for _, pat := range patterns {
		if err := exec.Command("pgrep", "-f", pat).Run(); err == nil {
			return true
		}
	}
	return false
}

// Detected reports whether the agent seems to be installed or configured.
func (a *Agent) Detected() bool {
	if _, err := os.Stat(a.Path); err == nil {
		return true
	}
	if a.Dir != "" {
		if _, err := os.Stat(a.Dir); err == nil {
			return true
		}
	}
	if a.Bin != "" {
		if _, err := exec.LookPath(a.Bin); err == nil {
			return true
		}
	}
	return false
}

// Field looks a field up by key.
func (a *Agent) Field(key string) *Field {
	for i := range a.Fields {
		if a.Fields[i].Key == key {
			return &a.Fields[i]
		}
	}
	for i := range a.Fields {
		if a.Fields[i].Label == key { // `magpie gemini auth …`: the label as shown
			return &a.Fields[i]
		}
	}
	return nil
}

// Values reads every field.
func (a *Agent) Values() map[string]string {
	m := make(map[string]string, len(a.Fields))
	for _, f := range a.Fields {
		m[f.Key] = f.Get()
	}
	return m
}

// Detected returns the agents present on this machine, in display order.
func Detected() []*Agent {
	var out []*Agent
	for _, a := range All() {
		if a.Detected() {
			out = append(out, a)
		}
	}
	return out
}

// Find resolves a user-typed name (id, alias, or unique prefix).
func Find(q string) (*Agent, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	all := All()
	var prefix []*Agent
	for _, a := range all {
		if a.ID == q {
			return a, nil
		}
		for _, al := range a.Aliases {
			if al == q {
				return a, nil
			}
		}
		if strings.HasPrefix(a.ID, q) || strings.HasPrefix(strings.ToLower(a.Name), q) {
			prefix = append(prefix, a)
		}
	}
	switch len(prefix) {
	case 1:
		return prefix[0], nil
	case 0:
		return nil, fmt.Errorf("unknown agent %q (try: %s)", q, strings.Join(ids(all), ", "))
	}
	return nil, fmt.Errorf("%q is ambiguous: %s", q, strings.Join(ids(prefix), ", "))
}

func ids(as []*Agent) []string {
	s := make([]string, len(as))
	for i, a := range as {
		s[i] = a.ID
	}
	sort.Strings(s)
	return s
}
