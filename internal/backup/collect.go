package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Parts selects which parts of a Bundle Restore applies. Unselected parts
// are left exactly as they are on this machine.
type Parts struct {
	Providers, Settings, Profiles, Agents bool
}

// All selects every part.
var All = Parts{Providers: true, Settings: true, Profiles: true, Agents: true}

// iconPrefix is how a provider's Icon names a picture magpie keeps.
const iconPrefix = "file:"

// credentialWords mark a custom header name as carrying a credential.
var credentialWords = []string{"auth", "key", "token", "secret", "cookie", "session", "password"}

// Collect gathers this machine's configuration into a Bundle. With keys
// false, no API key and no credential-like header is included. app names
// the magpie build doing the collecting.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: bundleVersion, App: app}

	// provider.All also lists the signed-in agent accounts. Those are the
	// agents' own logins, so they never go into a backup.
	for _, p := range provider.All() {
		if p.Account != nil {
			continue
		}
		if !keys {
			p = withoutKeys(p)
		}
		if name, ok := iconRef(p.Icon); ok {
			if data, err := os.ReadFile(provider.IconFile(name)); err == nil {
				if b.Icons == nil {
					b.Icons = map[string][]byte{}
				}
				b.Icons[name] = data
			}
		}
		b.Providers = append(b.Providers, p)
	}

	if _, err := os.Stat(settings.Path()); err == nil {
		raw, err := json.Marshal(settings.Load())
		if err != nil {
			return Bundle{}, fmt.Errorf("编码设置: %w", err)
		}
		b.Settings = raw
	}

	ps, err := profile.Load()
	if err != nil {
		return Bundle{}, fmt.Errorf("读取方案: %w", err)
	}
	if len(ps) > 0 {
		raw, err := json.Marshal(ps)
		if err != nil {
			return Bundle{}, fmt.Errorf("编码方案: %w", err)
		}
		b.Profiles = raw
	}

	agents := map[string]map[string]string{}
	for _, a := range agent.All() {
		vals := map[string]string{}
		for k, v := range a.Values() {
			if v != "" {
				vals[k] = v
			}
		}
		if len(vals) > 0 {
			agents[a.ID] = vals
		}
	}
	if len(agents) > 0 {
		raw, err := json.Marshal(agents)
		if err != nil {
			return Bundle{}, fmt.Errorf("编码 Agent 设置: %w", err)
		}
		b.Agents = raw
	}
	return b, nil
}

// withoutKeys is p with every key taken out: the first key, the saved
// accounts, and any header whose name says it carries a credential.
// It returns a copy; p's headers map is not touched.
func withoutKeys(p provider.Provider) provider.Provider {
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
	if len(p.Headers) > 0 {
		kept := make(map[string]string, len(p.Headers))
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

// credentialHeader is true for a header name that looks like it carries a
// credential, whatever its case.
func credentialHeader(name string) bool {
	l := strings.ToLower(name)
	for _, w := range credentialWords {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// iconRef is the picture name a provider's Icon refers to, if it refers to
// one magpie keeps.
func iconRef(icon string) (string, bool) {
	name, ok := strings.CutPrefix(icon, iconPrefix)
	return name, ok && name != ""
}
