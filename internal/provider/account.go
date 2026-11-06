package provider

// Account providers are the models of agents the user already signed in to:
// Codex (a ChatGPT plan) and GitHub Copilot. dial keeps no token of its own.
// Each time one is needed, the agent's login file is read, and a Codex token
// close to expiry is renewed the way Codex renews it.
//
// Claude Code is detected, never offered: only whether its login file exists
// is checked, and its contents are not read.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yetone/dial/internal/catalog"
)

const (
	codexID   = "codex"
	copilotID = "copilot"
	claudeID  = "claude"

	claudeNotOffered = "Claude Code 不作为 Provider 提供：它的登录只供 Claude Code 自己使用"
)

// CodexBase is the ChatGPT Codex backend. codex/<model> requests go to
// CodexBase + "/responses". Tests point it at a fake server.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// accountOrder is the order account providers are listed in.
var accountOrder = []string{codexID, copilotID}

// isAccountID is true for the provider ids that come from an agent's login.
func isAccountID(id string) bool { return id == codexID || id == copilotID }

// accountProviderFor builds the provider for an agent the user is signed in
// to. It only reads the agent's own login file; nothing is copied elsewhere.
func accountProviderFor(id string) (Provider, bool) {
	switch id {
	case codexID:
		l, ok := readCodex()
		if !ok {
			return Provider{}, false
		}
		return Provider{ID: codexID, Name: "Codex", Icon: "openai", Responses: CodexBase,
			Agent: codexID, Login: l.who(), Plan: l.plan}, true
	case copilotID:
		l, ok := readCopilot()
		if !ok {
			return Provider{}, false
		}
		login := l.user
		if login == "" {
			login = "GitHub account"
		}
		return Provider{ID: copilotID, Name: "GitHub Copilot", Chat: copilotDefaultAPI,
			Agent: copilotID, Login: login}, true
	}
	return Provider{}, false
}

// signInHint tells the user how to sign an agent in again.
func signInHint(id string) string {
	if id == codexID {
		return "run `codex login`"
	}
	return "sign in to GitHub Copilot in your editor or the Copilot CLI"
}

// Unavailable says why an agent the user is signed in to is not offered.
type Unavailable struct {
	Agent  string `json:"agent"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// UnavailableAgents lists the signed-in agents that dial does not offer as
// providers, with the reason. Claude Code is the only one for now.
func UnavailableAgents() []Unavailable {
	if claudeSignedIn() {
		return []Unavailable{{Agent: claudeID, Name: "Claude Code",
			Reason: "已登录但不提供：它的登录只供 Claude Code 自己使用，dial 只检测登录是否存在，不读取也不转发其内容。"}}
	}
	return nil
}

// claudeSignedIn checks that Claude Code's login file exists. The file is not opened.
func claudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	_, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	return err == nil
}

// accountRecord is all the config keeps of an account provider: the model picks.
type accountRecord struct {
	ID     string   `json:"id"`
	Models []string `json:"models,omitempty"`
}

// saveAccount records the model picks of an account provider. Only the picks
// are stored; the login stays where the agent keeps it.
func saveAccount(id string, models []string) error {
	if _, ok := accountProviderFor(id); !ok {
		return fmt.Errorf("%s is not signed in — %s first", id, signInHint(id))
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == id {
			f.Providers[i].Models = cleanList(models)
			return store(f)
		}
	}
	f.Providers = append(f.Providers, Provider{ID: id, Models: cleanList(models)})
	return store(f)
}

// deleteAccount clears the model picks of an account provider. It does not
// sign the agent out, and the provider stays listed while the login lasts.
func deleteAccount(id string) error {
	f := load()
	found := false
	for i := range f.Providers {
		if f.Providers[i].ID == id {
			f.Providers[i].Models = nil
			found = true
		}
	}
	if !found {
		if _, ok := accountProviderFor(id); !ok {
			return fmt.Errorf("no provider %q", id)
		}
		return nil
	}
	return store(f)
}

// Upstream is a request ready to leave dial for an account provider.
type Upstream struct {
	URL    string
	Header map[string]string
	Body   []byte
}

// Route prepares a request to an account provider: the agent's login is read
// from its own file, renewed if it is about to expire, and the body is shaped
// for that agent's backend. path is the client-facing path, e.g. "/responses".
func Route(ctx context.Context, p Provider, to Protocol, path string, body []byte) (Upstream, error) {
	switch p.Agent {
	case codexID:
		if to != Responses {
			return Upstream{}, errors.New("Codex only takes Responses requests")
		}
		return routeCodex(ctx, path, body)
	case copilotID:
		return routeCopilot(ctx, path, body)
	}
	return Upstream{}, fmt.Errorf("%s is not a signed-in account", p.Name)
}

// accountAvailable lists the models an account provider can serve: the live
// list when one was fetched, else the built-in one.
func accountAvailable(p Provider) []catalog.Model {
	if p.Agent == copilotID {
		if live, _, ok := catalog.Live(copilotID); ok {
			return live
		}
		var out []catalog.Model
		for _, m := range catalog.Builtin(copilotID) {
			if m.ID != "auto" {
				out = append(out, m)
			}
		}
		return out
	}
	return catalog.Builtin(codexID)
}

// fetchAccount asks an account provider for its models. Codex has no list
// endpoint dial can use, so its built-in list stands; Copilot's is fetched.
func fetchAccount(ctx context.Context, p Provider) ([]catalog.Model, error) {
	if p.Agent == codexID {
		return catalog.Builtin(codexID), nil
	}
	return fetchCopilot(ctx)
}

// testAccount sends one tiny request through the account's backend.
func (p Provider) testAccount(ctx context.Context) []Result {
	model := ""
	if ms := p.Exposed(); len(ms) > 0 {
		model = ms[0].ID
	} else if ms := p.Available(); len(ms) > 0 {
		model = ms[0].ID
	}
	speaks := p.Speaks()
	if len(speaks) == 0 {
		return nil
	}
	proto := speaks[0]
	if model == "" {
		return []Result{{Protocol: proto, Error: "no model to try: expose one, or refresh the model list"}}
	}
	path, body := "/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`, model)
	if proto == Responses {
		path, body = "/responses", fmt.Sprintf(`{"model":%q,"input":"hi","max_output_tokens":16}`, model)
	}
	up, err := Route(ctx, p, proto, path, []byte(body))
	if err != nil {
		return []Result{{Protocol: proto, Model: model, Error: err.Error()}}
	}
	return []Result{probe(ctx, proto, up.URL, string(up.Body), model, up.Header)}
}
