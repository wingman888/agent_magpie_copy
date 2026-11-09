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

// codexCacheVersion is the client version Codex CLI last asked with, or the
// built-in one when its cache says none.
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

// codexVersionCLI is the version the installed Codex CLI reports, if there
// is one. The real probe keeps its answer for ten minutes; replacing this
// variable takes effect at once.
var codexVersionCLI = codexVersionOfCLI

// codexVersionTTL is how long the Codex CLI's version is kept.
const codexVersionTTL = 10 * time.Minute

var codexCLI struct {
	sync.Mutex
	at      time.Time
	version string
	ok      bool
}

// codexVersionOfCLI runs `codex --version` and reads the version from it.
func codexVersionOfCLI() (string, bool) {
	codexCLI.Lock()
	defer codexCLI.Unlock()
	if !codexCLI.at.IsZero() && time.Since(codexCLI.at) < codexVersionTTL {
		return codexCLI.version, codexCLI.ok
	}
	v, ok := "", false
	if path, err := exec.LookPath("codex"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, path, "--version").Output(); err == nil {
			if m := codexVersionText.Find(out); m != nil && validCodexVersion(string(m)) {
				v, ok = string(m), true
			}
		}
	}
	codexCLI.at, codexCLI.version, codexCLI.ok = time.Now(), v, ok
	return v, ok
}

var codexVersionText = regexp.MustCompile(`\d+(?:\.\d+)+`)

// codexClientVersionNow is the client version to ask the models list with:
// the newest of the installed Codex CLI's, Codex CLI's cache and the
// built-in one.
func codexClientVersionNow() string {
	best := codexClientVersion
	if v := codexCacheVersion(); codexVersionNewer(v, best) {
		best = v
	}
	if v, ok := codexVersionCLI(); ok && codexVersionNewer(v, best) {
		best = v
	}
	return best
}

// validCodexVersion says whether s is a dotted run of numbers, like 0.154.0.
func validCodexVersion(s string) bool {
	return codexVersionText.FindString(s) == s
}

// codexVersionNewer says whether version a is later than b, comparing the
// numbers of each dotted part in turn; a missing part counts as zero.
func codexVersionNewer(a, b string) bool {
	if !validCodexVersion(a) {
		return false
	}
	if !validCodexVersion(b) {
		return true
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		x, y := 0, 0
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
