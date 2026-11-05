// Package backup moves magpie's configuration from one machine to another: the
// providers with their keys, icons, settings, saved profiles and the agents'
// model choices, sealed in one passphrase-protected file (see seal.go).
//
// Signed-in subscriptions never travel: their sign-in lives in each agent,
// and only the model picks magpie keeps for them stay behind as well.
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

// iconPrefix marks a provider's own picture, as provider.StoreIcon names it.
const iconPrefix = "file:"

// Bundle is everything a backup carries, before it is sealed.
type Bundle struct {
	Version   int                        `json:"version"`
	App       string                     `json:"app,omitempty"`
	WithKeys  bool                       `json:"withKeys"`
	Providers []provider.Provider        `json:"providers"`
	Icons     map[string][]byte          `json:"icons,omitempty"` // picture file name → content
	Settings  *settings.Settings         `json:"settings,omitempty"`
	Profiles  map[string]profile.Profile `json:"profiles,omitempty"`
	Agents    profile.Profile            `json:"agents,omitempty"` // "agent.field" → value
}

// Parts chooses which sections a restore touches.
type Parts struct {
	Providers, Settings, Profiles, Agents bool
}

// All restores every section.
var All = Parts{true, true, true, true}

// Result says what a restore did.
type Result struct {
	Added    int      // providers new to this machine
	Replaced int      // providers that replaced one with the same id
	NeedKey  []string // names of providers left without any key
	Settings bool     // settings were restored
	Profiles int      // profiles written
	Agents   int      // agent settings changed
	Skipped  []string // what was not restored, and why
}

// Collect gathers this machine's configuration. With keys false the bundle
// carries no API key and no credential-looking header.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: formatVersion, App: app, WithKeys: keys, Icons: map[string][]byte{}}
	for _, p := range provider.StoredProviders() {
		if !p.HasURL() {
			// the model picks kept for a signed-in account: its sign-in is
			// the agent's, so nothing of it goes into a backup
			continue
		}
		p.Account = nil
		if !keys {
			p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
		}
		p.Headers = keepHeaders(p.Headers, keys)
		if name, ok := strings.CutPrefix(p.Icon, iconPrefix); ok {
			if data, ok := readIcon(name); ok {
				b.Icons[name] = data
			}
		}
		b.Providers = append(b.Providers, p)
	}
	if len(b.Icons) == 0 {
		b.Icons = nil
	}

	if _, err := os.Stat(settings.Path()); err == nil {
		s := settings.Load()
		if !keys {
			s.Proxy = withoutProxyLogin(s.Proxy)
		}
		b.Settings = &s
	}

	ps, err := profile.Load()
	if err != nil {
		return Bundle{}, err
	}
	if len(ps) > 0 {
		b.Profiles = ps
	}

	if snap := profile.Snapshot(); len(snap) > 0 {
		b.Agents = snap
	}
	return b, nil
}

// Restore writes the selected parts of b into this machine. Providers come
// first, then settings, profiles and last the agents, whose models depend on
// the providers.
func Restore(b Bundle, parts Parts) (Result, error) {
	var res Result
	if parts.Providers {
		if err := restoreProviders(b, &res); err != nil {
			return res, err
		}
	}
	if parts.Settings && b.Settings != nil {
		if err := settings.Save(*b.Settings); err != nil {
			return res, fmt.Errorf("恢复设置：%w", err)
		}
		res.Settings = true
	}
	if parts.Profiles {
		restoreProfiles(b, &res)
	}
	if parts.Agents {
		restoreAgents(b, &res)
	}
	return res, nil
}

func restoreProviders(b Bundle, res *Result) error {
	for _, bp := range b.Providers {
		if !validID(bp.ID) {
			continue
		}
		p := bp
		p.Account = nil
		if local, ok := findStored(p.ID); ok && strings.TrimSpace(p.Key) == "" && len(p.Keys) == 0 {
			// a backup without keys keeps this machine's keys and their names,
			// and the credential headers the backup does not carry
			p.Key, p.KeyName, p.KeyProtocol, p.Keys = local.Key, local.KeyName, local.KeyProtocol, local.Keys
			p.Headers = withLocalCredentials(p.Headers, local.Headers)
		}
		p.Icon = restoreIcon(b.Icons, p.Icon)

		replaced, err := provider.PutStored(p)
		if err != nil {
			return fmt.Errorf("恢复提供商 %s：%w", p.ID, err)
		}
		if replaced {
			res.Replaced++
		} else {
			res.Added++
		}
		if strings.TrimSpace(p.Key) == "" && len(p.Keys) == 0 {
			name := strings.TrimSpace(p.Name)
			if name == "" {
				name = p.ID
			}
			res.NeedKey = append(res.NeedKey, name)
		}
	}
	return nil
}

