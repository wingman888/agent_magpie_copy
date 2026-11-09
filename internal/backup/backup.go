// Package backup moves magpie's configuration from one machine to another:
// providers (with their keys unless asked not to), their pictures, the
// settings, the saved profiles and each agent's current choices, sealed in a
// single file under a passphrase (see seal.go).
//
// Signed-in subscriptions are never part of a bundle: their sign-in lives in
// each agent's own files on each machine.
package backup

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Ext is the extension of a backup file.
const Ext = ".magpie-backup"

// version is the newest bundle layout this build reads and writes.
const version = 1

// iconFilePrefix marks a provider icon kept in the icons folder.
const iconFilePrefix = "file:"

// Bundle is everything a backup carries. A nil Settings, or an empty map,
// means "not in this bundle": restoring it leaves the machine's own alone.
type Bundle struct {
	Version   int                        `json:"version"`
	App       string                     `json:"app,omitempty"`
	Providers []provider.Provider        `json:"providers,omitempty"`
	Icons     map[string][]byte          `json:"icons,omitempty"` // picture file name -> content
	Settings  *settings.Settings         `json:"settings,omitempty"`
	Profiles  map[string]profile.Profile `json:"profiles,omitempty"`
	Agents    profile.Profile            `json:"agents,omitempty"` // "agent.field" -> value
}

// credentialWords mark a custom header as carrying a secret.
var credentialWords = []string{"auth", "key", "token", "secret", "cookie", "session", "password"}

