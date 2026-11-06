package agent

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/yetone/dial/internal/provider"
)

// The stash remembers what an agent's config said before dial pointed it
// at the gateway, so switching back restores it instead of guessing.

func stashPath() string { return filepath.Join(filepath.Dir(provider.Path()), "stash.json") }

// stashSet marks a key whose original setting existed, even when its value
// was empty. stash() never writes empty values, so claude and codex entries
// never carry it.
const stashSet = ".set"

func stashLoad() map[string]string {
	out := map[string]string{}
	if b, err := os.ReadFile(stashPath()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

func stashWrite(m map[string]string) {
	b, _ := json.MarshalIndent(m, "", "  ")
	os.MkdirAll(filepath.Dir(stashPath()), 0o755)
	os.WriteFile(stashPath(), b, 0o600)
}

func stash(kv map[string]string) {
	m := stashLoad()
	for k, v := range kv {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	stashWrite(m)
}

// original is one setting as it was found: present says whether it existed,
// so an empty value that was there is put back as empty, and an absent one
// is removed.
type original struct {
	value   string
	present bool
}

// stashOriginals records the originals exactly, replacing older entries for the same keys.
func stashOriginals(kv map[string]original) {
	m := stashLoad()
	for k, o := range kv {
		delete(m, k)
		delete(m, k+stashSet)
		if o.present {
			m[k] = o.value
			m[k+stashSet] = "1"
		}
	}
	stashWrite(m)
}

// peek reads a stashed original without removing it.
func peek(key string) (string, bool) {
	m := stashLoad()
	v, ok := m[key]
	_, marked := m[key+stashSet]
	return v, marked || (ok && v != "")
}

// forget drops stashed values without restoring them.
func forget(keys ...string) {
	m := stashLoad()
	for _, k := range keys {
		delete(m, k)
		delete(m, k+stashSet)
	}
	stashWrite(m)
}

// unstash reads and removes one stashed value (claude and codex). Gemini uses
// peek and forget so that its stash is cleared only after its writes succeed.
func unstash(key string) string {
	m := stashLoad()
	v := m[key]
	if v != "" {
		delete(m, key)
		stashWrite(m)
	}
	return v
}
