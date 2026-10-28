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
	u := CodexBase + "/models?client_version=" + url.QueryEscape(codexCacheVersion())
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

var (
	// codexHome and codexRun are where the client version is looked for;
	// tests swap them.
	codexHome = func() string { h, _ := os.UserHomeDir(); return h }
	codexRun  = func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, "codex", "--version").Output()
	}

	codexVerMu   sync.Mutex
	codexVer     string
	codexVerTime time.Time
)

const codexVersionTTL = 10 * time.Minute

// codexCacheVersion is the client version the models list is asked with: the
// newest of the installed Codex CLI, the one in its models cache, and the
// built-in default.
func codexCacheVersion() string {
	codexVerMu.Lock()
	defer codexVerMu.Unlock()
	if codexVer != "" && time.Since(codexVerTime) < codexVersionTTL {
		return codexVer
	}
	best := codexClientVersion
	if v := codexCLIVersion(); v != "" && semverLess(best, v) {
		best = v
	}
	var c struct {
		ClientVersion string `json:"client_version"`
	}
	if b, err := os.ReadFile(filepath.Join(codexHome(), ".codex", "models_cache.json")); err == nil && json.Unmarshal(b, &c) == nil {
		if v := strings.TrimSpace(c.ClientVersion); semverParse(v) != nil && semverLess(best, v) {
			best = v
		}
	}
	codexVer, codexVerTime = best, time.Now()
	return best
}

// codexCLIVersion is what `codex --version` says ("codex-cli 0.155.1"), or ""
// when there is no Codex CLI or it fails.
func codexCLIVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := codexRun(ctx)
	if err != nil {
		return ""
	}
	for _, f := range strings.Fields(string(out)) {
		if semverParse(f) != nil {
			return f
		}
	}
	return ""
}

// semverParse reads major.minor.patch, ignoring a leading v and any
// pre-release or build suffix; nil when it isn't one.
func semverParse(v string) []int {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return nil
	}
	n := []int{0, 0, 0}
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return nil
		}
		n[i] = x
	}
	return n
}

// semverLess reports whether a is an older version than b.
func semverLess(a, b string) bool {
	x, y := semverParse(a), semverParse(b)
	if x == nil || y == nil {
		return false
	}
	return slices.Compare(x, y) < 0
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
