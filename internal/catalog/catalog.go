// Package catalog knows which models exist. It reads the models.dev catalog
// (from dial's own cache or OpenCode's), Codex's model cache, and falls back
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
	ID       string // e.g. "claude-sonnet-5"
	Name     string // display name
	Provider string // models.dev provider id
	Released string // YYYY-MM-DD, used for ordering
	Efforts  []string
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
	Reasoning   []struct {
		Type   string   `json:"type"`
		Values []string `json:"values"`
	} `json:"reasoning_options"`
	Modalities struct {
		Output []string `json:"output"`
	} `json:"modalities"`
}

const modelsDevURL = "https://models.dev/api.json"

var (
	once sync.Once
	mdev map[string]mdProvider
)

// CachePath is where `dial sync` stores the models.dev catalog.
func CachePath() string {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "dial", "models.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "dial", "models.json")
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

// Sync downloads the models.dev catalog into CachePath.
func Sync(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsDevURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "dial")
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

// Provider returns the text models of one models.dev provider, newest first.
// Built-in models are merged in so the list is usable without any cache.
func Provider(id string) []Model {
	seen := map[string]bool{}
	var out []Model
	if p, ok := load()[id]; ok {
		for _, m := range p.Models {
			if !textModel(m) {
				continue
			}
			mm := Model{ID: m.ID, Name: m.Name, Provider: id, Released: m.ReleaseDate}
			for _, r := range m.Reasoning {
				if r.Type == "effort" {
					mm.Efforts = r.Values
				}
			}
			seen[m.ID] = true
			out = append(out, mm)
		}
	}
	for _, m := range builtin[id] {
		if !seen[m.ID] {
			m.Provider = id
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Released != out[j].Released {
			return out[i].Released > out[j].Released
		}
		return out[i].ID < out[j].ID
	})
	return out
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

// Codex returns the models Codex itself lists (from its local cache).
func Codex() []Model {
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".codex", "models_cache.json"))
	if err != nil {
		return builtin["codex"]
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
		return builtin["codex"]
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

// builtin keeps the picker useful with no catalog on disk.
var builtin = map[string][]Model{
	"anthropic": {
		{ID: "claude-fable-5-1", Name: "Claude Fable 5.1", Released: "2026-09-01"},
		{ID: "claude-opus-5", Name: "Claude Opus 5", Released: "2026-07-01"},
		{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", Released: "2026-07-01"},
		{ID: "claude-haiku-4-5-20251001", Name: "Claude Haiku 4.5", Released: "2025-10-01"},
	},
	"openai": {
		{ID: "gpt-5.6-sol", Name: "GPT-5.6 Sol"},
		{ID: "gpt-5.5", Name: "GPT-5.5"},
		{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex"},
	},
	"codex": {
		{ID: "gpt-6-astra", Name: "GPT-6-Astra", Efforts: []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
		{ID: "gpt-5.6-sol", Name: "GPT-5.6-Sol", Efforts: []string{"low", "medium", "high", "xhigh"}},
		{ID: "gpt-5.6-terra", Name: "GPT-5.6-Terra", Efforts: []string{"low", "medium", "high", "xhigh"}},
		{ID: "gpt-5.6-luna", Name: "GPT-5.6-Luna", Efforts: []string{"low", "medium", "high", "xhigh"}},
		{ID: "gpt-5.5", Name: "GPT-5.5", Efforts: []string{"low", "medium", "high", "xhigh"}},
	},
	"deepseek": {
		{ID: "deepseek-flash", Name: "DeepSeek V4.1 Flash", Released: "2026-09-10", Efforts: []string{"low", "high", "max"}},
		{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", Released: "2026-08-12", Efforts: []string{"low", "high", "max"}},
	},
	"google": {
		{ID: "gemini-3.1-pro", Name: "Gemini 3.1 Pro"},
		{ID: "gemini-3.5-flash", Name: "Gemini 3.5 Flash"},
		{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
		{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash"},
	},
	"copilot": {
		{ID: "auto", Name: "Let Copilot pick"},
		{ID: "claude-fable-5", Name: "Claude Fable 5"},
		{ID: "claude-opus-4.8", Name: "Claude Opus 4.8"},
		{ID: "claude-sonnet-4.5", Name: "Claude Sonnet 4.5"},
		{ID: "claude-haiku-4.5", Name: "Claude Haiku 4.5"},
		{ID: "gpt-5.6-sol", Name: "GPT-5.6 Sol"},
		{ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra"},
		{ID: "gpt-5.6-luna", Name: "GPT-5.6 Luna"},
		{ID: "gpt-5.5", Name: "GPT-5.5"},
		{ID: "gpt-5.4", Name: "GPT-5.4"},
		{ID: "gpt-5.4-mini", Name: "GPT-5.4 mini"},
		{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex"},
		{ID: "gemini-3.6-flash", Name: "Gemini 3.6 Flash"},
		{ID: "gemini-3.5-flash", Name: "Gemini 3.5 Flash"},
	},
}

// Builtin exposes a built-in list by name (e.g. "copilot").
func Builtin(name string) []Model { return builtin[name] }
