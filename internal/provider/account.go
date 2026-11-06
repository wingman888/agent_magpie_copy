package provider

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/dial/internal/catalog"
)

// Agent accounts. Codex (ChatGPT) and Copilot (GitHub) sign-ins come with
// models, so dial serves them as providers "codex" and "copilot" while the
// agent stays signed in. Nothing of the sign-in is copied into dial's files:
// every use reads the agent's own credentials again. The provider file keeps
// only the user's model picks for these two.

// claudeID is Claude Code's sign-in. dial detects it and never serves it.
const claudeID = "claude"

// accountHosts is the host each account provider shows for itself.
var accountHosts = map[string]string{"codex": "chatgpt.com", "copilot": "api.githubcopilot.com"}

func isAccount(id string) bool { _, ok := accountHosts[id]; return ok }

// accounts lists the agent sign-ins dial serves right now. A sign-in that is
// gone (the agent logged out) is absent, whatever the file says.
func accounts() []Provider {
	picks := map[string][]string{}
	for _, p := range load().Providers {
		picks[p.ID] = p.Models
	}
	var out []Provider
	if p, ok := codexAccount(); ok {
		p.Models = cleanList(picks["codex"])
		out = append(out, p)
	}
	if p, ok := copilotAccount(); ok {
		p.Models = cleanList(picks["copilot"])
		out = append(out, p)
	}
	return out
}

// accountSignedIn reports whether an account provider is currently signed in.
func accountSignedIn(id string) bool {
	for _, p := range accounts() {
		if p.ID == id {
			return true
		}
	}
	return false
}

// saveAccount records the model picks of an account provider and nothing
// else: no key, no name, no sign-in. The sign-in is not dial's to keep.
func saveAccount(p Provider) error {
	rec := Provider{ID: p.ID, Models: p.Models}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == p.ID {
			f.Providers[i] = rec
			return store(f)
		}
	}
	f.Providers = append(f.Providers, rec)
	return store(f)
}

// accountProtocol is the one API an account provider speaks.
func accountProtocol(acct string) Protocol {
	if acct == "copilot" {
		return Chat
	}
	return Responses
}

// Session returns an account provider ready to send one request: its token
// is current (Codex refreshes it when it is near expiry) and its upstream
// base is set. A provider that is not an account is returned unchanged.
func Session(ctx context.Context, p Provider) (Provider, error) {
	switch p.Account {
	case "codex":
		return codexSession(ctx, p)
	case "copilot":
		return copilotSessionFor(ctx, p)
	}
	return p, nil
}

// accountKnown is the model list of an account when nothing was fetched.
func accountKnown(acct string) []catalog.Model {
	var out []catalog.Model
	switch acct {
	case "codex":
		out = append(out, catalog.Codex()...)
		for _, m := range catalog.Builtin("codex") {
			if !hasModel(out, m.ID) {
				out = append(out, m)
			}
		}
	case "copilot":
		for _, m := range catalog.Builtin("copilot") {
			if m.ID != "auto" {
				out = append(out, m)
			}
		}
	}
	return out
}

func hasModel(ms []catalog.Model, id string) bool {
	for _, m := range ms {
		if m.ID == id {
			return true
		}
	}
	return false
}

// accountAvailable is what an account offers: the list it last fetched, else
// the built-in one.
func (p Provider) accountAvailable() []catalog.Model {
	known := accountKnown(p.Account)
	if live, _, ok := catalog.Live(p.ID); ok {
		return catalog.Decorate(live, known)
	}
	return known
}

// Notice is advice about an agent sign-in dial does not serve.
type Notice struct {
	Agent string `json:"agent"`
	Text  string `json:"text"`
}

// Notices lists what the user should know about signed-in agents dial leaves out.
func Notices() []Notice {
	var out []Notice
	if claudeSignedIn() {
		out = append(out, Notice{Agent: "Claude Code",
			Text: "Claude Code 已登录，但不作为 Provider 提供：它的登录令牌只供 Claude Code 自己使用，dial 只检测登录是否存在，从不读取或转发其中的凭据。需要在 dial 里使用 Claude 时，请添加 Anthropic Provider 并填入 API Key。"})
	}
	return out
}

// claudeSignedIn only checks that Claude Code's credentials file exists. Its
// contents are never read.
func claudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	_, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	return err == nil
}

// FixEventStream gives a reply that arrived without a Content-Type the
// event-stream type when its body opens like one (Codex sometimes sends it
// bare). The body is read as before, through a buffer.
func FixEventStream(res *http.Response) {
	if res.Header.Get("Content-Type") != "" {
		return
	}
	br := bufio.NewReader(res.Body)
	head, _ := br.Peek(6)
	if s := string(head); strings.HasPrefix(s, "event:") || strings.HasPrefix(s, "data:") {
		res.Header.Set("Content-Type", "text/event-stream")
	}
	res.Body = readCloser{Reader: br, Closer: res.Body}
}

type readCloser struct {
	io.Reader
	io.Closer
}
