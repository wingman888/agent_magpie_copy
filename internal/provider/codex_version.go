package provider

// The client version a Codex models list is asked with. The list leaves out
// the models newer than the client asking, so magpie asks with the newest
// version it can find: the Codex CLI installed here, the one Codex's cache
// records, or the built-in default.

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// codexVersionTTL is how long the version found is kept.
const codexVersionTTL = 10 * time.Minute

var codexVersions struct {
	sync.Mutex
	v  string
	at time.Time
}

// codexCLIVersion is the version of the Codex CLI installed here, "" when
// there is none. A test replaces it.
var codexCLIVersion = installedCodexVersion

var dottedVersion = regexp.MustCompile(`\d+(?:\.\d+)+`)

// installedCodexVersion is what `codex --version` reports, "" when there is no
// Codex CLI to ask.
func installedCodexVersion() string {
	path, err := exec.LookPath("codex")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return ""
	}
	return dottedVersion.FindString(string(out))
}

// compareVersions compares dotted numeric versions: -1, 0 or 1.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
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
	return 0
}

// newestCodexVersion is the highest of the versions that are not "".
func newestCodexVersion(vs ...string) string {
	best := ""
	for _, v := range vs {
		if v != "" && (best == "" || compareVersions(v, best) > 0) {
			best = v
		}
	}
	return best
}

// codexClientVersionNow is the newest of the installed Codex CLI's version,
// the cache's and the built-in default. It is kept for codexVersionTTL.
func codexClientVersionNow() string {
	codexVersions.Lock()
	defer codexVersions.Unlock()
	if codexVersions.v != "" && time.Since(codexVersions.at) < codexVersionTTL {
		return codexVersions.v
	}
	v := newestCodexVersion(codexCacheVersion(), codexClientVersion, codexCLIVersion())
	codexVersions.v, codexVersions.at = v, time.Now()
	return v
}

// forgetCodexVersion makes the next ask look the version up again.
func forgetCodexVersion() {
	codexVersions.Lock()
	codexVersions.v, codexVersions.at = "", time.Time{}
	codexVersions.Unlock()
}
