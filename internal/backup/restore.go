package backup

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Result reports what Restore did.
type Result struct {
	Added    int      // providers new to this machine
	Replaced int      // providers that replaced one with the same id
	NeedKey  []string // names of restored providers left with no key
	Settings bool     // settings were restored
	Profiles int      // profiles written
	Agents   int      // agent settings changed
	Skipped  []string // agent settings left out, with the reason
}

// Restore applies the selected parts of b to this machine. Everything in
// the bundle is parsed before anything is written, so a damaged part fails
// the whole restore and leaves the machine untouched. Parts that are not
// selected, or that the bundle does not carry, are not touched.
func Restore(b Bundle, parts Parts) (Result, error) {
	var r Result

	var st settings.Settings
	hasSettings := parts.Settings && hasJSON(b.Settings)
	if hasSettings {
		if err := json.Unmarshal(b.Settings, &st); err != nil {
			return r, fmt.Errorf("备份中的设置无效: %w", err)
		}
	}
	var ps map[string]profile.Profile
	if parts.Profiles && hasJSON(b.Profiles) {
		if err := json.Unmarshal(b.Profiles, &ps); err != nil {
			return r, fmt.Errorf("备份中的方案无效: %w", err)
		}
	}
	var ags map[string]map[string]string
	if parts.Agents && hasJSON(b.Agents) {
		if err := json.Unmarshal(b.Agents, &ags); err != nil {
			return r, fmt.Errorf("备份中的 Agent 设置无效: %w", err)
		}
	}

	// settings.Save validates before it writes, so an invalid setting
	// stops the restore before any provider changes.
	if hasSettings {
		if err := settings.Save(st); err != nil {
			return r, err
		}
		r.Settings = true
	}
	if parts.Providers {
		if err := restoreProviders(b.Providers, b.Icons, &r); err != nil {
			return r, err
		}
	}
	if parts.Profiles {
		if err := restoreProfiles(ps, &r); err != nil {
			return r, err
		}
	}
	// Agents come last: their models refer to the providers restored above.
	if parts.Agents {
		restoreAgents(ags, &r)
	}
	return r, nil
}

// restoreProviders writes the bundle's providers into providers.json, by id.
// A provider that comes without any key keeps the local key it replaces.
func restoreProviders(ps []provider.Provider, icons map[string][]byte, r *Result) error {
	stored, err := readStored()
	if err != nil {
		return err
	}
	var touched []string
	for _, p := range ps {
		p.ID = strings.ToLower(strings.TrimSpace(p.ID))
		if !validID(p.ID) {
			continue
		}
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			p.Name = p.ID
		}
		if i := indexOf(stored, p.ID); i >= 0 {
			if !hasKey(p) {
				old := stored[i]
				p.Key, p.KeyName, p.KeyProtocol, p.Keys = old.Key, old.KeyName, old.KeyProtocol, old.Keys
			}
			stored[i] = p
			r.Replaced++
		} else {
			stored = append(stored, p)
			r.Added++
		}
		if !slices.Contains(touched, p.ID) {
			touched = append(touched, p.ID)
		}
	}
	if len(touched) == 0 {
		return nil
	}
	if err := writeStored(stored); err != nil {
		return err
	}
	for _, id := range touched {
		if p := stored[indexOf(stored, id)]; !hasKey(p) {
			r.NeedKey = append(r.NeedKey, p.Name)
		}
	}
	return restoreIcons(icons)
}

// restoreIcons writes the bundle's pictures that this machine does not have
// yet. A picture already there is kept, and an oversized or malformed one
// is skipped.
func restoreIcons(icons map[string][]byte) error {
	for _, name := range slices.Sorted(maps.Keys(icons)) {
		data := icons[name]
		path := provider.IconFile(name)
		if path == "" || len(data) == 0 || len(data) > provider.MaxIcon {
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

// restoreProfiles writes each profile by name; a profile with the same name
// is replaced, and the others are kept.
func restoreProfiles(ps map[string]profile.Profile, r *Result) error {
	for _, name := range slices.Sorted(maps.Keys(ps)) {
		if strings.TrimSpace(name) == "" {
			continue
		}
		p := ps[name]
		if p == nil {
			p = profile.Profile{}
		}
		if err := profile.Save(name, p); err != nil {
			return err
		}
		r.Profiles++
	}
	return nil
}

// restoreAgents applies each agent's model settings. Only agents installed
// here are touched. Each field is applied on its own, so one that fails
// (say, a subscription this machine is not signed in to) is skipped and the
// rest still apply.
func restoreAgents(ags map[string]map[string]string, r *Result) {
	byID := map[string]*agent.Agent{}
	for _, a := range agent.All() {
		byID[a.ID] = a
	}
	for _, id := range slices.Sorted(maps.Keys(ags)) {
		a := byID[id]
		if a == nil {
			continue // an agent this build does not know
		}
		if !a.Detected() {
			r.Skipped = append(r.Skipped, a.Name+"：本机未安装该 Agent")
			continue
		}
		vals := ags[id]
		for _, key := range fieldOrder(vals) {
			v := vals[key]
			f := a.Field(key)
			if v == "" || f == nil || f.Get() == v {
				continue
			}
			if err := f.Set(v); err != nil {
				label := f.Label
				if label == "" {
					label = f.Key
				}
				r.Skipped = append(r.Skipped, a.Name+" "+label+"：模型暂时不可用")
				continue
			}
			r.Agents++
		}
	}
}

// fieldOrder lists an agent's fields in the order they should be applied:
// the provider first, then the model, which the others hang on.
func fieldOrder(vals map[string]string) []string {
	keys := slices.Sorted(maps.Keys(vals))
	rank := func(k string) int {
		switch k {
		case "provider":
			return 0
		case "model":
			return 1
		}
		return 2
	}
	slices.SortStableFunc(keys, func(a, b string) int { return cmp.Compare(rank(a), rank(b)) })
	return keys
}

// storedProviders is the layout of providers.json, as provider keeps it.
type storedProviders struct {
	Providers []provider.Provider `json:"providers"`
}

// readStored reads providers.json as it is on disk, so entries that are not
// visible through provider.All (signed-in account picks, hidden accounts)
// are kept as they were. A file that does not parse is an error, not an
// empty list, so it is never overwritten.
func readStored() ([]provider.Provider, error) {
	b, err := os.ReadFile(provider.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取提供商: %w", err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	var f storedProviders
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s 无法解析，未做任何改动: %w", provider.Path(), err)
	}
	return f.Providers, nil
}

// writeStored writes providers.json with the same mode provider uses (0600).
// It does not go through provider.Save, which refuses a provider with no key.
func writeStored(ps []provider.Provider) error {
	path := provider.Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(storedProviders{Providers: ps}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// validID is true for an id magpie accepts for a provider. The reserved id
// "magpie" names the gateway itself, so it is never valid here.
func validID(id string) bool {
	return id != "" && id != "magpie" && id == provider.Slug(id)
}

// hasKey is true when p has a first key or any saved key.
func hasKey(p provider.Provider) bool {
	if p.Key != "" {
		return true
	}
	for _, k := range p.Keys {
		if k.Key != "" {
			return true
		}
	}
	return false
}

func indexOf(ps []provider.Provider, id string) int {
	for i, p := range ps {
		if p.ID == id {
			return i
		}
	}
	return -1
}

// hasJSON is true when a raw part is present and is not JSON null.
func hasJSON(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && !bytes.Equal(t, []byte("null"))
}
