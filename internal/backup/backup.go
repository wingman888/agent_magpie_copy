// Package backup moves magpie's configuration from one machine to another:
// providers (with their keys), their icons, the settings, the saved profiles
// and each agent's current model choices. Subscriptions are never included:
// they are the agents' own sign-ins and each machine signs in itself.
//
// A backup is a Bundle sealed with a passphrase (see crypt.go).
package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Ext is the extension a backup file gets.
const Ext = ".magpie-backup"

// bundleVersion is the version of the content a Bundle carries.
const bundleVersion = 1

// Bundle is everything a backup holds, in clear, before it is sealed.
type Bundle struct {
	Version   int                          `json:"version"`
	App       string                       `json:"app,omitempty"`
	Providers []provider.Provider          `json:"providers,omitempty"`
	Icons     map[string][]byte            `json:"icons,omitempty"`    // picture file name -> bytes
	Settings  *settings.Settings           `json:"settings,omitempty"` // nil: none
	Profiles  map[string]profile.Profile   `json:"profiles,omitempty"`
	Agents    map[string]map[string]string `json:"agents,omitempty"` // agent id -> field -> value
}

// Parts selects what Restore applies.
type Parts struct {
	Providers, Settings, Profiles, Agents bool
}

// All selects every part.
var All = Parts{true, true, true, true}

// Result says what Restore did.
type Result struct {
	Added, Replaced int      // providers written, new or replacing one with the same id
	NeedKey         []string // names of providers left with no key at all
	Settings        bool     // settings were written
	Profiles        int      // profiles written
	Agents          int      // agent fields changed
	Skipped         []string // items not applied, with the reason
}

// credentialWords mark a custom header whose value is a secret.
var credentialWords = []string{"auth", "key", "token", "secret", "cookie", "session", "password"}

func isCredentialHeader(name string) bool {
	n := strings.ToLower(name)
	for _, w := range credentialWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// Collect gathers this machine's configuration. With keys false it carries
// no API key at all, and drops custom headers that look like credentials.
// app identifies the magpie that made the bundle.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: bundleVersion, App: app}

	for _, p := range provider.All() {
		if p.Account != nil {
			continue // a signed-in subscription: never leaves this machine
		}
		if !keys {
			p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
			if len(p.Headers) > 0 {
				kept := map[string]string{}
				for k, v := range p.Headers {
					if !isCredentialHeader(k) {
						kept[k] = v
					}
				}
				p.Headers = nil
				if len(kept) > 0 {
					p.Headers = kept
				}
			}
		}
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			if path := provider.IconFile(name); path != "" {
				if data, err := os.ReadFile(path); err == nil && len(data) <= provider.MaxIcon {
					if b.Icons == nil {
						b.Icons = map[string][]byte{}
					}
					b.Icons[name] = data
				}
			}
		}
		b.Providers = append(b.Providers, p)
	}

	if _, err := os.Stat(settings.Path()); err == nil {
		s := settings.Load()
		if !keys {
			s.Proxy = stripProxyUserinfo(s.Proxy)
		}
		b.Settings = &s
	}

	profs, err := profile.Load()
	if err != nil {
		return Bundle{}, fmt.Errorf("could not read the saved profiles: %w", err)
	}
	if len(profs) > 0 {
		b.Profiles = profs
	}

	for _, a := range agent.Detected() {
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

// stripProxyUserinfo removes "user:pass@" from a proxy address, keeping the
// scheme and host:port. "" and "direct" are returned unchanged.
func stripProxyUserinfo(proxy string) string {
	if proxy == "" || proxy == "direct" {
		return proxy
	}
	scheme, rest := "", proxy
	if i := strings.Index(proxy, "://"); i >= 0 {
		scheme, rest = proxy[:i+3], proxy[i+3:]
	}
	authority := rest
	if i := strings.Index(rest, "/"); i >= 0 {
		authority = rest[:i]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return proxy
	}
	return scheme + rest[at+1:]
}

var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validProvider reports whether a backed-up provider can be restored: its id
// is well formed and is not the reserved "magpie".
func validProvider(p provider.Provider) bool {
	return p.ID != "" && p.ID != "magpie" && idPattern.MatchString(p.ID)
}

// Restore applies the selected parts of b to this machine. Providers go
// first, so the agent models that depend on them can take effect.
func Restore(b Bundle, parts Parts) (Result, error) {
	var res Result

	if parts.Providers {
		var incoming []provider.Provider
		for _, p := range b.Providers {
			if validProvider(p) {
				incoming = append(incoming, p)
			}
		}
		if len(incoming) > 0 {
			writeIcons(b.Icons, incoming)
			merged, added, replaced, err := provider.Merge(incoming)
			if err != nil {
				return res, fmt.Errorf("could not write the providers: %w", err)
			}
			res.Added, res.Replaced = added, replaced
			for _, p := range merged {
				if p.Key == "" && len(p.Keys) == 0 {
					name := p.Name
					if name == "" {
						name = p.ID
					}
					res.NeedKey = append(res.NeedKey, name)
				}
			}
		}
	}

	if parts.Settings && b.Settings != nil {
		if err := settings.Save(*b.Settings); err != nil {
			res.Skipped = append(res.Skipped, "设置：未能写入（"+err.Error()+"）")
		} else {
			res.Settings = true
		}
	}

	if parts.Profiles && len(b.Profiles) > 0 {
		for _, name := range sortedKeys(b.Profiles) {
			if err := profile.Save(name, b.Profiles[name]); err != nil {
				res.Skipped = append(res.Skipped, "方案 "+name+"：未能写入（"+err.Error()+"）")
				continue
			}
			res.Profiles++
		}
	}

	if parts.Agents && len(b.Agents) > 0 {
		installed := map[string]*agent.Agent{}
		for _, a := range agent.Detected() {
			installed[a.ID] = a
		}
		for _, id := range sortedKeys(b.Agents) {
			a := installed[id]
			if a == nil {
				res.Skipped = append(res.Skipped, id+"：本机未安装，已跳过其模型设置")
				continue
			}
			vals := b.Agents[id]
			for _, key := range sortedKeys(vals) {
				want := vals[key]
				f := a.Field(key)
				if f == nil {
					res.Skipped = append(res.Skipped, id+"."+key+"：本机版本不支持此项")
					continue
				}
				if f.Get() == want {
					continue
				}
				if err := f.Set(want); err != nil {
					res.Skipped = append(res.Skipped, id+"."+key+"：未能设置（"+err.Error()+"）")
					continue
				}
				res.Agents++
			}
		}
	}

	return res, nil
}

// writeIcons stores the pictures the incoming providers point at, when this
// machine has no picture of that name yet. Existing pictures are kept.
func writeIcons(icons map[string][]byte, incoming []provider.Provider) {
	if len(icons) == 0 {
		return
	}
	for _, p := range incoming {
		name, ok := strings.CutPrefix(p.Icon, "file:")
		if !ok {
			continue
		}
		data, ok := icons[name]
		if !ok || len(data) == 0 || len(data) > provider.MaxIcon {
			continue
		}
		path := provider.IconFile(name)
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			continue
		}
		_ = os.WriteFile(path, data, 0o644)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ErrNotBackup is returned for data that is not a magpie backup at all.
var ErrNotBackup = errors.New("不是 magpie 备份文件")

// errNewer is returned for a backup made by a newer magpie.
var errNewer = errors.New("这个备份由更新版本的 magpie 生成，请先升级 magpie 再恢复")
