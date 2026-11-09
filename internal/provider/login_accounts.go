package provider

// Remembered accounts that are used alongside the signed-in one ("on").
//
// The account an agent is signed in as is reached through the agent's own
// store, as before. Every other account that is on signs with the tokens magpie
// keeps for it in accounts/accounts.json: they are refreshed there, and never
// written into the agent's files. Each refresh token thereby has one holder:
// the agent for the current account, the store for the others. loginMu is held
// across a stored account's refresh so that a switch cannot interleave with it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// AccountRef is the candidate key of a remembered account. It is not a key
// fingerprint, so it cannot collide with one.
func AccountRef(user string) string {
	return "account:" + strings.ToLower(strings.TrimSpace(user))
}

// OnAccounts lists the accounts a request to p may be sent with, in the order
// to try them: the account the agent is signed in as first, then the other
// remembered accounts that are on, in the order they were saved.
//
// Only Codex has more than one account in use at a time. A Claude Code
// request runs through the Claude Code binary, which can only use the account
// the agent is signed in as, so Claude gets p alone.
func OnAccounts(p Provider) []Provider {
	if p.Account == nil || p.Account.Agent != "codex" {
		return []Provider{p}
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	out := []Provider{p}
	for _, r := range loadLogins().Accounts {
		if r.Agent != "codex" || !r.On || r.Creds == nil || sameLogin(r.User, p.Account.User) {
			continue
		}
		out = append(out, codexStoredAccount(p, r.User, r.Plan))
	}
	return out
}

// codexStoredAccount is p as signed in to a remembered Codex account.
func codexStoredAccount(base Provider, user, plan string) Provider {
	acct := &Account{Agent: "codex", Stream: true, User: user, Plan: plan}
	acct.sign = func(ctx context.Context, req *http.Request, _ []byte) error {
		tok, accountID, err := storedCodexToken(ctx, user)
		if err != nil {
			return err
		}
		codexSignRequest(req, tok, accountID)
		return nil
	}
	acct.body = codexBody
	acct.models = catalog.Codex
	acct.fetch = func(context.Context) ([]catalog.Model, error) { return catalog.Codex(), nil }
	base.Account = acct
	return base
}

// storedCodexToken returns a usable access token for a remembered Codex
// account, refreshing it when needed and keeping the rotated tokens in the store.
func storedCodexToken(ctx context.Context, user string) (tok, accountID string, err error) {
	loginMu.Lock()
	defer loginMu.Unlock()
	f := loadLogins()
	i := findLogin(f, "codex", user)
	if i < 0 || f.Accounts[i].Creds == nil {
		return "", "", fmt.Errorf("no saved Codex sign-in for %s; sign in to it with magpie accounts add codex", user)
	}
	var raw map[string]any
	if json.Unmarshal(f.Accounts[i].Creds, &raw) != nil || raw == nil {
		return "", "", errors.New("the saved Codex sign-in is unreadable")
	}
	toks, _ := raw["tokens"].(map[string]any)
	str := func(k string) string { s, _ := toks[k].(string); return s }
	access, refresh := str("access_token"), str("refresh_token")
	accountID = str("account_id")
	if accountID == "" {
		accountID = claimString(jwtClaims(str("id_token")), "https://api.openai.com/auth", "chatgpt_account_id")
	}
	if access == "" {
		return "", "", fmt.Errorf("the saved Codex sign-in for %s has no token; sign in to it again", user)
	}
	if exp, _ := jwtClaims(access)["exp"].(float64); exp == 0 || time.Until(time.Unix(int64(exp), 0)) > 5*time.Minute {
		return access, accountID, nil
	}
	if refresh == "" {
		return "", "", fmt.Errorf("the saved Codex sign-in for %s expired; sign in to it again", user)
	}
	fresh, err := codexRefreshCall(ctx, refresh)
	if err != nil {
		return "", "", err
	}
	toks["access_token"] = fresh.AccessToken
	if fresh.IDToken != "" {
		toks["id_token"] = fresh.IDToken
	}
	if fresh.RefreshToken != "" {
		toks["refresh_token"] = fresh.RefreshToken
	}
	raw["tokens"] = toks
	raw["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(raw)
	if err != nil {
		return "", "", err
	}
	f.Accounts[i].Creds = b
	if err := saveLogins(f); err != nil {
		return "", "", err
	}
	return fresh.AccessToken, accountID, nil
}

// storedClaudeToken returns a usable access token for a remembered Claude Code
// account. The gateway does not send Claude requests with it; the usage query does.
func storedClaudeToken(ctx context.Context, user string) (string, error) {
	loginMu.Lock()
	defer loginMu.Unlock()
	f := loadLogins()
	i := findLogin(f, "claude", user)
	if i < 0 || f.Accounts[i].Creds == nil {
		return "", fmt.Errorf("no saved Claude Code sign-in for %s; sign in to it with magpie accounts add claude", user)
	}
	var auth claudeAuth
	var oauth map[string]any
	if json.Unmarshal(f.Accounts[i].Creds, &auth) != nil || json.Unmarshal(f.Accounts[i].Creds, &oauth) != nil || oauth == nil {
		return "", errors.New("the saved Claude Code sign-in is unreadable")
	}
	if claudeFresh(claudeCredentials{OAuth: auth}) {
		return auth.AccessToken, nil
	}
	if auth.RefreshToken == "" {
		return "", fmt.Errorf("the saved Claude Code sign-in for %s expired; sign in to it again", user)
	}
	access, rotated, expiresIn, err := claudeRefreshCall(ctx, auth.RefreshToken)
	if err != nil {
		return "", err
	}
	oauth["accessToken"] = access
	if rotated != "" {
		oauth["refreshToken"] = rotated
	}
	if expiresIn > 0 {
		oauth["expiresAt"] = time.Now().Add(time.Duration(expiresIn) * time.Second).UnixMilli()
	}
	b, err := json.Marshal(oauth)
	if err != nil {
		return "", err
	}
	f.Accounts[i].Creds = b
	if err := saveLogins(f); err != nil {
		return "", err
	}
	return access, nil
}