// Collect gathers this machine's configuration. With keys false it holds no
// API key of any kind and no header that looks like a credential. app names
// the magpie build that made it.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: version, App: app}

	for _, p := range provider.All() {
		if p.Account != nil {
			continue // a signed-in subscription: stays on its own machine
		}
		if !keys {
			p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
			p.Headers = withoutCredentialHeaders(p.Headers)
		}
		if name, ok := strings.CutPrefix(p.Icon, iconFilePrefix); ok {
			path := provider.IconFile(name)
			data, err := readOptional(path)
			if err != nil {
				return Bundle{}, err
			}
			if data == nil {
				p.Icon = "" // the picture is gone; do not carry a dangling name
			} else {
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
		return Bundle{}, err
	}
	if len(ps) > 0 {
		b.Profiles = ps
	}

	if a := profile.Snapshot(); len(a) > 0 {
		b.Agents = a
	}
	return b, nil
}

// readOptional reads a file, returning nil (and no error) when it is absent
// or its name is not a picture name magpie uses.
func readOptional(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

func withoutCredentialHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if !credentialHeader(k) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func credentialHeader(name string) bool {
	n := strings.ToLower(name)
	for _, w := range credentialWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// Parts chooses which sections Restore touches.
type Parts struct{ Providers, Settings, Profiles, Agents bool }

// All selects every section.
var All = Parts{Providers: true, Settings: true, Profiles: true, Agents: true}

// Result says what Restore did.
type Result struct {
	Added    int      // providers that were new on this machine
	Replaced int      // providers that replaced one with the same id
	NeedKey  []string // names of providers left without any key
	Settings bool     // settings were restored
	Profiles int      // profiles written
	Agents   int      // agent settings changed
	Skipped  []string // agent settings not applied, and why
}

// Restore applies the chosen parts of b to this machine. Providers come
// first, so that the agent models which depend on them can take effect.
func Restore(b Bundle, parts Parts) (Result, error) {
	var r Result
	if b.Version > version {
		return r, errNewer(b.Version)
	}
	if parts.Providers {
		if err := restoreProviders(b, &r); err != nil {
			return r, err
		}
	}
	if parts.Settings && b.Settings != nil {
		if err := settings.Save(*b.Settings); err != nil {
			return r, err
		}
		r.Settings = true
	}
	if parts.Profiles {
		n, err := restoreProfiles(b.Profiles)
		r.Profiles = n
		if err != nil {
			return r, err
		}
	}
	if parts.Agents {
		restoreAgents(b.Agents, &r)
	}
	return r, nil
}

// restoreProviders matches providers by id. A provider that comes without
// any key keeps the key and key accounts this machine already has.
func restoreProviders(b Bundle, r *Result) error {
	// Subscriptions on this machine own their ids; a backup entry may not take one.
	accounts := map[string]bool{}
	for _, a := range provider.Accounts() {
		accounts[a.ID] = true
	}
	local := map[string]provider.Provider{}
	for _, p := range provider.All() {
		if p.Account == nil {
			local[p.ID] = p
		}
	}

	cur := map[string]provider.Provider{} // what will be written, by id
	var order []string                    // ids in the order they are written
	for _, in := range b.Providers {
		p := in
		p.ID = strings.ToLower(strings.TrimSpace(p.ID))
		if p.ID == "" || p.ID != provider.Slug(p.ID) || p.ID == "magpie" || accounts[p.ID] {
			continue
		}
		if p.Name == "" {
			p.Name = p.ID
		}

		prev, inCur := cur[p.ID]
		seen := inCur
		if !seen {
			prev, seen = local[p.ID]
		}
		if seen {
			r.Replaced++
		} else {
			r.Added++
		}
		if !inCur {
			order = append(order, p.ID)
		}
		if p.Key == "" && len(p.Keys) == 0 && seen {
			p.Key, p.KeyName, p.KeyProtocol, p.Keys = prev.Key, prev.KeyName, prev.KeyProtocol, prev.Keys
			// Keep this machine's credential headers the backup does not carry.
			// A header the backup does carry keeps the backup's value.
			for k, v := range prev.Headers {
				if !credentialHeader(k) {
					continue
				}
				if _, ok := p.Headers[k]; ok {
					continue
				}
				merged := make(map[string]string, len(p.Headers)+1)
				for hk, hv := range p.Headers {
					merged[hk] = hv
				}
				merged[k] = v
				p.Headers = merged
			}
		}

		if name, ok := strings.CutPrefix(p.Icon, iconFilePrefix); ok {
			if err := writeIcon(name, b.Icons[name]); err != nil {
				return err
			}
			// Drop the reference if no picture of that name ends up on this machine.
			if path := provider.IconFile(name); path == "" {
				p.Icon = ""
			} else if _, err := os.Stat(path); err != nil {
				p.Icon = ""
			}
		}
		cur[p.ID] = p
	}

	out := make([]provider.Provider, 0, len(order))
	for _, id := range order {
		p := cur[id]
		out = append(out, p)
		if p.Key == "" && len(p.Keys) == 0 {
			r.NeedKey = append(r.NeedKey, p.Name)
		}
	}
	return provider.PutAll(out)
}

// writeIcon keeps a picture under its name unless one of that name exists
// already. Names magpie would not use and oversized pictures are ignored.
func writeIcon(name string, data []byte) error {
	path := provider.IconFile(name)
	if path == "" || data == nil || len(data) > provider.MaxIcon {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(provider.IconDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// restoreProfiles writes each profile by name, replacing one with the same
// name and keeping the others.
func restoreProfiles(ps map[string]profile.Profile) (int, error) {
	names := make([]string, 0, len(ps))
	for n := range ps {
		names = append(names, n)
	}
	sort.Strings(names)
	n := 0
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			continue
		}
		p := ps[name]
		if p == nil {
			p = profile.Profile{}
		}
		if err := profile.Save(name, p); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// restoreAgents applies each agent setting one at a time. Agents not
// installed here are skipped, and so is any setting the agent refuses.
func restoreAgents(vals profile.Profile, r *Result) {
	if len(vals) == 0 {
		return
	}
	agents := map[string]*agent.Agent{}
	for _, a := range agent.All() {
		agents[a.ID] = a
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	// Same order as profile.Apply: providers, then models, then the rest.
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

	absent := map[string]bool{}
	for _, k := range keys {
		id, field, ok := strings.Cut(k, ".")
		a := agents[id]
		if !ok || a == nil || !a.Detected() {
			if !ok {
				r.Skipped = append(r.Skipped, k+"：配置项格式不对")
				continue
			}
			if !absent[id] {
				absent[id] = true
				name := id
				if a != nil {
					name = a.Name
				}
				r.Skipped = append(r.Skipped, name+"：本机未安装该 Agent")
			}
			continue
		}
		f := a.Field(field)
		if f == nil {
			r.Skipped = append(r.Skipped, k+"：该 Agent 没有这个配置项")
			continue
		}
		if f.Get() == vals[k] {
			continue
		}
		if err := f.Set(vals[k]); err != nil {
			r.Skipped = append(r.Skipped, fmt.Sprintf("%s：模型暂时不可用（%v）", k, err))
			continue
		}
		r.Agents++
	}
}
