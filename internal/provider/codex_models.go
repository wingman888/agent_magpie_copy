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
// when neither an installed Codex CLI nor Codex's cache says a newer one: the
// list leaves out models newer than the client asking.
const codexClientVersion = "0.154.0"

// codexLookPath and codexVersionOutput find the installed Codex CLI and ask it
// its version. They are variables so a test can put in its own.
var (
	codexLookPath      = func(file string) (string, error) { return exec.LookPath(file) }
	codexVersionOutput = func(path string) ([]byte, error) { return exec.Command(path, "--version").Output() }
)

// codexVersionRE is the version in `codex --version`'s output, "codex-cli 0.155.1".
var codexVersionRE = regexp.MustCompile(`\d+\.\d+\.\d+`)

// codexVersionCache keeps the client version to ask with for a while, as
// running the CLI for each list would be needless.
var codexVersionCache struct {
	sync.Mutex
	at time.Time
	v  string
}

// codexVersionToAsk is the newest of the installed Codex CLI's version, the
// one Codex's cache records and the built-in one. It is kept for ten minutes.
func codexVersionToAsk() string {
	c := &codexVersionCache
	c.Lock()
	defer c.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < 10*time.Minute {
		return c.v
	}
	best := codexCacheVersion()
	if path, err := codexLookPath("codex"); err == nil {
		if out, err := codexVersionOutput(path); err == nil {
			if v := codexVersionRE.FindString(string(out)); v != "" && compareCodexVersion(v, best) > 0 {
				best = v
			}
		}
	}
	if compareCodexVersion(codexClientVersion, best) > 0 {
		best = codexClientVersion
	}
	c.at, c.v = time.Now(), best
	return best
}

// compareCodexVersion compares dotted versions part by part as numbers: -1, 0
// or 1 as a comes before, the same as, or after b.
func compareCodexVersion(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var av, bv int
		if i < len(pa) {
			av, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			bv, _ = strconv.Atoi(pb[i])
		}
		switch {
		case av < bv:
			return -1
		case av > bv:
			return 1
		}
	}
	return 0
}

// codexModels asks the ChatGPT backend which Codex models the account's own
// plan has — a Free account lists fewer than a Plus or Pro one, and one it
// doesn't have fails with a 400. It is the list Codex CLI keeps in
// models_cache.json, but that one is of whichever account Codex CLI last
// asked with, if it ran at all.
func codexModels(ctx context.Context, sign func(context.Context, *http.Request, []byte) error) ([]catalog.Model, error) {
	u := CodexBase + "/models?client_version=" + url.QueryEscape(codexVersionToAsk())
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

// codexCacheVersion is the client version Codex CLI last asked with.
func codexCacheVersion() string {
	home, _ := os.UserHomeDir()
	var c struct {
		ClientVersion string `json:"client_version"`
	}
	if b, err := os.ReadFile(filepath.Join(home, ".codex", "models_cache.json")); err == nil && json.Unmarshal(b, &c) == nil && c.ClientVersion != "" {
		return c.ClientVersion
	}
	return codexClientVersion
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
