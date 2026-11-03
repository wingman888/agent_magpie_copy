// Package backup moves magpie's configuration from one machine to another as
// a single encrypted file: providers (keys included unless asked not to),
// their custom icons, the settings, the saved profiles and the models each
// agent uses. Subscriptions are not part of it: their sign-in lives on each
// machine and is never read here.
//
// The file format and its encryption are in seal.go.
package backup

import (
	"os"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Ext is the extension a backup file is given.
const Ext = ".magpie-backup"

// FormatVersion is the version of the backup format this build writes and
// the newest it can read. A file written by a newer magpie is refused.
const FormatVersion = 1

// iconPrefix marks a provider icon that is a picture kept in the icons folder.
const iconPrefix = "file:"

// Bundle is everything a backup holds, before it is sealed.
type Bundle struct {
	Version   int                        `json:"version"`
	App       string                     `json:"app,omitempty"`
	Providers []provider.Provider        `json:"providers,omitempty"`
	Icons     map[string][]byte          `json:"icons,omitempty"`    // icon file name -> picture
	Settings  *settings.Settings         `json:"settings,omitempty"` // nil when the machine has none
	Profiles  map[string]profile.Profile `json:"profiles,omitempty"`
	Agents    profile.Profile            `json:"agents,omitempty"` // "agent.field" -> value
}

// Parts chooses which parts of a bundle Restore applies.
type Parts struct {
	Providers, Settings, Profiles, Agents bool
}

// All restores everything.
var All = Parts{Providers: true, Settings: true, Profiles: true, Agents: true}

// Result reports what Restore did.
type Result struct {
	Added    int      // providers that were new on this machine
	Replaced int      // providers that replaced one with the same id
	Settings bool     // whether the settings were restored
	Profiles int      // profiles written
	Agents   int      // agent settings changed
	Skipped  []string // agent settings left alone, with the reason
	NeedKey  []string // names of providers that have no key after restoring
}

// Collect gathers this machine's configuration. With keys false it carries no
// API key and no header that looks like a credential. app names the version
// of magpie that made it.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: FormatVersion, App: app, Icons: map[string][]byte{}}
	for _, p := range provider.Stored() {
		// Entries without a URL are only the model picks of a signed-in
		// account or a hidden account marker: nothing to carry.
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			continue
		}
		if !keys {
			stripKeys(&p)
			p.Headers = withoutCredentials(p.Headers)
		}
		if name, ok := strings.CutPrefix(p.Icon, iconPrefix); ok {
			data, err := os.ReadFile(provider.IconFile(name))
			if err != nil || len(data) == 0 || len(data) > provider.MaxIcon {
				p.Icon = ""
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

// stripKeys removes every key a provider holds: the first and the saved ones.
func stripKeys(p *provider.Provider) {
	p.Key = ""
	p.KeyName = ""
	p.KeyProtocol = ""
	p.Keys = nil
}

// credentialWords mark a header whose value is a secret.
var credentialWords = []string{"auth", "key", "token", "secret", "cookie", "session", "password"}

// withoutCredentials drops the headers whose names look like credentials.
// It returns nil when nothing is left, so the field stays out of the JSON.
func withoutCredentials(h map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		if !looksLikeCredential(k) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func looksLikeCredential(name string) bool {
	n := strings.ToLower(name)
	for _, w := range credentialWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// Restore applies the parts of b selected by parts to this machine. Parts
// not selected are left alone. Providers are restored before the agents,
// so the models that depend on them can take effect.
func Restore(b Bundle, parts Parts) (Result, error) {
	var res Result
	if parts.Settings && b.Settings != nil {
		if err := settings.Save(*b.Settings); err != nil {
			return res, err
		}
		res.Settings = true
	}
	if parts.Profiles {
		for _, name := range sortedNames(b.Profiles) {
			if strings.TrimSpace(name) == "" {
				continue
			}
			if err := profile.Save(name, b.Profiles[name]); err != nil {
				return res, err
			}
			res.Profiles++
		}
	}
	if parts.Providers {
		if err := restoreProviders(b, &res); err != nil {
			return res, err
		}
	}
	if parts.Agents {
		restoreAgents(b, &res)
	}
	return res, nil
}

// validID is an id a provider can have on this machine.
func validID(id string) bool {
	return id != "" && id != "magpie" && provider.Slug(id) == id
}

func restoreProviders(b Bundle, res *Result) error {
	local := map[string]provider.Provider{}
	for _, p := range provider.Stored() {
		local[p.ID] = p
	}
	for _, p := range b.Providers {
		p.ID = strings.ToLower(strings.TrimSpace(p.ID))
		if !validID(p.ID) {
			continue
		}
		old, had := local[p.ID]
		if had && p.Key == "" && len(p.Keys) == 0 {
			// The backup carries no keys: keep the ones this machine has.
			p.Key, p.KeyName, p.KeyProtocol, p.Keys = old.Key, old.KeyName, old.KeyProtocol, old.Keys
		}
		replaced, err := provider.Put(p)
		if err != nil {
			return err
		}
		if replaced {
			res.Replaced++
		} else {
			res.Added++
		}
		local[p.ID] = p
		if name, ok := strings.CutPrefix(p.Icon, iconPrefix); ok {
			if err := keepIcon(name, b.Icons[name]); err != nil {
				return err
			}
		}
		// Without a first key and without saved keys it needs one from the user.
		if p.Key == "" && len(p.Keys) == 0 {
			// Put gives an unnamed provider its id as name; report the same.
			name := p.Name
			if name == "" {
				name = p.ID
			}
			res.NeedKey = append(res.NeedKey, name)
		}
	}
	return nil
}

// keepIcon writes a picture into the icons folder unless one of that name is
// already there. A name that is not a stored picture's, or a picture that is
// empty or too large, is ignored.
func keepIcon(name string, data []byte) error {
	path := provider.IconFile(name)
	if path == "" || len(data) == 0 || len(data) > provider.MaxIcon {
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

// agentRank orders settings so that providers are set first and models
// after them, the same order profiles are applied in.
func agentRank(k string) int {
	switch {
	case strings.HasSuffix(k, ".provider"):
		return 0
	case strings.HasSuffix(k, ".model"):
		return 1
	}
	return 2
}

func restoreAgents(b Bundle, res *Result) {
	byID := map[string]*agent.Agent{}
	for _, a := range agent.All() {
		byID[a.ID] = a
	}
	keys := make([]string, 0, len(b.Agents))
	for k := range b.Agents {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if ri, rj := agentRank(keys[i]), agentRank(keys[j]); ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})

	installed := map[string]bool{}
	reported := map[string]bool{}
	for _, k := range keys {
		value := b.Agents[k]
		id, field, ok := strings.Cut(k, ".")
		if !ok || value == "" {
			continue
		}
		a := byID[id]
		if a == nil {
			if !reported[id] {
				reported[id] = true
				res.Skipped = append(res.Skipped, id+": 未知的 Agent，已跳过")
			}
			continue
		}
		if _, seen := installed[id]; !seen {
			installed[id] = a.Detected()
		}
		if !installed[id] {
			if !reported[id] {
				reported[id] = true
				res.Skipped = append(res.Skipped, id+": 本机未安装，已跳过")
			}
			continue
		}
		f := a.Field(field)
		if f == nil {
			res.Skipped = append(res.Skipped, k+": 本机没有该设置项")
			continue
		}
		if f.Get() == value {
			continue
		}
		if err := f.Set(value); err != nil {
			res.Skipped = append(res.Skipped, k+": 模型暂时不可用（"+err.Error()+"）")
			continue
		}
		res.Agents++
	}
}

func sortedNames(ps map[string]profile.Profile) []string {
	names := make([]string, 0, len(ps))
	for n := range ps {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
