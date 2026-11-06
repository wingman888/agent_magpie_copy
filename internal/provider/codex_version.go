package provider

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The client version a Codex models list is asked with is the newest of:
// the Codex CLI installed here (`codex --version`), the one Codex CLI's cache
// records, and codexClientVersion built in. Newer models are listed only to
// a newer client, so the newest of the three is the one to ask with.

// codexVersionCommand runs `codex --version`. A variable, so a test can stand
// in for the CLI.
var codexVersionCommand = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "codex", "--version").Output()
}

// codexVersionTTL is how long the version asked with is kept.
const codexVersionTTL = 10 * time.Minute

var codexVersionCache struct {
	sync.Mutex
	v  string
	at time.Time
}

var versionNumRe = regexp.MustCompile(`\d+(\.\d+)*`)

// codexRequestVersion is the client version to ask the models list with,
// kept for codexVersionTTL.
func codexRequestVersion(ctx context.Context) string {
	c := &codexVersionCache
	c.Lock()
	defer c.Unlock()
	if c.v != "" && time.Since(c.at) < codexVersionTTL {
		return c.v
	}
	v := codexCacheVersion()
	if cli := codexCLIVersion(ctx); cli != "" {
		v = newerVersion(v, cli)
	}
	c.v, c.at = v, time.Now()
	return v
}

// codexCLIVersion is the version of the Codex CLI installed, "" when there is
// no CLI or its version can't be read.
func codexCLIVersion(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := codexVersionCommand(ctx)
	if err != nil {
		return ""
	}
	return versionNumRe.FindString(string(out))
}

// newerVersion is the later of two dotted version numbers; a string that
// isn't one is older than any that is.
func newerVersion(a, b string) string {
	if compareVersions(b, a) > 0 {
		return b
	}
	return a
}

// compareVersions is -1, 0 or 1 as a is before, the same as, or after b.
func compareVersions(a, b string) int {
	pa, pb := versionFields(a), versionFields(b)
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
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

// versionFields reads the numbers of a dotted version, "0.155.1" as 0, 155, 1.
func versionFields(s string) []int {
	m := versionNumRe.FindString(strings.TrimSpace(s))
	if m == "" {
		return nil
	}
	var out []int
	for _, f := range strings.Split(m, ".") {
		n, _ := strconv.Atoi(f)
		out = append(out, n)
	}
	return out
}
