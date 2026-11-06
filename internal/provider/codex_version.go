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

// codexCLIVersion is the version of Codex CLI installed here, as
// `codex --version` reports it, or "" when there is no Codex CLI. A variable
// so tests can stand in for the machine.
var codexCLIVersion = func() string {
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
	return strings.TrimSpace(string(out))
}

// codexVersionTTL is how long the client version asked for is kept.
const codexVersionTTL = 10 * time.Minute

var codexVersionAskedCache struct {
	sync.Mutex
	v  string
	at time.Time
}

// codexVersionAsked is the client version the Codex models list is asked
// with: the newest of Codex CLI's installed version, the one Codex CLI's
// cache records, and the built-in default. Kept for a few minutes.
func codexVersionAsked() string {
	c := &codexVersionAskedCache
	c.Lock()
	defer c.Unlock()
	if c.v != "" && time.Since(c.at) < codexVersionTTL {
		return c.v
	}
	v := latestVersion(codexCLIVersion(), codexCacheVersion(), codexClientVersion)
	if v == "" {
		v = codexClientVersion
	}
	c.v, c.at = v, time.Now()
	return v
}

// versionNumber finds the dotted version number in s, such as 0.155.1 in
// "codex-cli 0.155.1"; "" when there is none.
var versionNumber = regexp.MustCompile(`\d+(?:\.\d+)+`)

// latestVersion is the newest of the versions given, compared number by
// number (0.155.1 is newer than 0.154.0), as the number it was written as.
// Empty or unreadable ones are skipped; "" when none is readable.
func latestVersion(vs ...string) string {
	best, bestParts := "", []int(nil)
	for _, v := range vs {
		num := versionNumber.FindString(v)
		if num == "" {
			continue
		}
		parts := versionParts(num)
		if bestParts == nil || compareVersions(parts, bestParts) > 0 {
			best, bestParts = num, parts
		}
	}
	return best
}

func versionParts(num string) []int {
	var out []int
	for _, p := range strings.Split(num, ".") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

// compareVersions is -1, 0 or 1 as a is older than, the same as, or newer
// than b; a missing number counts as 0.
func compareVersions(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
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
