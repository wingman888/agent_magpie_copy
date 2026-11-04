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

// Parts selects which parts of a bundle Restore applies.
type Parts struct {
	Providers, Settings, Profiles, Agents bool
}

// All selects every part.
var All = Parts{Providers: true, Settings: true, Profiles: true, Agents: true}

// Result says what Restore did.
type Result struct {
	Added, Replaced int      // providers written as new / over an existing one
	NeedKey         []string // names of providers left with no key at all
	Settings        bool     // settings were restored
	Profiles        int      // profiles written
	Agents          int      // agent settings changed
	Skipped         []string // items not applied, with the reason
}

// credentialWords mark a custom header whose value is a secret.
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

// Collect gathers this machine's configuration. With keys false nothing secret
// is included: no API key, and no custom header that looks like a credential.
// Signed-in agent accounts are never included; they belong to each machine.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{
		Version: formatVersion,
		App:     app,
		Icons:   map[string][]byte{},
	}

	for _, p := range provider.All() {
		if p.Account != nil {
			continue
		}
		if !keys {
			p = withoutKeys(p)
		}
		if name, ok := provider.IconFileName(p.Icon); ok {
			data, err := os.ReadFile(provider.IconFile(name))
			if err == nil && len(data) > 0 && len(data) <= provider.MaxIcon {
				b.Icons[name] = data
			} else {
				p.Icon = "" // the picture is gone; do not carry a dangling reference
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
		return Bundle{}, fmt.Errorf("读取方案失败: %w", err)
	}
	b.Profiles = ps
	b.Agents = profile.Snapshot()
	return b, nil
}

// withoutKeys drops every key of a provider, with the key metadata that goes
// with them, and the custom headers that carry credentials.
func withoutKeys(p provider.Provider) provider.Provider {
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
	var h map[string]string
	for k, v := range p.Headers {
		if credentialHeader(k) {
			continue
		}
		if h == nil {
			h = map[string]string{}
		}
		h[k] = v
	}
	p.Headers = h
	return p
}

// Restore applies the selected parts of b to this machine. Nothing that the
// bundle does not carry is removed. Providers go first, so agent settings that
// refer to them take effect.
func Restore(b Bundle, parts Parts) (Result, error) {
	var r Result
	if b.Version > formatVersion {
		return r, errNewer
	}
	if parts.Providers {
		restoreProviders(b, &r)
	}
	if parts.Settings && b.Settings != nil {
		if err := settings.Save(*b.Settings); err != nil {
			r.Skipped = append(r.Skipped, "设置："+err.Error())
		} else {
			r.Settings = true
		}
	}
	if parts.Profiles {
		for _, name := range profile.Names(b.Profiles) {
			if err := profile.Save(name, b.Profiles[name]); err != nil {
				r.Skipped = append(r.Skipped, fmt.Sprintf("方案 %s：%v", name, err))
				continue
			}
			r.Profiles++
		}
	}
	if parts.Agents {
		restoreAgents(b.Agents, &r)
	}
	return r, nil
}

func restoreProviders(b Bundle, r *Result) {
	for _, p := range b.Providers {
		id := strings.ToLower(strings.TrimSpace(p.ID))
		if !provider.ValidID(id) {
			continue // unusable id, or the gateway's own name: ignored
		}
		if name, ok := provider.IconFileName(p.Icon); ok {
			writeIcon(name, b.Icons[name])
		}
		stored, replaced, err := provider.Import(p)
		if err != nil {
			r.Skipped = append(r.Skipped, fmt.Sprintf("提供商 %s：%v", id, err))
			continue
		}
		if replaced {
			r.Replaced++
		} else {
			r.Added++
		}
		if len(stored.KeyList()) == 0 {
			r.NeedKey = append(r.NeedKey, stored.Name)
		}
	}
}

// writeIcon keeps a bundled picture under its file name, unless this machine
// already has a file of that name. Pictures that are missing, empty, too large
// or badly named are left out.
func writeIcon(name string, data []byte) {
	path := provider.IconFile(name)
	if path == "" || len(data) == 0 || len(data) > provider.MaxIcon {
		return
	}
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.MkdirAll(provider.IconDir(), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// restoreAgents sets each agent field the bundle names, for the agents
// installed here. Each item stands alone: one that fails is skipped and the
// rest still apply.
func restoreAgents(want profile.Profile, r *Result) {
	if len(want) == 0 {
		return
	}
	known := map[string]bool{}
	for _, a := range agent.All() {
		known[a.ID] = true
	}
	installed := map[string]*agent.Agent{}
	for _, a := range agent.Detected() {
		installed[a.ID] = a
	}

	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	// providers first, then models, as profile.Apply does
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

	for _, k := range keys {
		id, field, ok := strings.Cut(k, ".")
		if !ok || !known[id] {
			continue
		}
		a := installed[id]
		if a == nil {
			r.Skipped = append(r.Skipped, fmt.Sprintf("%s：本机未安装该 Agent", k))
			continue
		}
		f := a.Field(field)
		if f == nil || f.Get() == want[k] {
			continue
		}
		if err := f.Set(want[k]); err != nil {
			r.Skipped = append(r.Skipped, fmt.Sprintf("%s：其模型暂时不可用（%v）", k, err))
			continue
		}
		r.Agents++
	}
}
