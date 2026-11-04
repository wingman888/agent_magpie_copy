// Package backup gathers the whole magpie setup into one passphrase-sealed
// file and puts it back on another machine. The file holds the providers
// (keys, accounts, headers), their chosen icons, settings, saved profiles and
// the agents' current model choices. Subscription logins are not in it: they
// stay with each agent on each machine.
package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Bundle is everything a backup carries, before sealing.
type Bundle struct {
	Version   int                         `json:"version"`
	App       string                      `json:"app,omitempty"`      // magpie version that made it
	Providers []provider.Provider         `json:"providers,omitempty"`
	Icons     map[string][]byte           `json:"icons,omitempty"`    // picture file name → content
	Settings  *settings.Settings          `json:"settings,omitempty"` // nil when the machine has none
	Profiles  map[string]profile.Profile  `json:"profiles,omitempty"`
	Agents    profile.Profile             `json:"agents,omitempty"` // "agent.field" → value
}

// Parts selects which parts of a bundle Restore applies.
type Parts struct {
	Providers, Settings, Profiles, Agents bool
}

// All selects every part.
var All = Parts{Providers: true, Settings: true, Profiles: true, Agents: true}

// Result says what a restore did.
type Result struct {
	Added    int      // providers new to this machine
	Replaced int      // providers that replaced one with the same id
	NeedKey  []string // names of providers left with no key at all
	Settings bool     // settings were restored
	Profiles int      // profiles written
	Agents   int      // agent settings changed
	Skipped  []string // items not applied, with the reason
}

// Collect gathers this machine's setup. With keys false, no API key is
// included and credential-looking headers are dropped.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: currentVer, App: app, Icons: map[string][]byte{}}
	for _, p := range provider.Stored() {
		if !keys {
			p.Key = ""
			for i := range p.Keys {
				p.Keys[i].Key = ""
			}
			p.Headers = plainHeaders(p.Headers)
		}
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			data, err := readIcon(name)
			if err != nil {
				p.Icon = "" // a picture that cannot be read is not carried
			} else {
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
	b.Profiles = ps
	b.Agents = profile.Snapshot()
	return b, nil
}

// Restore applies the selected parts of b to this machine.
func Restore(b Bundle, parts Parts) (Result, error) {
	var r Result
	if b.Version > currentVer {
		return r, fmt.Errorf("需升级 magpie 到更新版本：备份格式版本为 %d", b.Version)
	}
	if parts.Settings && b.Settings != nil {
		if err := settings.Save(*b.Settings); err != nil {
			return r, err
		}
		r.Settings = true
	}
	if parts.Providers {
		if err := restoreProviders(b, &r); err != nil {
			return r, err
		}
	}
	if parts.Profiles {
		if err := restoreProfiles(b, &r); err != nil {
			return r, err
		}
	}
	if parts.Agents {
		restoreAgents(b.Agents, &r)
	}
	return r, nil
}

// restoreProviders merges the bundle's providers into providers.json by id.
// Icons go in first, then the file is written once.
func restoreProviders(b Bundle, r *Result) error {
	out := provider.Stored()
	pos := map[string]int{}
	for i, p := range out {
		pos[p.ID] = i
	}
	var accounts map[string]bool // signed-in agents here, looked up on first need
	written := map[string]bool{}
	var order []string
	changed := false

	for _, bp := range b.Providers {
		id := strings.ToLower(strings.TrimSpace(bp.ID))
		if id == "" || id == "magpie" || id != provider.Slug(id) {
			continue
		}
		prev, had := pos[id]

		if bp.Chat == "" && bp.Responses == "" && bp.Anthropic == "" {
			// Model picks for a signed-in agent. Anything else without a
			// base URL is not a provider and is ignored.
			if accounts == nil {
				accounts = map[string]bool{}
				for _, a := range provider.Accounts() {
					accounts[a.ID] = true
				}
			}
			if !accounts[id] || (had && out[prev].Chat+out[prev].Responses+out[prev].Anthropic != "") {
				continue
			}
			stub := provider.Provider{ID: id, Models: bp.Models, Fallback: bp.Fallback, Routing: bp.Routing, Hidden: bp.Hidden}
			if had {
				out[prev] = stub
			} else {
				pos[id] = len(out)
				out = append(out, stub)
			}
			changed = true
			continue
		}

		p := bp
		p.ID = id
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			p.Name = id
		}
		p.IconURL = ""
		p.Account = nil
		p.Keys = usableKeys(p.Keys)
		if p.Key == "" && len(p.Keys) > 0 {
			first := p.Keys[0]
			p.Key, p.KeyName, p.KeyProtocol = first.Key, first.Name, first.Protocol
			p.Keys = p.Keys[1:]
		}
		if p.Key == "" && len(p.Keys) == 0 {
			// The backup carries no key: keep this machine's own.
			if had {
				p.Key, p.KeyName, p.KeyProtocol, p.Keys = out[prev].Key, out[prev].KeyName, out[prev].KeyProtocol, out[prev].Keys
			} else {
				p.KeyName, p.KeyProtocol = "", ""
			}
		}
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			if !placeIcon(name, b.Icons[name]) {
				p.Icon = ""
			}
		}
		if had {
			out[prev] = p
		} else {
			pos[id] = len(out)
			out = append(out, p)
		}
		changed = true
		if !written[id] {
			written[id] = true
			order = append(order, id)
			if had {
				r.Replaced++
			} else {
				r.Added++
			}
		}
	}

	if changed {
		if err := provider.PutStored(out); err != nil {
			return err
		}
	}
	for _, id := range order {
		p := out[pos[id]]
		if p.Key == "" && len(p.Keys) == 0 && !p.Ready() {
			r.NeedKey = append(r.NeedKey, p.Name)
		}
	}
	return nil
}

