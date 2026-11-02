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
// when Codex CLI has not asked itself yet: the list leaves out models newer
// than the client asking.
const codexClientVersion = "0.154.0"

// codexModels asks the ChatGPT backend which Codex models the account's own
// plan has — a Free account lists fewer than a Plus or Pro one, and one it
// doesn't have fails with a 400. It is the list Codex CLI keeps in
// models_cache.json, but that one is of whichever account Codex CLI last
// asked with, if it ran at all.
func codexModels(ctx context.Context, sign func(context.Context, *http.Request, []byte) error) ([]catalog.Model, error) {
	u := CodexBase + "/models?client_version=" + url.QueryEscape(codexAskVersion())
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

// codexAskVersion is the client version the models list is asked for: the
// newest of the Codex CLI installed here, the one Codex CLI last asked with
// and magpie's own default — the list leaves out models newer than the
// client asking, and Codex CLI's cache can be older than the CLI itself.
func codexAskVersion() string {
	return newestVersion(codexCLIVersion(), codexCacheVersion(), codexClientVersion)
}

// codexCacheVersion is the client version Codex CLI last asked with, ""
// when it has not asked.
func codexCacheVersion() string {
	home, _ := os.UserHomeDir()
	var c struct {
		ClientVersion string `json:"client_version"`
	}
	if b, err := os.ReadFile(filepath.Join(home, ".codex", "models_cache.json")); err == nil && json.Unmarshal(b, &c) == nil {
		return strings.TrimSpace(c.ClientVersion)
	}
	return ""
}

// codexVersionTTL is how long the installed Codex CLI's version is trusted.
var codexVersionTTL = 10 * time.Minute

var codexVersionCache struct {
	sync.Mutex
	key string // the binary asked: its path, size and time
	at  time.Time
	v   string
}

var semverRe = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?(?:-[0-9A-Za-z.\-]+)?`)

// CodexExecutable finds the Codex CLI, "" when it isn't installed; a var so
// tests can fake it.
var CodexExecutable = func() string {
	if p, err := exec.LookPath("codex"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{filepath.Join(home, ".local", "bin", "codex"), "/usr/local/bin/codex", "/opt/homebrew/bin/codex"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// codexExecutable is the Codex CLI asked for its version; a var so tests can
// fake it without touching CodexExecutable.
var codexExecutable = func() string { return CodexExecutable() }

// forgetCodexVersion drops the cached Codex CLI version.
func forgetCodexVersion() {
	c := &codexVersionCache
	c.Lock()
	c.key, c.at, c.v = "", time.Time{}, ""
	c.Unlock()
}

// codexCLIVersion is what `codex --version` says, "" when Codex CLI isn't
// installed or doesn't say.
func codexCLIVersion() string {
	path := codexExecutable()
	if path == "" {
		return ""
	}
	key := path
	if st, err := os.Stat(path); err == nil {
		key = fmt.Sprintf("%s|%d|%d", path, st.Size(), st.ModTime().UnixNano())
	}
	c := &codexVersionCache
	c.Lock()
	if c.key == key && time.Since(c.at) < codexVersionTTL {
		v := c.v
		c.Unlock()
		return v
	}
	c.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, path, "--version").Output()
	v := semverRe.FindString(string(out))
	c.Lock()
	c.key, c.at, c.v = key, time.Now(), v
	c.Unlock()
	return v
}

// newestVersion is the newest of some dotted versions, "" ones skipped.
func newestVersion(vs ...string) string {
	best := ""
	for _, v := range vs {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		if v == "" {
			continue
		}
		if best == "" || compareVersions(v, best) > 0 {
			best = v
		}
	}
	return best
}

// compareVersions orders two dotted versions; a pre-release ("-beta")
// comes before the release it leads to.
func compareVersions(a, b string) int {
	ac, apre, _ := strings.Cut(a, "-")
	bc, bpre, _ := strings.Cut(b, "-")
	as, bs := strings.Split(ac, "."), strings.Split(bc, ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case apre == bpre:
		return 0
	case apre == "":
		return 1
	case bpre == "":
		return -1
	}
	return strings.Compare(apre, bpre)
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
