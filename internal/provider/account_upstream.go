package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/yetone/dial/internal/catalog"
)

// Upstream is where one request to an account provider goes, and the
// headers that prove the login.
type Upstream struct {
	Base    string
	Headers map[string]string
}

// PrepareAccount works out how to send one request to an account provider:
// it reads the agent's login afresh (refreshing it when due), and returns the
// body the upstream expects. proto is the protocol the body is in.
func (p Provider) PrepareAccount(ctx context.Context, proto Protocol, body []byte) (Upstream, []byte, error) {
	switch p.ID {
	case CodexID:
		token, acct, err := codexToken(ctx)
		if err != nil {
			return Upstream{}, nil, err
		}
		nb, err := codexBody(body)
		if err != nil {
			return Upstream{}, nil, err
		}
		return Upstream{
			Base:    p.Responses,
			Headers: map[string]string{"Authorization": "Bearer " + token, "chatgpt-account-id": acct},
		}, nb, nil
	case CopilotID:
		l, err := readCopilot()
		if err != nil {
			return Upstream{}, nil, errors.New("Copilot 未登录，请先登录 Copilot")
		}
		s, err := copilotSessionFor(ctx, l.oauth)
		if err != nil {
			return Upstream{}, nil, err
		}
		base := p.Chat
		if s.api != "" {
			base = s.api
		}
		return Upstream{Base: base, Headers: copilotHeaders(s.token, body)}, body, nil
	}
	return Upstream{}, nil, fmt.Errorf("%s is not an agent login", p.Name)
}

// AccountExpired is the error for a login the agent no longer honours. It
// also forgets any session dial kept for it.
func AccountExpired(p Provider) error {
	if p.ID == CopilotID {
		forgetCopilotSessions()
		return errCopilotOut
	}
	return errCodexOut
}

// fetchAccount refreshes an account's model list. Codex has no list to ask
// for, so it keeps its built-in one; Copilot's is read from the account.
func (p Provider) fetchAccount(ctx context.Context) ([]catalog.Model, error) {
	switch p.ID {
	case CodexID:
		return accountBuiltin(CodexID), nil
	case CopilotID:
		base, ms, err := fetchCopilotModels(ctx)
		if err != nil {
			return nil, err
		}
		if len(ms) == 0 {
			return nil, errors.New("Copilot 没有返回可用的聊天模型")
		}
		return ms, catalog.SaveLive(p.ID, base, ms)
	}
	return nil, fmt.Errorf("%s has no model list to fetch", p.Name)
}

// accountBuiltin is the list an account offers before it is refreshed.
func accountBuiltin(id string) []catalog.Model {
	if id == CodexID {
		return codexBuiltinModels()
	}
	out := make([]catalog.Model, 0, len(copilotBuiltin))
	for _, m := range copilotBuiltin {
		out = append(out, catalog.Model{ID: m, Name: m, Provider: "github-copilot"})
	}
	return out
}

// probeAccount is Test for an account: the same request a client would
// send, through the login.
func (p Provider) probeAccount(ctx context.Context, proto Protocol, model string) Result {
	body := []byte(fmt.Sprintf(`{"model":%q,"input":"hi","max_output_tokens":16,"stream":false}`, model))
	path := "/responses"
	if proto == Chat {
		body = []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`, model))
		path = "/chat/completions"
	}
	up, nb, err := p.PrepareAccount(ctx, proto, body)
	if err != nil {
		return Result{Protocol: proto, Model: model, Error: err.Error()}
	}
	return probe(ctx, proto, up.Base+path, string(nb), model, up.Headers)
}
