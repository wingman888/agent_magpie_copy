package provider

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// The Codex client version the models list is asked with is the newest of:
// the installed Codex CLI's (`codex --version`), the one Codex CLI last
// wrote into its models cache, and the built-in codexClientVersion. A newer
// Codex lists models an older client version leaves out.

// codexCLIVersion reports the installed Codex CLI's version, "" when there is
// no Codex CLI or it doesn't say. A variable so tests can replace it.
var codexCLIVersion = func() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "codex", "--version").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// codexVersionNow is the clock the version cache runs on; a variable so
// tests can move it.
var codexVersionNow = time.Now

// codexVersionTTL is how long the answer about the version is kept.
const codexVersionTTL = 10 * time.Minute

var codexVersions struct {
	sync.Mutex
	v  string
	at time.Time
}

// codexRequestVersion is the client_version the models list is asked with.
func codexRequestVersion() string {
	codexVersions.Lock()
	defer codexVersions.Unlock()
	now := codexVersionNow()
	if codexVersions.v != "" && now.Sub(codexVersions.at) < codexVersionTTL {
		return codexVersions.v
	}
	v := newestCodexVersion(codexCLIVersion(), codexCacheVersion(), codexClientVersion)
	codexVersions.v, codexVersions.at = v, now
	return v
}

// codexCacheVersion is the client version Codex CLI last asked with, "" when
// its models cache says none.
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

var semver = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// parseSemver reads the first major.minor.patch in s.
func parseSemver(s string) ([3]int, string, bool) {
	m := semver.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, "", false
	}
	var v [3]int
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, "", false
		}
		v[i] = n
	}
	return v, m[0], true
}

// newestCodexVersion is the newest of the given versions by major, minor
// and patch, as written; the ones that don't read as a version are skipped,
// and the built-in default is the answer when none does.
func newestCodexVersion(cands ...string) string {
	best, bestText, found := [3]int{}, "", false
	for _, c := range cands {
		v, text, ok := parseSemver(c)
		if !ok {
			continue
		}
		if !found || less(best, v) {
			best, bestText, found = v, text, true
		}
	}
	if !found {
		return codexClientVersion
	}
	return bestText
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