// usableKeys drops saved accounts that carry no key value.
func usableKeys(ks []provider.KeyAccount) []provider.KeyAccount {
	var out []provider.KeyAccount
	for _, k := range ks {
		if k.Key != "" {
			out = append(out, k)
		}
	}
	return out
}

// placeIcon writes a picture from the bundle unless one of that name is
// already here. It reports whether the picture is available afterwards.
func placeIcon(name string, data []byte) bool {
	path := provider.IconFile(name)
	if path == "" {
		return false
	}
	if _, err := os.Stat(path); err == nil {
		return true
	}
	if len(data) == 0 || len(data) > provider.MaxIcon {
		return false
	}
	sum := sha256.Sum256(data)
	if !strings.HasPrefix(name, hex.EncodeToString(sum[:8])+".") {
		return false
	}
	if err := os.MkdirAll(provider.IconDir(), 0o755); err != nil {
		return false
	}
	return os.WriteFile(path, data, 0o644) == nil
}

func readIcon(name string) ([]byte, error) {
	path := provider.IconFile(name)
	if path == "" {
		return nil, fmt.Errorf("icon %q has an unusable name", name)
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > provider.MaxIcon {
		return nil, fmt.Errorf("icon %q is too large", name)
	}
	return os.ReadFile(path)
}

func restoreProfiles(b Bundle, r *Result) error {
	names := make([]string, 0, len(b.Profiles))
	for n := range b.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if strings.TrimSpace(n) == "" {
			continue
		}
		p := b.Profiles[n]
		if p == nil {
			p = profile.Profile{}
		}
		if err := profile.Save(n, p); err != nil {
			return err
		}
		r.Profiles++
	}
	return nil
}

// restoreAgents applies each agent setting on an installed agent, in the
// order profile.Apply uses: providers, then models, then the rest. A setting
// that fails is skipped and the others still apply.
func restoreAgents(vals profile.Profile, r *Result) {
	byID := map[string]*agent.Agent{}
	for _, a := range agent.All() {
		byID[a.ID] = a
	}
	installed := map[string]bool{}
	reported := map[string]bool{}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if ri, rj := agentRank(keys[i]), agentRank(keys[j]); ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		v := vals[k]
		id, field, ok := strings.Cut(k, ".")
		if !ok || v == "" {
			continue
		}
		a := byID[id]
		if a == nil {
			r.Skipped = append(r.Skipped, fmt.Sprintf("%s：未知的 Agent，已跳过", k))
			continue
		}
		if _, seen := installed[id]; !seen {
			installed[id] = a.Detected()
		}
		if !installed[id] {
			if !reported[id] {
				reported[id] = true
				r.Skipped = append(r.Skipped, fmt.Sprintf("%s：本机未安装，已跳过其设置", a.Name))
			}
			continue
		}
		f := a.Field(field)
		if f == nil {
			r.Skipped = append(r.Skipped, fmt.Sprintf("%s：未知的设置项，已跳过", k))
			continue
		}
		if f.Get() == v {
			continue
		}
		if err := f.Set(v); err != nil {
			r.Skipped = append(r.Skipped, fmt.Sprintf("%s：暂不可用（%v），已跳过", k, err))
			continue
		}
		r.Agents++
	}
}

func agentRank(k string) int {
	switch {
	case strings.HasSuffix(k, ".provider"):
		return 0
	case strings.HasSuffix(k, ".model"):
		return 1
	}
	return 2
}

// plainHeaders keeps the headers that do not look like credentials.
func plainHeaders(h map[string]string) map[string]string {
	out := map[string]string{}
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

var credentialWords = []string{"auth", "key", "token", "secret", "cookie", "session", "password"}

func credentialHeader(name string) bool {
	l := strings.ToLower(name)
	for _, w := range credentialWords {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}
