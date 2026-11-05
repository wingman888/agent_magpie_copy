// Package backup carries magpie's setup from one machine to another: the
// providers and their keys, the picked icons, the settings, the saved
// profiles and each agent's model choice, sealed in one file under a
// passphrase (see seal.go).
//
// Signed-in subscriptions do not travel. Their login lives in the agent's
// own files, not in magpie's, so nothing here reads or writes it.
package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// Ext is the extension a backup file gets.
const Ext = ".magpie-backup"

// FormatVersion is the newest backup format this build writes and reads.
const FormatVersion = 1

// iconRef is the prefix of a provider's Icon when it names a stored picture.
const iconRef = "file:"

var (
	// ErrPassphrase means the file is a backup but does not open: the
	// password is wrong, or the file was changed.
	ErrPassphrase = errors.New("wrong password, or the backup was changed")
	// ErrNotBackup means the file is not a magpie backup at all.
	ErrNotBackup = errors.New("不是 magpie 备份文件")
	// ErrNewer means the file was written by a newer magpie.
	ErrNewer = errors.New("这个备份由更新版本的 magpie 生成，请先升级 magpie")
)

// Bundle is everything a backup holds, before sealing.
type Bundle struct {
	Version   int                        `json:"version"`
	App       string                     `json:"app,omitempty"`
	Providers []provider.Provider        `json:"providers,omitempty"`
	Icons     map[string][]byte          `json:"icons,omitempty"` // picture bytes by file name
	Settings  *settings.Settings         `json:"settings,omitempty"`
	Profiles  map[string]profile.Profile `json:"profiles,omitempty"`
	Agents    map[string]string          `json:"agents,omitempty"` // "agent.field" → value
}

// Parts picks which sections Restore applies.
type Parts struct {
	Providers, Settings, Profiles, Agents bool
}

// All restores every section.
var All = Parts{Providers: true, Settings: true, Profiles: true, Agents: true}

// Result says what Restore did.
type Result struct {
	Added, Replaced int      // providers written as new, or over an existing id
	NeedKey         []string // names of restored providers with no key at all
	Settings        bool     // whether the settings were restored
	Profiles        int      // profiles written
	Agents          int      // agent settings changed
	Skipped         []string // what was left alone, and why
}

// credentialWords mark a custom header as carrying a secret.
var credentialWords = []string{"auth", "key", "token", "secret", "cookie", "session", "password"}

// Collect gathers this machine's setup. With keys false it holds no API
// key and no header that looks like a credential. app names the magpie
// that made it.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: FormatVersion, App: app}
	icons := map[string][]byte{}
	for _, p := range provider.Stored() {
		if !keys {
			p = withoutKeys(p)
		}
		if name, ok := strings.CutPrefix(p.Icon, iconRef); ok {
			if data, ok := readIcon(name); ok {
				icons[name] = data
			} else {
				p.Icon = "" // a picture that cannot be read or is too large stays behind
			}
		}
		b.Providers = append(b.Providers, p)
	}
	if len(icons) > 0 {
		b.Icons = icons
	}

	if _, err := os.Stat(settings.Path()); err == nil {
		s := settings.Load()
		if !keys {
			s.Proxy = withoutProxyUserinfo(s.Proxy)
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
		b.Agents = map[string]string(snap)
	}
	return b, nil
}

