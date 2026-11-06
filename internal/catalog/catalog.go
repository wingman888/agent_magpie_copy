// Package catalog knows which models exist. It reads the models.dev catalog
// (from magpie's own cache or OpenCode's), Codex's model cache, and falls back
// to a small built-in list so the picker is never empty.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Model is one entry the picker can offer.
type Model struct {
	ID          string // e.g. "claude-sonnet-5"
	Name        string // display name
	Provider    string // models.dev provider id
	Released    string // YYYY-MM-DD, used for ordering
	Efforts     []string
	Temperature *bool  // false when the model refuses temperature/top_p
	Price       *Price // USD per million tokens, when models.dev lists it
	// Keys, for a vendor whose keys each see models of their own, are the
	// keys (by fingerprint) whose list has this one; empty is every key.
	Keys []string `json:",omitempty"`
}

// Price is what a model costs, in USD per million tokens.
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// Cost of a call at this price. Reasoning tokens are billed as output by
// every vendor, and are already inside the output count.
func (p Price) Cost(input, output, cacheRead, cacheWrite int) float64 {
	return (float64(input)*p.Input + float64(output)*p.Output +
		float64(cacheRead)*p.CacheRead + float64(cacheWrite)*p.CacheWrite) / 1e6
}

type mdProvider struct {
	ID     string             `json:"id"`
	Name   string             `json:"name"`
	Env    []string           `json:"env"`
	Models map[string]mdModel `json:"models"`
}

type mdModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ReleaseDate string `json:"release_date"`
	Temperature *bool  `json:"temperature"` // false: rejects temperature/top_p
	Reasoning   []struct {
		Type   string   `json:"type"`
		Values []string `json:"values"`
	} `json:"reasoning_options"`
	Modalities struct {
		Output []string `json:"output"`
	} `json:"modalities"`
	Cost *Price `json:"cost"`
}

const modelsDevURL = "https://models.dev/api.json"

var (
	once sync.Once
	mdev map[string]mdProvider

	syncMu sync.Mutex
)

// CachePath is where `magpie sync` stores the models.dev catalog.
func CachePath() string {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "magpie", "models.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "magpie", "models.json")
}

func opencodeCache() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "opencode", "models.json")
}

// Source reports which catalog file is in use ("" when only built-ins are).
func Source() string {
	for _, p := range []string{CachePath(), opencodeCache()} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func load() map[string]mdProvider {
	once.Do(func() {
		for _, p := range []string{CachePath(), opencodeCache()} {
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			var m map[string]mdProvider
			if json.Unmarshal(b, &m) == nil && len(m) > 0 {
				mdev = m
				return
			}
		}
	})
	return mdev
}

// Reset forgets the loaded catalog so the next call re-reads the cache.
func Reset() {
	once = sync.Once{}
	mdev = nil
}

// Sync downloads the models.dev catalog into CachePath. It serializes with
// itself so a background refresh and a manual one cannot interleave writes.
func Sync(ctx context.Context) error {
	syncMu.Lock()
	defer syncMu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsDevURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "magpie")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("models.dev: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	var probe map[string]mdProvider
	if err := json.Unmarshal(b, &probe); err != nil || len(probe) == 0 {
		return fmt.Errorf("models.dev: unexpected payload")
	}
	p := CachePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return err
	}
	Reset()
	return nil
}

// Stale reports whether no catalog exists or the cache is older than a week.
func Stale() bool {
	src := Source()
	if src == "" {
		return true
	}
	st, err := os.Stat(src)
	return err != nil || time.Since(st.ModTime()) > 7*24*time.Hour
}

// ProviderEnv lists the env vars that unlock a models.dev provider.
func ProviderEnv(provider string) []string {
	if p, ok := load()[provider]; ok {
		return p.Env
	}
	return nil
}

// ProviderAvailable is true when any of the provider's API-key env vars is set.
func ProviderAvailable(provider string) bool {
	for _, e := range ProviderEnv(provider) {
		if os.Getenv(e) != "" {
			return true
		}
	}
	return false
}

// PriceOf is the list price of a models.dev provider's model, if known.
func PriceOf(providerID, modelID string) (Price, bool) {
	if p, ok := load()[providerID]; ok {
		if m, ok := p.Models[modelID]; ok && m.Cost != nil {
			return *m.Cost, true
		}
	}
	return Price{}, false
}

// Provider returns the text models of one models.dev provider, newest first.
// The list comes from the synced models.dev catalog; nothing is compiled in.
// A vendor's own /models answer, once fetched, is layered over it by the
// provider package (see provider.Provider.Available).
func Provider(id string) []Model {
	p, ok := load()[id]
	if !ok {
		return nil
	}
	var out []Model
	for _, m := range p.Models {
		if !textModel(m) {
			continue
		}
		mm := Model{ID: m.ID, Name: m.Name, Provider: id, Released: m.ReleaseDate, Price: m.Cost, Temperature: m.Temperature}
		for _, r := range m.Reasoning {
			if r.Type == "effort" {
				mm.Efforts = r.Values
			}
		}
		out = append(out, mm)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Released != out[j].Released {
			return out[i].Released > out[j].Released
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ProviderName is a models.dev provider's display name ("GitHub Copilot"
// for "github-copilot"), or "" when the catalog doesn't know it.
func ProviderName(id string) string {
	return load()[id].Name
}

func textModel(m mdModel) bool {
	if len(m.Modalities.Output) > 0 {
		text := false
		for _, o := range m.Modalities.Output {
			if o == "text" {
				text = true
			}
		}
		if !text {
			return false
		}
	}
	id := m.ID
	for _, bad := range []string{"embed", "-tts", "image", "audio", "-live", "robotics", "computer-use", "deep-research", "transcribe", "realtime", "moderation", "whisper", "dall-e", "sora"} {
		if strings.Contains(id, bad) {
			return false
		}
	}
	return true
}

// Providers returns models.dev provider ids known to the catalog.
func Providers() []string {
	var ids []string
	for id := range load() {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Codex returns the models Codex itself lists, straight from the cache the
// Codex CLI writes; there is no compiled-in list to fall back to.
func Codex() []Model {
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".codex", "models_cache.json"))
	if err != nil {
		return nil
	}
	var cache struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
			Priority    int    `json:"priority"`
			Levels      []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if json.Unmarshal(b, &cache) != nil || len(cache.Models) == 0 {
		return nil
	}
	sort.SliceStable(cache.Models, func(i, j int) bool { return cache.Models[i].Priority < cache.Models[j].Priority })
	var out []Model
	for _, m := range cache.Models {
		if m.Visibility == "hide" {
			continue
		}
		mm := Model{ID: m.Slug, Name: m.DisplayName, Provider: "openai"}
		for _, l := range m.Levels {
			mm.Efforts = append(mm.Efforts, l.Effort)
		}
		out = append(out, mm)
	}
	return out
}

// Efforts returns the reasoning levels a model supports, if known.
func Efforts(models []Model, id string) []string {
	for _, m := range models {
		if m.ID == id && len(m.Efforts) > 0 {
			return m.Efforts
		}
	}
	return nil
}
