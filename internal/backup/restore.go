package backup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// maxIcon is the largest picture a backup restores. Larger ones are ignored.
const maxIcon = 512 << 10

// credentialWords mark a custom request header as a credential. Matching is
// case-insensitive and by substring, so "X-Api-Key" and "Cookie" both match.
var credentialWords = []string{"auth", "key", "token", "secret", "cookie", "session", "password"}

// Parts selects which sections Restore touches.
type Parts struct{ Providers, Settings, Profiles, Agents bool }

// All selects every section.
var All = Parts{Providers: true, Settings: true, Profiles: true, Agents: true}

// Result reports what Restore did.
type Result struct {
	Added, Replaced int      // providers written as new / over an existing id
	Settings        bool     // settings were restored
	Profiles        int      // profiles written
	Agents          int      // agent settings changed
	NeedKey         []string // names of providers left without any key
	Skipped         []string // agent settings not applied, with the reason
}

// Collect gathers this machine's configuration into a Bundle. With keys false
// it carries no API key and no credential-looking header. Subscription logins
// (signed-in agents) are never included. app is recorded as the writer's version.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: version, App: app}

	b.Providers = []provider.Provider{}
	b.Icons = map[string][]byte{}
	for _, p := range provider.All() {
		if p.Account != nil {
			continue // a subscription: lives in the agent, not in the backup
		}
		if !keys {
			p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
			p.Headers = stripCredentialHeaders(p.Headers)
		}
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			if err := collectIcon(b.Icons, name); err != nil {
				return b, err
			}
		}
		b.Providers = append(b.Providers, p)
	}

	if _, err := os.Stat(settings.Path()); err == nil {
		raw, err := json.Marshal(settings.Load())
		if err != nil {
			return b, fmt.Errorf("encode settings: %w", err)
		}
		b.Settings = raw
	}

	ps, err := profile.Load()
	if err != nil {
		return b, fmt.Errorf("read profiles: %w", err)
	}
	if len(ps) > 0 {
		raw, err := json.Marshal(ps)
		if err != nil {
			return b, fmt.Errorf("encode profiles: %w", err)
		}
		b.Profiles = raw
	}

	agents := map[string]string{}
	for _, a := range agent.Detected() {
		for _, f := range a.Fields {
			if v := f.Get(); v != "" {
				agents[a.ID+"."+f.Key] = v
			}
		}
	}
	if len(agents) > 0 {
		raw, err := json.Marshal(agents)
		if err != nil {
			return b, fmt.Errorf("encode agents: %w", err)
		}
		b.Agents = raw
	}
	return b, nil
}

// collectIcon reads a stored picture into icons. A picture that no longer
// exists is skipped rather than failing the whole backup.
func collectIcon(icons map[string][]byte, name string) error {
	path := provider.IconFile(name)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read icon: %w", err)
	}
	if len(data) <= maxIcon {
		icons[name] = data
	}
	return nil
}

func stripCredentialHeaders(h map[string]string) map[string]string {
	if h == nil {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if !isCredentialHeader(k) {
			out[k] = v
		}
	}
	return out
}