// withoutKeys is p with every key, and every credential-looking header, taken out.
func withoutKeys(p provider.Provider) provider.Provider {
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
	var h map[string]string
	for k, v := range p.Headers {
		if isCredentialHeader(k) {
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

// withoutProxyUserinfo drops the user name and password from a proxy
// address, keeping scheme://host:port. An address that cannot be read is
// dropped whole rather than risk keeping a password.
func withoutProxyUserinfo(proxy string) string {
	if proxy == "" || proxy == "direct" {
		return proxy
	}
	raw, bare := proxy, !strings.Contains(proxy, "://")
	if bare {
		raw = "http://" + proxy
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.User == nil {
		return proxy
	}
	u.User = nil
	out := u.String()
	if bare {
		out = strings.TrimPrefix(out, "http://")
	}
	return out
}

func isCredentialHeader(name string) bool {
	lower := strings.ToLower(name)
	for _, w := range credentialWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// readIcon reads a stored picture for a backup; false if it is missing or over the size limit.
func readIcon(name string) ([]byte, bool) {
	path := provider.IconFile(name)
	if path == "" {
		return nil, false
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() > provider.MaxIcon {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}

// Restore applies the chosen parts of b to this machine. The order is
// settings, providers, profiles, then agents, since an agent's model
// depends on the providers being there.
func Restore(b Bundle, parts Parts) (Result, error) {
	var res Result
	if b.Version > FormatVersion {
		return res, ErrNewer
	}
	if parts.Settings && b.Settings != nil {
		if err := settings.Save(*b.Settings); err != nil {
			return res, err
		}
		res.Settings = true
	}
	if parts.Providers {
		if err := restoreProviders(b, &res); err != nil {
			return res, err
		}
	}
	if parts.Profiles {
		n, err := restoreProfiles(b.Profiles)
		res.Profiles = n
		if err != nil {
			return res, err
		}
	}
	if parts.Agents {
		restoreAgents(b.Agents, &res)
	}
	return res, nil
}

// restoreProviders merges the bundle's providers into providers.json by id.
// An incoming provider with no key keeps the key this machine already has.
func restoreProviders(b Bundle, res *Result) error {
	local := provider.Stored()
	index := map[string]int{}
	for i, p := range local {
		index[p.ID] = i
	}
	var ids []string
	seen := map[string]bool{}
	for _, raw := range b.Providers {
		p, ok := incoming(raw)
		if !ok {
			continue
		}
		hasKey := p.Key != "" || len(p.Keys) > 0
		if i, exists := index[p.ID]; exists {
			if !hasKey {
				old := local[i]
				p.Key, p.KeyName, p.KeyProtocol, p.Keys = old.Key, old.KeyName, old.KeyProtocol, old.Keys
				p.Headers = mergeHeaders(p.Headers, old.Headers)
			}
			local[i] = p
			res.Replaced++
		} else {
			index[p.ID] = len(local)
			local = append(local, p)
			res.Added++
		}
		i := index[p.ID]
		if err := restoreIcon(&local[i], b.Icons); err != nil {
			return err
		}
		if !seen[p.ID] {
			seen[p.ID] = true
			ids = append(ids, p.ID)
		}
	}
	if res.Added+res.Replaced == 0 {
		return nil
	}
	for _, id := range ids {
		p := local[index[id]]
		if p.Key != "" || len(p.Keys) > 0 {
			continue
		}
		res.NeedKey = append(res.NeedKey, p.Name)
	}
	return provider.WriteStored(local)
}

// mergeHeaders is the header set for a provider restored without a key:
// the bundle's ordinary headers, plus this machine's credential headers.
// On a name both carry, the credential header is this machine's.
func mergeHeaders(incoming, local map[string]string) map[string]string {
	var out map[string]string
	put := func(k, v string) {
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	for k, v := range incoming {
		if !isCredentialHeader(k) {
			put(k, v)
		}
	}
	for k, v := range local {
		if isCredentialHeader(k) {
			put(k, v)
		}
	}
	return out
}

// incoming cleans a provider from a bundle, and reports false when it
// cannot be stored: an invalid id, or the reserved "magpie".
func incoming(in provider.Provider) (provider.Provider, bool) {
	id := strings.ToLower(strings.TrimSpace(in.ID))
	if id == "" || id == "magpie" || id != provider.Slug(id) {
		return in, false
	}
	in.ID = id
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		in.Name = id
	}
	in.Key = strings.TrimSpace(in.Key)
	in.IconURL = ""
	in.Account = nil
	return in, true
}

// restoreIcon makes sure the picture p points at is on disk. An existing
// file is kept as it is; a missing one is written from the bundle when the
// bytes are present, the right size, and match the name. Otherwise the
// icon is dropped rather than left dangling.
func restoreIcon(p *provider.Provider, icons map[string][]byte) error {
	name, ok := strings.CutPrefix(p.Icon, iconRef)
	if !ok {
		return nil
	}
	path := provider.IconFile(name)
	if path == "" {
		p.Icon = ""
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	data, ok := icons[name]
	if !ok || len(data) == 0 || len(data) > provider.MaxIcon || !nameMatches(name, data) {
		p.Icon = ""
		return nil
	}
	if err := os.MkdirAll(provider.IconDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// nameMatches is true when a picture's name is the fingerprint its bytes give (see provider.StoreIcon).
func nameMatches(name string, data []byte) bool {
	sum := sha256.Sum256(data)
	return strings.HasPrefix(name, hex.EncodeToString(sum[:8])+".")
}

func restoreProfiles(ps map[string]profile.Profile) (int, error) {
	names := make([]string, 0, len(ps))
	for n := range ps {
		names = append(names, n)
	}
	sort.Strings(names)
	n := 0
	for _, orig := range names {
		name := strings.TrimSpace(orig)
		if name == "" {
			continue
		}
		p := ps[orig]
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

// restoreAgents sets each agent's fields for the agents installed here.
// Providers are settled first, then models, as profile.Apply does. A
// missing agent is skipped as a whole; a field that fails is skipped alone.
func restoreAgents(vals map[string]string, res *Result) {
	if len(vals) == 0 {
		return
	}
	byID := map[string]*agent.Agent{}
	for _, a := range agent.All() {
		byID[a.ID] = a
	}
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

	installed := map[string]bool{}
	reported := map[string]bool{}
	for _, k := range keys {
		id, field, ok := strings.Cut(k, ".")
		if !ok || vals[k] == "" {
			continue
		}
		a := byID[id]
		if a == nil {
			res.Skipped = append(res.Skipped, k+"（不认识的 Agent）")
			continue
		}
		if _, seen := installed[id]; !seen {
			installed[id] = a.Detected()
		}
		if !installed[id] {
			if !reported[id] {
				reported[id] = true
				res.Skipped = append(res.Skipped, a.Name+"（本机未安装）")
			}
			continue
		}
		f := a.Field(field)
		if f == nil {
			res.Skipped = append(res.Skipped, k+"（本版本没有这项设置）")
			continue
		}
		if f.Get() == vals[k] {
			continue
		}
		if err := f.Set(vals[k]); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s（模型暂时不可用：%v）", k, err))
			continue
		}
		res.Agents++
	}
}

// agentRank puts providers before models before other fields, as profile.Apply does.
func agentRank(k string) int {
	switch {
	case strings.HasSuffix(k, ".provider"):
		return 0
	case strings.HasSuffix(k, ".model"):
		return 1
	}
	return 2
}
