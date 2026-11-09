// Package backup gathers magpie's configuration into one bundle, seals it with
// a passphrase, and restores a bundle onto another machine. A subscription
// sign-in never goes into a bundle: each machine signs in on its own.
package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Ext is the extension of a backup file.
const Ext = ".magpie-backup"

// iconPrefix marks a provider icon that is a picture in the icons folder.
const iconPrefix = "file:"

// Bundle is everything a backup carries. Parts left empty are skipped on
// restore.
type Bundle struct {
	Version   int                        `json:"version"`
	App       string                     `json:"app,omitempty"`
	Providers []provider.Provider        `json:"providers,omitempty"`
	Icons     map[string][]byte          `json:"icons,omitempty"` // icon file name → picture
	Settings  *settings.Settings         `json:"settings,omitempty"`
	Profiles  map[string]profile.Profile `json:"profiles,omitempty"`
	// Agents are the agents' current settings, "agent.field" → value, as
	// profile.Snapshot writes them.
	Agents profile.Profile `json:"agents,omitempty"`
}

// Parts chooses which parts of a bundle Restore applies.
type Parts struct {
	Providers, Settings, Profiles, Agents bool
}

// All selects every part.
var All = Parts{Providers: true, Settings: true, Profiles: true, Agents: true}

// Result reports what Restore did.
type Result struct {
	Added, Replaced int
	NeedKey         []string // names of providers left without any key
	Settings        bool
	Profiles        int
	Agents          int
	Skipped         []string
}

// secretWords mark a custom header as a credential when they appear in its
// name, in any case.
var secretWords = []string{"auth", "key", "token", "secret", "cookie", "session", "password"}

// Collect gathers this machine's configuration. With keys false the bundle
// holds no API key and no header that looks like a credential. app names the
// magpie that made it.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: formatVersion, App: app}
	for _, p := range provider.All() {
		if p.Account != nil {
			continue // a signed-in subscription: stays on its own machine
		}
		if !keys {
			p = withoutKeys(p)
		}
		b.Providers = append(b.Providers, p)
		if name, ok := iconName(p.Icon); ok {
			if path := provider.IconFile(name); path != "" {
				if data, err := os.ReadFile(path); err == nil {
					if b.Icons == nil {
						b.Icons = map[string][]byte{}
					}
					b.Icons[name] = data
				}
			}
		}
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
	if snap := profile.Snapshot(); len(snap) > 0 {
		b.Agents = snap
	}
	return b, nil
}

// withoutKeys is p with every key taken out, and the credential-looking
// headers too.
func withoutKeys(p provider.Provider) provider.Provider {
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
	p.Headers = keylessHeaders(p.Headers)
	return p
}

