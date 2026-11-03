package provider

import (
	"context"
	"net/http"
)

// Upstream says where a request for provider p, speaking proto, is sent, and
// which headers prove who is asking. A key-based provider uses its key. An
// agent-backed provider uses the agent's login, read now and kept nowhere.
// body is the request about to go out; only Copilot's headers depend on it.
func Upstream(ctx context.Context, p Provider, proto Protocol, body []byte) (string, map[string]string, error) {
	if p.Account != nil {
		switch p.Account.Agent {
		case "codex":
			tok, acct, err := codexAccess(ctx)
			if err != nil {
				return "", nil, err
			}
			h := map[string]string{"Authorization": "Bearer " + tok}
			if acct != "" {
				h["chatgpt-account-id"] = acct
			}
			return CodexBase, h, nil
		case "copilot":
			tok, api, err := copilotAuth(ctx)
			if err != nil {
				return "", nil, err
			}
			h := copilotHeaders(body)
			h["Authorization"] = "Bearer " + tok
			return api, h, nil
		}
	}
	return p.Base(proto), keyHeaders(p, proto), nil
}

// UpstreamBody is the request body as the upstream must receive it. Codex
// takes a rewritten Responses body; everything else is sent as given.
func UpstreamBody(p Provider, proto Protocol, body []byte) []byte {
	if p.Account != nil && p.Account.Agent == "codex" && proto == Responses {
		return CodexBody(body)
	}
	return body
}

// AccountRejected says what to tell the user when an agent's upstream refuses
// the login (401 or 403). It returns nil for any other status and for
// key-based providers. A refused Copilot session is dropped, so the next
// request trades for a new one.
func AccountRejected(p Provider, status int) error {
	if p.Account == nil || (status != http.StatusUnauthorized && status != http.StatusForbidden) {
		return nil
	}
	switch p.Account.Agent {
	case "codex":
		return errCodexLoggedOut
	case "copilot":
		dropCopilotSession()
		return errCopilotSession
	}
	return nil
}
