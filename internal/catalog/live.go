package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A vendor's own /models endpoint is the truth about what it serves today;
// models.dev lags and keeps legacy names around. dial asks the vendor when
// it has a key, remembers the answer next to the models.dev cache, and lets
// the catalog fill in display names and reasoning levels.

// LivePath is where the fetched model list of one provider is kept.
func LivePath(provider string) string {
	return filepath.Join(filepath.Dir(CachePath()), "models", provider+".json")
}

type liveFile struct {
	Fetched time.Time `json:"fetched"`
	Base    string    `json:"base"`
	Models  []Model   `json:"models"`
}

// Live returns the model list last fetched from the provider, if any.
func Live(provider string) (models []Model, fetched time.Time, ok bool) {
	b, err := os.ReadFile(LivePath(provider))
	if err != nil {
		return nil, time.Time{}, false
	}
	var f liveFile
	if json.Unmarshal(b, &f) != nil || len(f.Models) == 0 {
		return nil, time.Time{}, false
	}
	return f.Models, f.Fetched, true
}

// SaveLive stores a fetched list; an empty list forgets it.
func SaveLive(provider, base string, models []Model) error {
	p := LivePath(provider)
	if len(models) == 0 {
		err := os.Remove(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(liveFile{Fetched: time.Now(), Base: base, Models: models}, "", "  ")
	return os.WriteFile(p, b, 0o644)
}

// Fetch asks an endpoint for its models. base is an API base URL of any
// flavour (…/v1, …/anthropic, …/api); the usual list paths are tried
// around it. The result keeps the server's order.
func Fetch(ctx context.Context, base, key string) ([]Model, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil, errors.New("no base URL")
	}
	var urls []string
	add := func(u string) {
		for _, x := range urls {
			if x == u {
				return
			}
		}
		urls = append(urls, u)
	}
	add(base + "/models")
	add(base + "/v1/models")
	root := base
	for _, suffix := range []string{"/anthropic", "/apps/anthropic", "/api/anthropic", "/v1", "/api", "/api/v1"} {
		if strings.HasSuffix(root, suffix) {
			root = strings.TrimSuffix(root, suffix)
		}
	}
	add(root + "/v1/models")
	add(root + "/models")

	var lastErr error
	for _, u := range urls {
		ms, err := fetchOne(ctx, u, key)
		if err == nil && len(ms) > 0 {
			return ms, nil
		}
		if err != nil {
			lastErr = err
		}
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no model list at " + base)
	}
	return nil, lastErr
}

func fetchOne(ctx context.Context, url, key string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, res.Status)
	}
	var v struct {
		Data   []liveModel `json:"data"`
		Models []liveModel `json:"models"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("%s: not a model list", url)
	}
	rows := v.Data
	if len(rows) == 0 {
		rows = v.Models
	}
	var out []Model
	for _, r := range rows {
		id := r.ID
		if id == "" {
			id = r.Name
		}
		if id == "" || !textModel(mdModel{ID: id}) {
			continue
		}
		name := r.DisplayName
		if name == "" {
			name = id
		}
		out = append(out, Model{ID: id, Name: name})
	}
	return out, nil
}

type liveModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// Decorate fills in names and reasoning levels for live models from the
// catalog's entry for the same id, keeping the live order.
func Decorate(live []Model, known []Model) []Model {
	byID := make(map[string]Model, len(known))
	for _, m := range known {
		byID[m.ID] = m
	}
	out := make([]Model, 0, len(live))
	for _, m := range live {
		if k, ok := byID[m.ID]; ok {
			if m.Name == "" || m.Name == m.ID {
				m.Name = k.Name
			}
			m.Efforts, m.Released, m.Provider = k.Efforts, k.Released, k.Provider
		}
		out = append(out, m)
	}
	return out
}