func keylessHeaders(h map[string]string) map[string]string {
	var out map[string]string
	for k, v := range h {
		low := strings.ToLower(k)
		if slices.ContainsFunc(secretWords, func(w string) bool { return strings.Contains(low, w) }) {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

// iconName is the picture file a provider's icon value names, if any.
func iconName(icon string) (string, bool) {
	return strings.CutPrefix(icon, iconPrefix)
}

// Restore applies the chosen parts of b to this machine. Settings are checked
// first, so a bad one changes nothing; providers come before the agents'
// models, which depend on them.
func Restore(b Bundle, parts Parts) (Result, error) {
	var res Result
	if parts.Settings && b.Settings != nil {
		if err := settings.Save(*b.Settings); err != nil {
			return res, err
		}
		res.Settings = true
	}
	if parts.Providers && len(b.Providers) > 0 {
		if err := restoreProviders(b, &res); err != nil {
			return res, err
		}
	}
	if parts.Profiles && len(b.Profiles) > 0 {
		n, err := restoreProfiles(b.Profiles)
		if err != nil {
			return res, err
		}
		res.Profiles = n
	}
	if parts.Agents && len(b.Agents) > 0 {
		restoreAgents(b.Agents, &res)
	}
	return res, nil
}

// storedFile is the providers file as it is on disk.
type storedFile struct {
	Providers []provider.Provider `json:"providers"`
}

func readStored() ([]provider.Provider, error) {
	raw, err := edit.Read(provider.Path())
	if err != nil {
		return nil, err
	}
	var f storedFile
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", provider.Path(), err)
		}
	}
	return f.Providers, nil
}

func writeStored(ps []provider.Provider) error {
	b, err := json.MarshalIndent(storedFile{Providers: ps}, "", "  ")
	if err != nil {
		return err
	}
	if err := edit.WriteAtomic(provider.Path(), append(b, '\n')); err != nil {
		return err
	}
	return os.Chmod(provider.Path(), 0o600)
}

func restoreProviders(b Bundle, res *Result) error {
	stored, err := readStored()
	if err != nil {
		return err
	}
	accounts := map[string]bool{}
	for _, a := range provider.Accounts() {
		accounts[a.ID] = true
	}
	changed := false
	for _, in := range b.Providers {
		p, ok := cleanIncoming(in)
		if !ok || accounts[p.ID] {
			continue
		}
		idx := -1
		for i := range stored {
			if stored[i].ID == p.ID {
				idx = i
				break
			}
		}
		if !hasKeys(p) && idx >= 0 && hasKeys(stored[idx]) {
			// the backup has no keys: this machine keeps its own
			old := stored[idx]
			p.Key, p.KeyName, p.KeyProtocol, p.Keys = old.Key, old.KeyName, old.KeyProtocol, old.Keys
		}
		p.Icon = adoptIcon(p.Icon, b.Icons)
		if idx >= 0 {
			stored[idx] = p
			res.Replaced++
		} else {
			stored = append(stored, p)
			res.Added++
		}
		if !hasKeys(p) {
			res.NeedKey = append(res.NeedKey, p.Name)
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return writeStored(stored)
}

// cleanIncoming makes a bundled provider fit to store. ok is false for an
// id that is invalid or the reserved "magpie".
func cleanIncoming(p provider.Provider) (provider.Provider, bool) {
	p.ID = strings.ToLower(strings.TrimSpace(p.ID))
	if p.ID == "" || p.ID != provider.Slug(p.ID) || p.ID == "magpie" {
		return p, false
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		p.Name = p.ID
	}
	p.IconURL = ""
	p.Account = nil
	return p, true
}

func hasKeys(p provider.Provider) bool {
	return p.Key != "" || len(p.Keys) > 0
}

// adoptIcon makes sure the picture an icon names is on this machine: it is
// written from the bundle when missing and never overwritten. It returns the
// icon value to keep, or "" when no picture is available.
func adoptIcon(icon string, icons map[string][]byte) string {
	name, ok := iconName(icon)
	if !ok {
		return icon
	}
	path := provider.IconFile(name)
	if path == "" {
		return ""
	}
	if _, err := os.Stat(path); err == nil {
		return icon
	}
	data := icons[name]
	if len(data) == 0 || len(data) > provider.MaxIcon {
		return ""
	}
	// pictures are named after their content (see provider.StoreIcon)
	sum := sha256.Sum256(data)
	if len(name) < 16 || name[:16] != hex.EncodeToString(sum[:8]) {
		return ""
	}
	if err := os.MkdirAll(provider.IconDir(), 0o755); err != nil {
		return ""
	}
	if err := os.WriteFile(filepath.Join(provider.IconDir(), name), data, 0o644); err != nil {
		return ""
	}
	return icon
}

// restoreProfiles writes the bundled profiles over the same names and keeps
// the rest. It returns how many were written.
func restoreProfiles(ps map[string]profile.Profile) (int, error) {
	cur, err := profile.Load()
	if err != nil {
		return 0, err
	}
	n := 0
	for name, p := range ps {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if p == nil {
			p = profile.Profile{}
		}
		cur[name] = p
		n++
	}
	if n == 0 {
		return 0, nil
	}
	b, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := edit.WriteAtomic(profile.Path(), append(b, '\n')); err != nil {
		return 0, err
	}
	return n, nil
}

// agentRank orders the settings so providers go first and models next, as
// profile.Apply does.
func agentRank(k string) int {
	switch {
	case strings.HasSuffix(k, ".provider"):
		return 0
	case strings.HasSuffix(k, ".model"):
		return 1
	}
	return 2
}

// restoreAgents applies each agent setting on its own: one that cannot be set
// is listed as skipped and the rest still go through.
func restoreAgents(vals profile.Profile, res *Result) {
	byID := map[string]*agent.Agent{}
	for _, a := range agent.All() {
		byID[a.ID] = a
	}
	installed := map[string]bool{}
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
		id, field, ok := strings.Cut(k, ".")
		if !ok {
			res.Skipped = append(res.Skipped, k+"：配置项格式不对")
			continue
		}
		a := byID[id]
		if a == nil {
			res.Skipped = append(res.Skipped, k+"：本机未安装该 Agent")
			continue
		}
		if _, seen := installed[id]; !seen {
			installed[id] = a.Detected()
		}
		if !installed[id] {
			res.Skipped = append(res.Skipped, k+"：本机未安装该 Agent")
			continue
		}
		f := a.Field(field)
		if f == nil {
			res.Skipped = append(res.Skipped, k+"：没有这个配置项")
			continue
		}
		if f.Get() == vals[k] {
			continue
		}
		if err := f.Set(vals[k]); err != nil {
			res.Skipped = append(res.Skipped, k+"：模型暂时不可用")
			continue
		}
		res.Agents++
	}
}
