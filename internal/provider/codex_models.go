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
	u := CodexBase + "/models?client_version=" + url.QueryEscape(codexVersion(ctx))
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
	var c struct {
		ClientVersion string `json:"client_version"`
	}
	if b, err := os.ReadFile(codexCachePath()); err == nil && json.Unmarshal(b, &c) == nil && c.ClientVersion != "" {
		return c.ClientVersion
	}
	return codexClientVersion
}

// codexCachePath is where Codex CLI keeps the models list it last fetched.
// A variable, so a test can point it somewhere else.
var codexCachePath = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "models_cache.json")
}

// codexCLIOutput is what `codex --version` prints. A variable, so a test
// can stand in for the CLI, or for there being none.
var codexCLIOutput = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "codex", "--version").Output()
}

// codexVersionCache keeps the version the models list was last asked with
// for codexVersionTTL, so the CLI isn't run for every list.
var codexVersionCache struct {
	sync.Mutex
	v  string
	at time.Time
}

const codexVersionTTL = 10 * time.Minute

// codexVersion is the client version the models list is asked with: the
// newest of the installed Codex CLI's version, the one its cache records,
// and the built-in one. Without a CLI it is the newest of the other two.
func codexVersion(ctx context.Context) string {
	codexVersionCache.Lock()
	defer codexVersionCache.Unlock()
	if codexVersionCache.v != "" && time.Since(codexVersionCache.at) < codexVersionTTL {
		return codexVersionCache.v
	}
	v := codexClientVersion
	for _, c := range []string{codexCacheVersion(), cliCodexVersion(ctx)} {
		if c != "" && compareVersions(c, v) > 0 {
			v = c
		}
	}
	codexVersionCache.v, codexVersionCache.at = v, time.Now()
	return v
}

// cliCodexVersion is the version `codex --version` reports, or "" when there
// is no CLI or it says none.
func cliCodexVersion(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := codexCLIOutput(ctx)
	if err != nil {
		return ""
	}
	return firstVersion(string(out))
}

var versionWord = regexp.MustCompile(`\d+(?:\.\d+)+`)

// firstVersion is the first dotted number in s ("codex-cli 0.155.1" gives
// 0.155.1), or "".
func firstVersion(s string) string {
	return versionWord.FindString(s)
}

// compareVersions compares two dotted versions part by part as numbers: -1,
// 0 or 1 as a is before, the same as, or after b. A missing part is zero.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		x, y := versionPart(pa, i), versionPart(pb, i)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

func versionPart(p []string, i int) int {
	if i >= len(p) {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimLeftFunc(p[i], func(r rune) bool { return r < '0' || r > '9' }))
	return n
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
