// Package profile stores named snapshots of every agent's settings so a whole
// setup can be switched in one move.
package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
)

// Profile maps "agent.field" to a value.
type Profile map[string]string

// Path is the profiles file.
func Path() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "magpie", "profiles.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "magpie", "profiles.json")
}

// Load reads every profile.
func Load() (map[string]Profile, error) {
	b, err := edit.Read(Path())
	if err != nil {
		return nil, err
	}
	out := map[string]Profile{}
	if len(b) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", Path(), err)
	}
	return out, nil
}

// Names lists profiles alphabetically.
func Names(ps map[string]Profile) []string {
	names := make([]string, 0, len(ps))
	for n := range ps {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func store(ps map[string]Profile) error {
	b, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(Path(), append(b, '\n'))
}

// Snapshot captures the current value of every detected agent's fields.
func Snapshot() Profile {
	p := Profile{}
	for _, a := range agent.Detected() {
		for k, v := range a.Values() {
			if v != "" {
				p[a.ID+"."+k] = v
			}
		}
	}
	return p
}

// Save stores p under name, replacing any existing profile.
func Save(name string, p Profile) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("profile name is empty")
	}
	ps, err := Load()
	if err != nil {
		return err
	}
	ps[name] = p
	return store(ps)
}

// Delete removes a profile.
func Delete(name string) error {
	ps, err := Load()
	if err != nil {
		return err
	}
	if _, ok := ps[name]; !ok {
		return fmt.Errorf("no profile named %q", name)
	}
	delete(ps, name)
	return store(ps)
}

// Apply writes every value in p that differs from what is set now. It
// returns the number of changes made and the first error encountered.
func Apply(p Profile) (int, error) {
	agents := map[string]*agent.Agent{}
	for _, a := range agent.All() {
		agents[a.ID] = a
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	// providers first: switching one re-settles the model behind it; then
	// models, which settle what the other fields (Claude Code's tiers) hang on
	rank := func(k string) int {
		switch {
		case strings.HasSuffix(k, ".provider"):
			return 0
		case strings.HasSuffix(k, ".model"):
			return 1
		}
		return 2
	}
	sort.Slice(keys, func(i, j int) bool {
		if ri, rj := rank(keys[i]), rank(keys[j]); ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	changed := 0
	for _, k := range keys {
		id, field, ok := strings.Cut(k, ".")
		if !ok {
			continue
		}
		a := agents[id]
		if a == nil {
			continue
		}
		f := a.Field(field)
		if f == nil || f.Get() == p[k] {
			continue
		}
		if err := f.Set(p[k]); err != nil {
			return changed, fmt.Errorf("%s: %w", k, err)
		}
		changed++
	}
	return changed, nil
}

// Summary renders a profile as a short one-line description.
func Summary(p Profile) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		if strings.HasSuffix(k, ".model") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, strings.TrimSuffix(k, ".model")+" "+p[k])
	}
	return strings.Join(parts, " · ")
}