func isCredentialHeader(name string) bool {
	l := strings.ToLower(name)
	for _, w := range credentialWords {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// Restore writes the selected parts of b into this machine's configuration.
// Providers go first so the agent models that depend on them can take effect.
// Parts that are not selected, or absent from b, are left alone.
func Restore(b Bundle, parts Parts) (Result, error) {
	var res Result
	if b.Version > version {
		return res, ErrNewer
	}

	// Decode everything up front so a damaged section fails before any write.
	var st *settings.Settings
	if parts.Settings && hasSection(b.Settings) {
		var s settings.Settings
		if err := json.Unmarshal(b.Settings, &s); err != nil {
			return res, fmt.Errorf("backup settings are damaged: %w", err)
		}
		st = &s
	}
	var profs map[string]profile.Profile
	if parts.Profiles && hasSection(b.Profiles) {
		if err := json.Unmarshal(b.Profiles, &profs); err != nil {
			return res, fmt.Errorf("backup profiles are damaged: %w", err)
		}
	}
	var agentVals map[string]string
	if parts.Agents && hasSection(b.Agents) {
		if err := json.Unmarshal(b.Agents, &agentVals); err != nil {
			return res, fmt.Errorf("backup agent settings are damaged: %w", err)
		}
	}

	if parts.Providers && len(b.Providers) > 0 {
		if err := restoreProviders(b, &res); err != nil {
			return res, err
		}
	}
	if st != nil {
		if err := settings.Save(*st); err != nil {
			return res, fmt.Errorf("restore settings: %w", err)
		}
		res.Settings = true
	}
	for _, name := range profile.Names(profs) {
		if strings.TrimSpace(name) == "" {
			continue
		}
		if err := profile.Save(name, profs[name]); err != nil {
			return res, fmt.Errorf("restore profile %q: %w", name, err)
		}
		res.Profiles++
	}
	if agentVals != nil {
		restoreAgents(agentVals, &res)
	}
	return res, nil
}

// hasSection reports whether a raw section carries anything: absent, empty
// and JSON null all mean the backup has no such section.
func hasSection(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && !bytes.Equal(t, []byte("null"))
}

func validID(id string) bool {
	return id != "" && id != "magpie" && id == provider.Slug(id)
}

func restoreProviders(b Bundle, res *Result) error {
	local := map[string]provider.Provider{}
	for _, p := range provider.All() {
		if p.Account == nil {
			local[p.ID] = p
		}
	}

	// Whether each id was already on this machine before the restore.
	existed := map[string]bool{}
	for id := range local {
		existed[id] = true
	}

	// A repeated id in the bundle is written once, with its last entry; the
	// key falls back to whatever an earlier entry for that id supplied.
	var order []string // ids in the order first seen, for stable output
	final := map[string]provider.Provider{}
	for _, bp := range b.Providers {
		p := bp
		p.ID = strings.ToLower(strings.TrimSpace(p.ID))
		if !validID(p.ID) {
			continue
		}
		old, had := local[p.ID]
		if p.Key == "" && len(p.Keys) == 0 && had {
			// The backup carries no key: keep this machine's own.
			p.Key, p.KeyName, p.KeyProtocol, p.Keys = old.Key, old.KeyName, old.KeyProtocol, old.Keys
		}
		if strings.TrimSpace(p.Name) == "" {
			p.Name = p.ID
		}
		if _, ok := final[p.ID]; !ok {
			order = append(order, p.ID)
		}
		final[p.ID] = p
		local[p.ID] = p
	}
	if len(order) == 0 {
		return nil
	}
	put := make([]provider.Provider, 0, len(order))
	added, replaced := 0, 0
	for _, id := range order {
		put = append(put, final[id])
		if existed[id] {
			replaced++
		} else {
			added++
		}
	}

	// Icons are written before the providers that name them.
	used := map[string]bool{}
	for _, p := range put {
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			used[name] = true
		}
	}
	for name, data := range b.Icons {
		path := provider.IconFile(name)
		if path == "" || !used[name] || len(data) == 0 || len(data) > maxIcon {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			continue // a picture already here is kept
		}
		if err := os.MkdirAll(provider.IconDir(), 0o755); err != nil {
			return fmt.Errorf("restore icons: %w", err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fmt.Errorf("restore icon: %w", err)
		}
	}

	if err := provider.Put(put); err != nil {
		return fmt.Errorf("restore providers: %w", err)
	}
	res.Added, res.Replaced = added, replaced
	for _, id := range order {
		p := local[id]
		if p.Key == "" && len(p.Keys) == 0 {
			res.NeedKey = append(res.NeedKey, p.Name)
		}
	}
	return nil
}

// agentRank orders keys so providers are set before models, and models
// before the other fields that hang on them.
func agentRank(k string) int {
	switch {
	case strings.HasSuffix(k, ".provider"):
		return 0
	case strings.HasSuffix(k, ".model"):
		return 1
	}
	return 2
}

func restoreAgents(vals map[string]string, res *Result) {
	agents := map[string]*agent.Agent{}
	for _, a := range agent.All() {
		agents[a.ID] = a
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
	for _, k := range keys {
		v := vals[k]
		id, field, ok := strings.Cut(k, ".")
		if !ok || v == "" {
			continue
		}
		a := agents[id]
		if a == nil {
			res.Skipped = append(res.Skipped, k+"：本机不支持该 Agent")
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
			res.Skipped = append(res.Skipped, k+"：本机不支持该设置项")
			continue
		}
		if f.Get() == v {
			continue
		}
		if err := f.Set(v); err != nil {
			res.Skipped = append(res.Skipped, k+"：模型暂时不可用")
			continue
		}
		res.Agents++
	}
}
