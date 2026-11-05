package provider

// The Codex client version the models list is asked for: the newest of the
// locally installed Codex CLI's, the one its cache file records, and the
// built-in default. The list leaves out models newer than the client asking,
// so a newer CLI than the cache says is the one to ask as.

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

// codexVersionTTL is how long the version resolved is kept.
const codexVersionTTL = 10 * time.Minute

var (
	codexVersionMu sync.Mutex
	codexVersionAt time.Time
	codexVersion   string

	codexSemverRE = regexp.MustCompile(`\d+\.\d+\.\d+`)
)

// codexVersionFromCLI is the version the locally installed Codex CLI reports,
// from `codex --version`. A var so tests can stand in for the CLI.
var codexVersionFromCLI = func() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "codex", "--version").Output()
	if err != nil {
		return "", err
	}
	v := codexSemverRE.FindString(string(out))
	if v == "" {
		return "", errors.New("codex --version reported no version")
	}
	return v, nil
}

// codexClientVersionNow is the client version to ask the models list with:
// the newest of the CLI's, the cache's and the default, kept for a while.
func codexClientVersionNow() string {
	codexVersionMu.Lock()
	if codexVersion != "" && time.Since(codexVersionAt) < codexVersionTTL {
		v := codexVersion
		codexVersionMu.Unlock()
		return v
	}
	codexVersionMu.Unlock()
	// the CLI is asked outside the lock: it may take a moment, and other
	// callers should not wait behind it
	best := codexCacheVersion() // the cache's, or the default when there is none
	if compareClaudeVersion(codexClientVersion, best) > 0 {
		best = codexClientVersion
	}
	if v, err := codexVersionFromCLI(); err == nil && compareClaudeVersion(v, best) > 0 {
		best = v
	}
	codexVersionMu.Lock()
	codexVersion, codexVersionAt = best, time.Now()
	codexVersionMu.Unlock()
	return best
}
