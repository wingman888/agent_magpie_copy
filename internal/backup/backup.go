// Package backup moves magpie's configuration from one machine to another: the
// providers (keys included unless asked not to), their icons, the settings,
// the saved profiles, and every agent's current choices. The result is one
// file, sealed with a passphrase (see seal.go).
//
// Signed-in subscriptions are not part of a bundle: their sign-in is the
// agent's own, so each machine logs in itself.
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

// Ext is the extension a backup file is given.
const Ext = ".magpie-backup"

// Bundle is the configuration as it is written to a backup.
type Bundle struct {
	Version   int                        `json:"version"`
	App       string                     `json:"app,omitempty"` // the magpie that made it
	Providers []provider.Provider        `json:"providers,omitempty"`
	Icons     map[string][]byte          `json:"icons,omitempty"` // picture file name → bytes
	Settings  *settings.Settings         `json:"settings,omitempty"`
	Profiles  map[string]profile.Profile `json:"profiles,omitempty"`

	// Agents are the agents' current settings, as "agent.field" → value,
	// the same keys a profile uses.
	Agents profile.Profile `json:"agents,omitempty"`
}

// Parts selects which parts of a bundle Restore applies.
type Parts struct{ Providers, Settings, Profiles, Agents bool }

// All selects every part.
var All = Parts{true, true, true, true}

// Result says what Restore did.
type Result struct {
	Added    int      // providers newly added
	Replaced int      // providers replaced
	NeedKey  []string // names of providers left with no key
	Settings bool     // settings were restored
	Profiles int      // profiles written
	Agents   int      // agent settings changed
	Skipped  []string // agent settings left alone, with the reason
}

// credentialWords mark a custom header as carrying a secret.
var credentialWords = []string{"auth", "key", "token", "secret", "cookie", "session", "password"}

// Collect gathers this machine's configuration. With keys false the bundle
// holds no API key and no header that looks like a credential.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: CurrentVersion, App: app, Icons: map[string][]byte{}}

	for _, p := range provider.All() {
		if p.Account != nil {
			continue // a signed-in subscription: each machine signs in itself
		}
		if !keys {
			p = withoutKeys(p)
		}
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			if data, ok := readIcon(name); ok {
				b.Icons[name] = data
			} else {
				p.Icon = "" // the picture is not kept; do not point at a missing one
			}
		}
		b.Providers = append(b.Providers, p)
	}
	if len(b.Icons) == 0 {
		b.Icons = nil
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

	b.Agents = profile.Snapshot()
	if len(b.Agents) == 0 {
		b.Agents = nil
	}
	return b, nil
}

// readIcon reads a provider's picture from the icons folder, if it is there
// and within the size limit.
func readIcon(name string) ([]byte, bool) {
	path := provider.IconFile(name)
	if path == "" {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || len(data) > provider.MaxIcon {
		return nil, false
	}
	return data, true
}

// withoutKeys is p with every key and every credential-like header taken out.
func withoutKeys(p provider.Provider) provider.Provider {
	p.Key = ""
	p.KeyName = ""
	p.Keys = nil
	if len(p.Headers) > 0 {
		h := map[string]string{}
		for k, v := range p.Headers {
			if !isCredentialHeader(k) {
				h[k] = v
			}
		}
		p.Headers = h
		if len(h) == 0 {
			p.Headers = nil
		}
	}
	return p
}

func isCredentialHeader(name string) bool {
	n := strings.ToLower(name)
	for _, w := range credentialWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// Restore applies the selected parts of a bundle to this machine. Parts are
// applied in order: settings, providers (with their icons), profiles, then
// agent settings, so agent models can rely on the providers restored before.
func Restore(b Bundle, parts Parts) (Result, error) {
	var res Result
	if b.Version > CurrentVersion {
		return res, errNewer
	}

	if parts.Settings && b.Settings != nil {
		if err := settings.Save(*b.Settings); err != nil {
			return res, fmt.Errorf("settings in backup are invalid: %w", err)
		}
		res.Settings = true
	}

	if parts.Providers && len(b.Providers) > 0 {
		imp, err := provider.Import(b.Providers)
		if err != nil {
			return res, err
		}
		res.Added, res.Replaced = imp.Added, imp.Replaced
		for _, p := range imp.Stored {
			if p.Key == "" && len(p.Keys) == 0 {
				res.NeedKey = append(res.NeedKey, p.Name)
			}
		}
		if err := writeIcons(b.Icons, imp.Stored); err != nil {
			return res, err
		}
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
			p := b.Profiles[n]
			if p == nil {
				p = profile.Profile{}
			}
			if err := profile.Save(n, p); err != nil {
				return res, err
			}
			res.Profiles++
		}
	}

	if parts.Agents && len(b.Agents) > 0 {
		restoreAgents(b.Agents, &res)
	}
	return res, nil
}

// writeIcons writes the pictures the restored providers point at. A picture
// already on this machine under the same name is kept as it is, and one that
// is over the size limit or does not match its name is ignored.
func writeIcons(icons map[string][]byte, ps []provider.Provider) error {
	for _, p := range ps {
		name, ok := strings.CutPrefix(p.Icon, "file:")
		if !ok {
			continue
		}
		data, ok := icons[name]
		if !ok || len(data) == 0 || len(data) > provider.MaxIcon {
			continue
		}
		sum := sha256.Sum256(data)
		if !strings.HasPrefix(name, hex.EncodeToString(sum[:8])) {
			continue
		}
		path := provider.IconFile(name)
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.MkdirAll(provider.IconDir(), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// restoreAgents sets each agent field in the bundle that this machine can
// take. Agents not installed here are skipped as a whole; a field that fails
// (a model behind a subscription not signed in here, say) is skipped alone.
func restoreAgents(vals profile.Profile, res *Result) {
	agents := map[string]*agent.Agent{}
	for _, a := range agent.All() {
		agents[a.ID] = a
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	// providers first, then models, then the rest: as profile.Apply does
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

	missing := map[string]bool{}
	for _, k := range keys {
		id, field, ok := strings.Cut(k, ".")
		if !ok || vals[k] == "" {
			continue
		}
		a := agents[id]
		if a == nil {
			continue
		}
		if !a.Detected() {
			if !missing[id] {
				missing[id] = true
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s: 本机未安装该 Agent，已跳过", id))
			}
			continue
		}
		f := a.Field(field)
		if f == nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: 本机没有此设置项，已跳过", k))
			continue
		}
		if f.Get() == vals[k] {
			continue
		}
		if err := f.Set(vals[k]); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: 暂时无法设置（%v），已跳过", k, err))
			continue
		}
		res.Agents++
	}
}
