package backup

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Bundle is everything a backup carries, before it is encrypted.
type Bundle struct {
	Version int `json:"version"`
	// App is the magpie that made the backup.
	App       string              `json:"app,omitempty"`
	Providers []provider.Provider `json:"providers"`
	// Icons maps a picture's file name to its content.
	Icons map[string][]byte `json:"icons,omitempty"`
	// Settings is nil when the machine has no settings file.
	Settings *settings.Settings `json:"settings,omitempty"`
	Profiles map[string]profile.Profile `json:"profiles,omitempty"`
	// Agents is each installed agent's settings: agent id → field key → value.
	Agents map[string]map[string]string `json:"agents,omitempty"`
}

// Parts chooses what a restore touches.
type Parts struct {
	Providers, Settings, Profiles, Agents bool
}

// All restores every part.
var All = Parts{Providers: true, Settings: true, Profiles: true, Agents: true}

// Result says what a restore did.
type Result struct {
	Added, Replaced int      // providers new to this machine, and providers replaced
	NeedKey         []string // names of restored providers with no key to use
	Settings        bool     // the settings were restored
	Profiles        int      // profiles written
	Agents          int      // agent settings changed
	Skipped         []string // agent settings left alone, with the reason
}

// credentialWords mark a custom header that carries a secret.
var credentialWords = []string{"auth", "key", "token", "secret", "cookie", "session", "password"}

// Collect gathers the setup of this machine. With keys false, no API key is
// included, nor a header whose name looks like a credential, nor a password
// in the proxy address. Signed-in accounts are never included: each machine
// signs in on its own.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: FormatVersion, App: app, Icons: map[string][]byte{}}
	for _, p := range provider.All() {
		if p.Account != nil {
			continue
		}
		if !keys {
			p = withoutKeys(p)
		}
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			data, err := readIcon(name)
			if err != nil {
				p.Icon = "" // a picture that is gone is not worth a dangling reference
			} else {
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
		return b, err
	}
	if len(ps) > 0 {
		b.Profiles = ps
	}
	for _, a := range agent.All() {
		if !a.Detected() {
			continue
		}
		m := map[string]string{}
		for k, v := range a.Values() {
			if v != "" {
				m[k] = v
			}
		}
		if len(m) > 0 {
			if b.Agents == nil {
				b.Agents = map[string]map[string]string{}
			}
			b.Agents[a.ID] = m
		}
	}
	return b, nil
}

// Restore applies the chosen parts of a bundle to this machine. Providers
// go first, then the agents' models, which may depend on them.
func Restore(b Bundle, parts Parts) (Result, error) {
	var r Result
	if b.Version > FormatVersion {
		return r, errNewer(b.Version)
	}
	if parts.Providers {
		writeIcons(b.Icons)
		im, err := provider.Import(b.Providers)
		if err != nil {
			return r, err
		}
		r.Added, r.Replaced, r.NeedKey = im.Added, im.Replaced, im.NoKey
	}
	if parts.Settings && b.Settings != nil {
		if err := settings.Save(*b.Settings); err != nil {
			return r, err
		}
		r.Settings = true
	}
	if parts.Profiles {
		names := make([]string, 0, len(b.Profiles))
		for n := range b.Profiles {
			names = append(names, n)
		}
		sort.Strings(names)
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
	if parts.Agents {
		r.Agents, r.Skipped = applyAgents(b.Agents)
	}
	return r, nil
}

// withoutKeys is p with every API key taken out, and the credential-like
// headers too.
func withoutKeys(p provider.Provider) provider.Provider {
	p.Key, p.KeyName, p.Keys = "", "", nil
	if len(p.Headers) > 0 {
		kept := map[string]string{}
		for k, v := range p.Headers {
			if !credentialHeader(k) {
				kept[k] = v
			}
		}
		p.Headers = nil
		if len(kept) > 0 {
			p.Headers = kept
		}
	}
	return p
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

// withoutProxyLogin drops the user:password part of a proxy address.
func withoutProxyLogin(proxy string) string {
	if !strings.Contains(proxy, "@") {
		return proxy
	}
	raw := proxy
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return proxy
	}
	u.User = nil
	if !strings.Contains(proxy, "://") {
		return strings.TrimPrefix(u.String(), "http://")
	}
	return u.String()
}

func readIcon(name string) ([]byte, error) {
	path := provider.IconFile(name)
	if path == "" {
		return nil, fmt.Errorf("bad picture name %q", name)
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > provider.MaxIcon {
		return nil, fmt.Errorf("picture %s is over the size limit", name)
	}
	return os.ReadFile(path)
}

// writeIcons keeps the pictures a backup carries. A picture this machine has
// already is left alone; one that is not a picture, or is too large, is
// ignored.
func writeIcons(icons map[string][]byte) {
	names := make([]string, 0, len(icons))
	for n := range icons {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		path := provider.IconFile(n)
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			continue
		}
		data := icons[n]
		if len(data) > provider.MaxIcon {
			continue
		}
		// StoreIcon checks the content is a picture and names it by its
		// hash, which is how magpie names every picture it keeps.
		_, _ = provider.StoreIcon(data)
	}
}

type agentChange struct {
	agent, key, value string
}

// applyAgents sets each installed agent's fields to the values in the bundle.
// Agents that are not installed are skipped, as are single fields that fail
// (a model this machine cannot use yet, say); the rest still apply. Providers
// are set before models, and models before the other fields, as profiles do.
func applyAgents(set map[string]map[string]string) (int, []string) {
	var skipped []string
	installed := map[string]*agent.Agent{}
	for _, a := range agent.All() {
		installed[a.ID] = a
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var todo []agentChange
	for _, id := range ids {
		a := installed[id]
		if a == nil {
			skipped = append(skipped, id+": 未知的 Agent，已跳过")
			continue
		}
		if !a.Detected() {
			skipped = append(skipped, a.Name+": 本机未安装该 Agent，已跳过")
			continue
		}
		keys := make([]string, 0, len(set[id]))
		for k := range set[id] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if v := set[id][k]; v != "" {
				todo = append(todo, agentChange{id, k, v})
			}
		}
	}
	sort.SliceStable(todo, func(i, j int) bool {
		return agentRank(todo[i].agent+"."+todo[i].key) < agentRank(todo[j].agent+"."+todo[j].key)
	})
	changed := 0
	for _, c := range todo {
		f := installed[c.agent].Field(c.key)
		if f == nil {
			skipped = append(skipped, c.agent+"."+c.key+": 本版本不支持该设置，已跳过")
			continue
		}
		if f.Get() == c.value {
			continue
		}
		if err := f.Set(c.value); err != nil {
			skipped = append(skipped, c.agent+"."+c.key+": "+err.Error())
			continue
		}
		changed++
	}
	return changed, skipped
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