func restoreProfiles(b Bundle, res *Result) {
	names := make([]string, 0, len(b.Profiles))
	for n := range b.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := profile.Save(n, b.Profiles[n]); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("方案 %s：%v", n, err))
			continue
		}
		res.Profiles++
	}
}

// restoreAgents applies each agent value on its own: one that fails, or whose
// agent is not installed here, is listed as skipped and the rest go on.
func restoreAgents(b Bundle, res *Result) {
	if len(b.Agents) == 0 {
		return
	}
	byID := map[string]*agent.Agent{}
	for _, a := range agent.All() {
		byID[a.ID] = a
	}
	installed := map[string]bool{}
	for _, k := range agentKeys(b.Agents) {
		v := b.Agents[k]
		id, field, ok := strings.Cut(k, ".")
		if !ok {
			res.Skipped = append(res.Skipped, k+"（设置项名称不对）")
			continue
		}
		a := byID[id]
		if a == nil {
			res.Skipped = append(res.Skipped, k+"（未知的 Agent）")
			continue
		}
		if _, seen := installed[id]; !seen {
			installed[id] = a.Detected()
		}
		if !installed[id] {
			res.Skipped = append(res.Skipped, k+"（本机未安装 "+a.Name+"）")
			continue
		}
		if v == "" {
			continue
		}
		f := a.Field(field)
		if f == nil {
			res.Skipped = append(res.Skipped, k+"（没有这个设置项）")
			continue
		}
		if f.Get() == v {
			continue
		}
		if err := f.Set(v); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s（%v）", k, err))
			continue
		}
		res.Agents++
	}
}

// agentKeys orders the agent values as profile.Apply does: providers before
// models, models before everything that hangs on them.
func agentKeys(p profile.Profile) []string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
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
	return keys
}

// findStored is the saved entry with this id, keys and all.
func findStored(id string) (provider.Provider, bool) {
	for _, p := range provider.StoredProviders() {
		if p.ID == id {
			return p, true
		}
	}
	return provider.Provider{}, false
}

// validID matches what provider.Save accepts, minus the reserved gateway id.
func validID(id string) bool {
	return id != "" && id == provider.Slug(id) && id != "magpie"
}

// restoreIcon settles a provider's picture on this machine: a picture already
// here with that name is kept, a bundled one is stored when it fits, and
// anything else is dropped rather than left pointing at nothing.
func restoreIcon(icons map[string][]byte, icon string) string {
	name, ok := strings.CutPrefix(icon, iconPrefix)
	if !ok {
		return icon
	}
	if path := provider.IconFile(name); path != "" {
		if _, err := os.Stat(path); err == nil {
			return icon
		}
	}
	data, ok := icons[name]
	if !ok || len(data) > provider.MaxIcon {
		return ""
	}
	stored, err := provider.StoreIcon(data)
	if err != nil {
		return ""
	}
	return stored
}

// readIcon returns a provider's picture from this machine, if it is there and
// within the size limit.
func readIcon(name string) ([]byte, bool) {
	path := provider.IconFile(name)
	if path == "" {
		return nil, false
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 || st.Size() > provider.MaxIcon {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}

// keepHeaders drops credential-looking headers unless keys are kept. Other
// headers stay; nil when nothing is left.
func keepHeaders(h map[string]string, keys bool) map[string]string {
	if keys || len(h) == 0 {
		return h
	}
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

// withLocalCredentials returns h plus every credential-looking header of local
// whose name h does not carry (header names compare case-insensitively). h is
// not modified; it is returned as is when nothing has to be added.
func withLocalCredentials(h, local map[string]string) map[string]string {
	have := map[string]bool{}
	for k := range h {
		have[strings.ToLower(k)] = true
	}
	var out map[string]string
	for k, v := range local {
		if !credentialHeader(k) || have[strings.ToLower(k)] {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(h)+1)
			for hk, hv := range h {
				out[hk] = hv
			}
		}
		out[k] = v
	}
	if out == nil {
		return h
	}
	return out
}

var credentialWords = []string{"auth", "key", "token", "secret", "cookie", "session", "password"}

func credentialHeader(name string) bool {
	n := strings.ToLower(name)
	for _, w := range credentialWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// withoutProxyLogin drops the user:password part of a proxy address.
func withoutProxyLogin(p string) string {
	if !strings.Contains(p, "@") {
		return p
	}
	scheme, rest := "", p
	if i := strings.Index(p, "://"); i >= 0 {
		scheme, rest = p[:i+3], p[i+3:]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	return scheme + rest
}
