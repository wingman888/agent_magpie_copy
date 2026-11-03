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

// codexClientVersion is the built-in Codex CLI version the models list is
// asked for, the floor when neither the installed CLI nor its cache says
// newer: the list leaves out models newer than the client asking.
const codexClientVersion = "0.154.0"

// codexModels asks the ChatGPT backend which Codex models the account's own
// plan has — a Free account lists fewer than a Plus or Pro one, and one it
// doesn't have fails with a 400. It is the list Codex CLI keeps in
// models_cache.json, but that one is of whichever account Codex CLI last
// asked with, if it ran at all.
func codexModels(ctx context.Context, sign func(context.Context, *http.Request, []byte) error) ([]catalog.Model, error) {
	u := CodexBase + "/models?client_version=" + url.QueryEscape(codexClientVersionNow())
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

// codexVersionCache holds the client version chosen, for a few minutes, so
// the installed CLI isn't run on every request.
var codexVersionCache struct {
	sync.Mutex
	at  time.Time
	ver string
}

const codexVersionTTL = 10 * time.Minute

// codexExecutable finds the codex CLI; a var so tests can fake it.
var codexExecutable = func() string {
	if p, err := exec.LookPath("codex"); err == nil {
		return p
	}
	return ""
}

var (
	codexSemverRE     = regexp.MustCompile(`\d+\.\d+\.\d+`)
	codexSemverFullRE = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

// codexClientVersionNow is the client version to ask the models list with: the
// newest of the installed Codex CLI's version, the version its cache records,
// and the built-in one. Without the CLI, the last two.
func codexClientVersionNow() string {
	c := &codexVersionCache
	c.Lock()
	defer c.Unlock()
	if c.ver != "" && time.Since(c.at) < codexVersionTTL {
		return c.ver
	}
	c.ver = newestCodexVersion(codexInstalledVersion(), codexCacheVersion(), codexClientVersion)
	c.at = time.Now()
	return c.ver
}

// codexInstalledVersion asks the installed Codex CLI its version, or "" when
// there is no CLI or it does not say.
func codexInstalledVersion() string {
	path := codexExecutable()
	if path == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return ""
	}
	return codexSemverRE.FindString(string(out))
}

// codexCacheVersion is the client version Codex CLI last asked with, or ""
// when its cache is absent or unreadable.
func codexCacheVersion() string {
	home, _ := os.UserHomeDir()
	var c struct {
		ClientVersion string `json:"client_version"`
	}
	if b, err := os.ReadFile(filepath.Join(home, ".codex", "models_cache.json")); err == nil && json.Unmarshal(b, &c) == nil {
		return c.ClientVersion
	}
	return ""
}

// newestCodexVersion is the highest of the candidates that are plain x.y.z
// versions; a candidate that isn't one is ignored, and the built-in version
// stands when none is.
func newestCodexVersion(cands ...string) string {
	best := ""
	for _, v := range cands {
		if !codexSemverFullRE.MatchString(v) {
			continue
		}
		if best == "" || compareCodexVersion(v, best) > 0 {
			best = v
		}
	}
	if best == "" {
		return codexClientVersion
	}
	return best
}

// compareCodexVersion compares two x.y.z versions numerically: -1, 0 or 1.
func compareCodexVersion(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		av, _ := strconv.Atoi(pa[i])
		bv, _ := strconv.Atoi(pb[i])
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
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
