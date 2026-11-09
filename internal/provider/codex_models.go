package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// codexClientVersion is the Codex CLI version the models list is asked for
// when neither the installed CLI nor its cache says one: the list leaves out
// models newer than the client asking.
const codexClientVersion = "0.154.0"

// codexVersionTTL is how long the version the models list is asked with is kept.
const codexVersionTTL = 10 * time.Minute

// codexCLIVersion is the version of the Codex CLI installed here, as
// `codex --version` says it, or "" when there is no CLI. A var so tests can
// replace it.
var codexCLIVersion = func() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "codex", "--version").Output()
	if err != nil {
		return ""
	}
	return codexVersionIn(string(out))
}

// codexCacheVersion is the client version Codex CLI last asked with, as its
// models cache records it, or "" when there is no cache. A var so tests can
// replace it.
var codexCacheVersion = func() string {
	home, _ := os.UserHomeDir()
	var c struct {
		ClientVersion string `json:"client_version"`
	}
	if b, err := os.ReadFile(filepath.Join(home, ".codex", "models_cache.json")); err == nil && json.Unmarshal(b, &c) == nil {
		return codexVersionIn(c.ClientVersion)
	}
	return ""
}

var codexVersions struct {
	sync.Mutex
	v  string
	at time.Time
}

// codexVersionNow is the client version to ask the models list with: the
// newest of the installed CLI's, the cache's and the built-in default. It is
// kept for a while, as running the CLI on every ask would be wasteful.
func codexVersionNow() string {
	c := &codexVersions
	c.Lock()
	defer c.Unlock()
	if c.v != "" && time.Since(c.at) < codexVersionTTL {
		return c.v
	}
	best := codexClientVersion
	for _, v := range []string{codexCLIVersion(), codexCacheVersion()} {
		if v != "" && codexVersionNewer(v, best) {
			best = v
		}
	}
	c.v, c.at = best, time.Now()
	return best
}

var codexVersionRE = regexp.MustCompile(`\d+(\.\d+)+`)

// codexVersionIn is the first version number in s, "" when there is none.
func codexVersionIn(s string) string {
	return codexVersionRE.FindString(s)
}

// codexVersionNewer reports whether version a is newer than b, comparing the
// numbers of each dot-separated part, so 0.155.1 is newer than 0.154.0.
func codexVersionNewer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// codexModels asks the ChatGPT backend which Codex models the account's own
// plan has — a Free account lists fewer than a Plus or Pro one, and one it
// doesn't have fails with a 400. It is the list Codex CLI keeps in
// models_cache.json, but that one is of whichever account Codex CLI last
// asked with, if it ran at all.
func codexModels(ctx context.Context, sign func(context.Context, *http.Request, []byte) error) ([]catalog.Model, error) {
	u := CodexBase + "/models?client_version=" + url.QueryEscape(codexVersionNow())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if err := sign(ctx, req, nil); err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ChatGPT models: %s", resp.Status)
	}
	ms := parseCodexModels(b)
	if len(ms) == 0 {
		return nil, errors.New("ChatGPT listed no Codex models")
	}
	return ms, nil
}

// parseCodexModels reads the backend's list, the listed ones in its order.
func parseCodexModels(b []byte) []catalog.Model {
	var list struct {
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
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	sort.SliceStable(list.Models, func(i, j int) bool { return list.Models[i].Priority < list.Models[j].Priority })
	var out []catalog.Model
	for _, m := range list.Models {
		if m.Slug == "" || m.Visibility == "hide" {
			continue
		}
		mm := catalog.Model{ID: m.Slug, Name: m.DisplayName, Provider: "openai"}
		for _, l := range m.Levels {
			mm.Efforts = append(mm.Efforts, l.Effort)
		}
		out = append(out, mm)
	}
	return out
}

// accountModels names where one account's own model list is kept.
func accountModels(agent, user string) string {
	return agent + "@" + keyID(strings.ToLower(user))
}

// Lists reports whether the account's plan has the model, as far as magpie
// knows: one whose list was never fetched is taken to have them all.
func (a *Account) Lists(model string) bool {
	if a == nil {
		return true
	}
	live, _, ok := catalog.Live(accountModels(a.Agent, a.User))
	if !ok {
		return true
	}
	return slices.ContainsFunc(live, func(m catalog.Model) bool { return m.ID == model })
}

// codexFetchSaved asks for the models of each saved ChatGPT account that
// stands behind the one Codex is signed in to, with that account's own
// sign-in, so the gateway doesn't send one a model its plan lacks. One that
// can't be asked now keeps what it listed last.
func codexFetchSaved(ctx context.Context) {
	for _, l := range Logins("codex") {
		if l.Active || !l.On {
			continue
		}
		user := l.User
		sign := codexSign(func(ctx context.Context) (string, string, error) { return savedLoginToken(ctx, "codex", user) })
		if ms, err := codexModels(ctx, sign); err == nil {
			catalog.SaveLive(accountModels("codex", user), CodexBase, ms)
		}
	}
}
