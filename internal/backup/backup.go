// Package backup exports the whole of a magpie setup — providers with their
// keys, their pictures, settings, profiles and each agent's model choice — to
// one passphrase-encrypted file, and puts it back on another machine.
// Subscriptions (agents the user signed in to) are never part of it.
package backup

import (
	"os"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Ext is the extension of a backup file.
const Ext = ".magpie-backup"

// Bundle is what a backup holds.
type Bundle struct {
	Version   int                        `json:"version"`
	App       string                     `json:"app,omitempty"`
	Created   string                     `json:"created,omitempty"`
	Keys      bool                       `json:"keys"`
	Providers []provider.Provider        `json:"providers"`
	Icons     map[string][]byte          `json:"icons,omitempty"`
	Settings  *settings.Settings         `json:"settings,omitempty"`
	Profiles  map[string]profile.Profile `json:"profiles,omitempty"`
	Agents    map[string]string          `json:"agents,omitempty"`
}

// Parts picks what Restore puts back.
type Parts struct{ Providers, Settings, Profiles, Agents bool }

// All is every part.
var All = Parts{true, true, true, true}

// Result is what Restore did.
type Result struct {
	Added, Replaced int
	NeedKey         []string // names of providers left with no key
	Settings        bool
	Profiles        int
	Agents          int
	Skipped         []string
}

var credWords = []string{"auth", "key", "token", "secret", "cookie", "session", "password"}

func credHeader(name string) bool {
	n := strings.ToLower(name)
	for _, w := range credWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// Collect gathers this machine's setup. Without keys, no API key and no
// header that looks like a credential is included.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: curVersion, App: app, Created: time.Now().UTC().Format(time.RFC3339), Keys: keys}
	for _, p := range provider.Stored() {
		p.Account, p.IconURL = nil, ""
		if !keys {
			p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
		}
		if len(p.Headers) > 0 {
			h := map[string]string{}
			for k, v := range p.Headers {
				if keys || !credHeader(k) {
					h[k] = v
				}
			}
			if len(h) == 0 {
				h = nil
			}
			p.Headers = h
		}
		if name := provider.IconFileName(p.Icon); name != "" {
			if data, err := os.ReadFile(provider.IconFile(name)); err == nil && len(data) > 0 && len(data) <= provider.MaxIcon {
				if b.Icons == nil {
					b.Icons = map[string][]byte{}
				}
				b.Icons[name] = data
			}
		}
		b.Providers = append(b.Providers, p)
	}
	if _, err := os.Stat(settings.Path()); err == nil {
		s := settings.Load()
		b.Settings = &s
	}
	ps, err := profile.Load()
	if err != nil {
		return b, err
	}
	if len(ps) > 0 {
		b.Profiles = ps
	}
	// the agents' current choices, minus any that point at a signed-in
	// subscription: that login stays on its own machine
	var accounts []string
	for _, a := range provider.Accounts() {
		accounts = append(accounts, a.ID)
	}
	ag := map[string]string{}
	for k, v := range profile.Snapshot() {
		if refersTo(v, accounts) {
			continue
		}
		ag[k] = v
	}
	if len(ag) > 0 {
		b.Agents = ag
	}
	return b, nil
}

func refersTo(v string, ids []string) bool {
	for _, id := range ids {
		if v == id || strings.HasPrefix(v, id+"/") {
			return true
		}
	}
	return false
}

func rank(k string) int {
	switch {
	case strings.HasSuffix(k, ".provider"):
		return 0
	case strings.HasSuffix(k, ".model"):
		return 1
	}
	return 2
}

// Restore puts the chosen parts of a bundle on this machine.
func Restore(b Bundle, parts Parts) (Result, error) {
	var r Result
	if b.Version > curVersion {
		return r, ErrTooNew
	}
	if parts.Providers {
		// pictures first, so the providers that name them find them
		for name, data := range b.Icons {
			provider.RestoreIcon(name, data)
		}
		added, replaced, need, err := provider.RestoreAll(b.Providers)
		if err != nil {
			return r, err
		}
		r.Added, r.Replaced, r.NeedKey = added, replaced, need
	}
	if parts.Settings && b.Settings != nil {
		if err := settings.Save(*b.Settings); err != nil {
			return r, err
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
				return r, err
			}
			r.Profiles++
		}
	}
	if parts.Agents && len(b.Agents) > 0 {
		r.Agents, r.Skipped = applyAgents(b.Agents)
	}
	return r, nil
}

func applyAgents(vals map[string]string) (int, []string) {
	agents := map[string]*agent.Agent{}
	for _, a := range agent.All() {
		agents[a.ID] = a
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if ri, rj := rank(keys[i]), rank(keys[j]); ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	changed := 0
	var skipped []string
	for _, k := range keys {
		id, field, ok := strings.Cut(k, ".")
		if !ok {
			continue
		}
		a := agents[id]
		if a == nil || !a.Detected() {
			skipped = append(skipped, k+" (not installed on this machine / 本机未安装该 Agent)")
			continue
		}
		f := a.Field(field)
		if f == nil {
			skipped = append(skipped, k+" (unknown setting / 未知设置)")
			continue
		}
		if f.Get() == vals[k] {
			continue
		}
		if err := f.Set(vals[k]); err != nil {
			skipped = append(skipped, k+" (model unavailable for now / 模型暂时不可用)")
			continue
		}
		changed++
	}
	return changed, skipped
}

