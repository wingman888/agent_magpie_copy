package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Parts picks what a restore brings back.
type Parts struct {
	Providers, Settings, Profiles, Agents bool
}

// All is every part.
var All = Parts{Providers: true, Settings: true, Profiles: true, Agents: true}

// Result is what a restore did.
type Result struct {
	Added, Replaced int      // providers new to this machine, and ones written over
	NeedKey         []string // names of providers left with no key at all
	Settings        bool     // the settings were restored
	Profiles        int      // profiles written
	Agents          int      // agent settings changed
	Skipped         []string // agent settings left alone, and why
}

// Restore writes a bundle's chosen parts to this machine: providers (and
// their pictures) first, so the agents' models that hang on them resolve,
// then settings, profiles and the agents. What the bundle lacks, and what
// a part not chosen covers, is left as it is.
func Restore(b Bundle, parts Parts) (Result, error) {
	var r Result
	if parts.Providers {
		if err := restoreProviders(b, &r); err != nil {
			return r, err
		}
	}
	if parts.Settings && b.Settings != nil {
		if err := settings.Save(*b.Settings); err != nil {
			return r, fmt.Errorf("settings: %w", err)
		}
		r.Settings = true
	}
	if parts.Profiles && len(b.Profiles) > 0 {
		names := profile.Names(b.Profiles)
		for _, n := range names {
			if strings.TrimSpace(n) == "" {
				continue
			}
			if err := profile.Save(n, b.Profiles[n]); err != nil {
				return r, fmt.Errorf("profile %q: %w", n, err)
			}
			r.Profiles++
		}
	}
	if parts.Agents && len(b.Agents) > 0 {
		restoreAgents(b.Agents, &r)
	}
	return r, nil
}

func restoreProviders(b Bundle, r *Result) error {
	for name, data := range b.Icons {
		if name != filepath.Base(name) || strings.Contains(name, "..") {
			continue
		}
		path := provider.IconFile(name)
		if path == "" || len(data) == 0 || len(data) > provider.MaxIcon {
			continue
		}
		if _, err := os.Lstat(path); err == nil {
			continue // one already here stays
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}

	local := map[string]provider.Provider{}
	for _, p := range provider.Stored() {
		local[strings.ToLower(strings.TrimSpace(p.ID))] = p
	}
	var put []provider.Provider
	at := map[string]int{}
	for _, p := range b.Providers {
		p.ID = strings.ToLower(strings.TrimSpace(p.ID))
		p.Key = strings.TrimSpace(p.Key)
		for _, u := range []*string{&p.Chat, &p.Responses, &p.Anthropic} {
			*u = strings.TrimSpace(*u)
		}
		// what provider.Put would pass over isn't written, so isn't counted
		if !provider.ValidID(p.ID) || (p.Chat == "" && p.Responses == "" && p.Anthropic == "") {
			continue
		}
		p.Account, p.IconURL = nil, ""
		if old, ok := local[p.ID]; ok && !hasKey(p) {
			// a backup without keys leaves this machine's keys alone
			p.Key, p.KeyName, p.KeyProtocol, p.Keys = old.Key, old.KeyName, old.KeyProtocol, old.Keys
		}
		if i, dup := at[p.ID]; dup {
			put[i] = p
			continue
		}
		at[p.ID] = len(put)
		put = append(put, p)
	}
	added, replaced, err := provider.Put(put)
	if err != nil {
		return err
	}
	r.Added, r.Replaced = added, replaced
	for _, p := range put {
		if !hasKey(p) {
			name := strings.TrimSpace(p.Name)
			if name == "" {
				name = p.ID
			}
			r.NeedKey = append(r.NeedKey, name)
		}
	}
	return nil
}

// hasKey reports whether a provider has a main key or any key account
// with a key, blanks not counting.
func hasKey(p provider.Provider) bool {
	if strings.TrimSpace(p.Key) != "" {
		return true
	}
	for _, k := range p.Keys {
		if strings.TrimSpace(k.Key) != "" {
			return true
		}
	}
	return false
}

// restoreAgents sets each agent's fields one by one: an agent not on this
// machine is passed over, and so is a value it won't take (a model behind
// a subscription not signed in here) without holding up the rest.
func restoreAgents(settings []AgentSetting, r *Result) {
	agents := map[string]*agent.Agent{}
	for _, a := range agent.All() {
		agents[a.ID] = a
	}
	// providers first: switching one re-settles the model behind it; then
	// models, which settle what the other fields hang on
	rank := func(field string) int {
		switch field {
		case "provider":
			return 0
		case "model":
			return 1
		}
		return 2
	}
	items := append([]AgentSetting(nil), settings...)
	sort.SliceStable(items, func(i, j int) bool { return rank(items[i].Field) < rank(items[j].Field) })

	missing := map[string]bool{}
	installed := map[string]bool{}
	for _, s := range items {
		a := agents[s.Agent]
		if a == nil {
			if !missing[s.Agent] {
				missing[s.Agent] = true
				r.Skipped = append(r.Skipped, s.Agent+": magpie doesn't know this agent (未知 Agent)")
			}
			continue
		}
		ok, seen := installed[a.ID]
		if !seen {
			ok = a.Detected()
			installed[a.ID] = ok
		}
		if !ok {
			if !missing[a.ID] {
				missing[a.ID] = true
				r.Skipped = append(r.Skipped, a.Name+": not installed on this machine (本机未安装)")
			}
			continue
		}
		f := a.Field(s.Field)
		if f == nil {
			r.Skipped = append(r.Skipped, fmt.Sprintf("%s %s: this version has no such field", a.Name, s.Field))
			continue
		}
		if f.Get() == s.Value {
			continue
		}
		if err := f.Set(s.Value); err != nil {
			r.Skipped = append(r.Skipped, fmt.Sprintf("%s %s %s: not available for now (模型暂时不可用): %v", a.Name, s.Field, s.Value, err))
			continue
		}
		r.Agents++
	}
}
