// Package backup gathers everything the user set up in magpie — providers
// with their keys and pictures, settings, saved profiles and the model each
// agent uses — into one passphrase-sealed file, and puts it back on another
// machine.
//
// Subscriptions are not part of it: a signed-in agent's account lives in the
// agent's own files, and each machine signs in for itself.
package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Bundle is what a backup holds.
type Bundle struct {
	Version int    `json:"version"`
	App     string `json:"app,omitempty"` // the magpie that made it

	Providers []provider.Provider `json:"providers,omitempty"`
	// Icons are the pictures providers point at ("file:<name>"), by name.
	Icons map[string][]byte `json:"icons,omitempty"`
	// Settings is nil when the machine had none saved.
	Settings *settings.Settings        `json:"settings,omitempty"`
	Profiles map[string]profile.Profile `json:"profiles,omitempty"`
	// Agents is every installed agent's settings, "agent.field" → value,
	// as a profile spells them.
	Agents map[string]string `json:"agents,omitempty"`
}

// Parts picks what Restore puts back.
type Parts struct {
	Providers, Settings, Profiles, Agents bool
}

// All is every part.
var All = Parts{Providers: true, Settings: true, Profiles: true, Agents: true}

// Result is what Restore did.
type Result struct {
	Added, Replaced int      // providers new to this machine, and ones replaced
	NeedKey         []string // names of providers left without a key
	Icons           int      // pictures written
	Settings        bool     // the settings were put back
	Profiles        int      // profiles written
	Agents          int      // agent settings changed
	// Skipped are the agent settings not applied, each with why.
	Skipped []string
}

// Collect gathers this machine's setup. Without keys, no API key is in it,
// nor any request header that looks like a credential.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: Version, App: app}
	for _, p := range provider.Stored() {
		if !keys {
			p = stripKeys(p)
		}
		b.Providers = append(b.Providers, p)
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			path := provider.IconFile(name)
			if path == "" {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil || len(data) == 0 || len(data) > provider.MaxIcon {
				continue
			}
			if b.Icons == nil {
				b.Icons = map[string][]byte{}
			}
			b.Icons[name] = data
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

// credentialWords mark a request header as carrying a secret.
var credentialWords = []string{"auth", "key", "token", "secret", "cookie", "session", "password", "passwd", "credential", "signature"}

// Credential reports whether a request header name looks like it carries a
// secret: Authorization, X-Api-Key, Cookie, …
func Credential(header string) bool {
	h := strings.ToLower(header)
	for _, w := range credentialWords {
		if strings.Contains(h, w) {
			return true
		}
	}
	return false
}

func stripKeys(p provider.Provider) provider.Provider {
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
	var hs map[string]string
	for k, v := range p.Headers {
		if Credential(k) {
			continue
		}
		if hs == nil {
			hs = map[string]string{}
		}
		hs[k] = v
	}
	p.Headers = hs
	return p
}

func hasKey(p provider.Provider) bool { return p.Key != "" || len(p.Keys) > 0 }

// Restore puts the chosen parts of a backup on this machine: providers
// first, so the agents' models that go through them resolve, then their
// pictures, the settings, the profiles and last the agents.
func Restore(b Bundle, parts Parts) (Result, error) {
	var res Result
	if b.Version > Version {
		return res, ErrTooNew
	}
	if parts.Providers {
		if err := restoreProviders(b, &res); err != nil {
			return res, err
		}
		if err := restoreIcons(b, &res); err != nil {
			return res, err
		}
	}
	if parts.Settings && b.Settings != nil {
		if err := settings.Save(*b.Settings); err != nil {
			return res, fmt.Errorf("settings: %w", err)
		}
		res.Settings = true
	}
	if parts.Profiles && len(b.Profiles) > 0 {
		for _, name := range profile.Names(b.Profiles) {
			if strings.TrimSpace(name) == "" {
				continue
			}
			if err := profile.Save(name, b.Profiles[name]); err != nil {
				return res, fmt.Errorf("profile %q: %w", name, err)
			}
			res.Profiles++
		}
	}
	if parts.Agents && len(b.Agents) > 0 {
		restoreAgents(b.Agents, &res)
	}
	return res, nil
}

func restoreProviders(b Bundle, res *Result) error {
	local := map[string]provider.Provider{}
	for _, p := range provider.Stored() {
		local[p.ID] = p
	}
	for _, p := range b.Providers {
		if !provider.ValidID(p.ID) {
			continue
		}
		if old, ok := local[p.ID]; ok && !hasKey(p) {
			// a backup without keys keeps the ones this machine has
			p.Key, p.KeyName, p.KeyProtocol, p.Keys = old.Key, old.KeyName, old.KeyProtocol, old.Keys
			for k, v := range old.Headers {
				if !Credential(k) || hasHeader(p.Headers, k) {
					continue
				}
				if p.Headers == nil {
					p.Headers = map[string]string{}
				}
				p.Headers[k] = v
			}
		}
		replaced, err := provider.Put(p)
		if err != nil {
			return fmt.Errorf("provider %s: %w", p.ID, err)
		}
		if replaced {
			res.Replaced++
		} else {
			res.Added++
		}
		if !hasKey(p) && !p.Ready() {
			name := p.Name
			if name == "" {
				name = p.ID
			}
			res.NeedKey = append(res.NeedKey, name)
		}
	}
	return nil
}

func hasHeader(h map[string]string, name string) bool {
	for k := range h {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// restoreIcons writes the pictures this machine doesn't have yet; one it
// has is left alone, and one too big or oddly named is passed over.
func restoreIcons(b Bundle, res *Result) error {
	names := make([]string, 0, len(b.Icons))
	for n := range b.Icons {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		data := b.Icons[name]
		path := provider.IconFile(name) // "" for any name that could escape the folder
		if path == "" || len(data) == 0 || len(data) > provider.MaxIcon {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		res.Icons++
	}
	return nil
}

// restoreAgents sets each agent setting one by one on the agents this
// machine has: providers before models, models before the fields that hang
// on them, as profile.Apply does. One that fails is skipped, the rest go on.
func restoreAgents(vals map[string]string, res *Result) {
	agents := map[string]*agent.Agent{}
	for _, a := range agent.All() {
		agents[a.ID] = a
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
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
	detected := map[string]bool{}
	for _, k := range keys {
		id, field, ok := strings.Cut(k, ".")
		if !ok {
			res.Skipped = append(res.Skipped, k+": not an agent setting")
			continue
		}
		a := agents[id]
		if a == nil {
			res.Skipped = append(res.Skipped, k+": magpie doesn't know this agent")
			continue
		}
		on, seen := detected[id]
		if !seen {
			on = a.Detected()
			detected[id] = on
		}
		if !on {
			res.Skipped = append(res.Skipped, k+": "+a.Name+" is not installed on this machine")
			continue
		}
		f := a.Field(field)
		if f == nil {
			res.Skipped = append(res.Skipped, k+": "+a.Name+" has no such setting")
			continue
		}
		v := vals[k]
		if f.Get() == v {
			continue
		}
		if err := setField(f, v); err != nil {
			res.Skipped = append(res.Skipped, k+": "+err.Error()+" (the model may not be available here yet)")
			continue
		}
		res.Agents++
	}
}

// setField sets a field, turning a panic in an agent's setter into an error
// so one bad value can't stop the rest.
func setField(f *agent.Field, v string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New(fmt.Sprint(r))
		}
	}()
	return f.Set(v)
}
