package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// GitHub Copilot is signed in through the editor or the Copilot CLI, which
// keep a GitHub OAuth token in github-copilot/apps.json or hosts.json. dial
// trades that token for a short-lived Copilot session token, keeps the session
// in memory only, and talks to the endpoint the session names.

// ErrCopilotSignedOut means the GitHub Copilot sign-in is gone or was rejected.
var ErrCopilotSignedOut = errors.New("copilot is signed out: sign in again in GitHub Copilot (in your editor, or with the copilot CLI)")

// copilotRefreshWindow is how close to expiry a session token is exchanged again.
const copilotRefreshWindow = 5 * time.Minute

// copilotFallbackTTL is used when the exchange reply carries no usable expiry.
const copilotFallbackTTL = 20 * time.Minute

// copilotIdentity are the headers the Copilot API expects from an editor client.
// Copilot-Integration-Id must be present on every call.
var copilotIdentity = map[string]string{
	"Copilot-Integration-Id": "vscode-chat",
	"Editor-Version":         "vscode/1.99.3",
	"Editor-Plugin-Version":  "copilot-chat/0.26.7",
	"User-Agent":             "GitHubCopilotChat/0.26.7",
	"Openai-Intent":          "conversation-panel",
}

// copilotSession is a Copilot API session: its token and the endpoint it is for.
type copilotSession struct {
	tokenURL string // the exchange address it was obtained from
	oauth    string // the GitHub token it was exchanged from
	token    string
	api      string
	expires  time.Time
}

// copilotSessionKey identifies a cached session. The exchange address is part
// of the key, so pointing CopilotTokenURL elsewhere never reuses a session
// obtained from the old address, even for the same OAuth token.
type copilotSessionKey struct {
	tokenURL string
	oauth    string
}

var copilotCache struct {
	sync.Mutex
	// keyed by (exchange address, GitHub OAuth token), so a different sign-in
	// or a different token service gets its own session
	sessions map[copilotSessionKey]copilotSession
}

var copilotHTTP = &http.Client{Timeout: 30 * time.Second}

// copilotSessionFor returns a usable session for the current sign-in, exchanging
// the OAuth token when there is no session or it is about to expire. The lock
// is held across the exchange, so concurrent requests share one exchange.
func copilotSessionFor(ctx context.Context) (copilotSession, error) {
	login, ok := readCopilot()
	if !ok {
		return copilotSession{}, ErrCopilotSignedOut
	}
	tokenURL := CopilotTokenURL
	key := copilotSessionKey{tokenURL: tokenURL, oauth: login.oauth}
	copilotCache.Lock()
	defer copilotCache.Unlock()
	if s, ok := copilotCache.sessions[key]; ok && time.Until(s.expires) > copilotRefreshWindow {
		return s, nil
	}
	s, err := exchangeCopilot(ctx, tokenURL, login.oauth)
	if err != nil {
		return copilotSession{}, err
	}
	if copilotCache.sessions == nil {
		copilotCache.sessions = map[copilotSessionKey]copilotSession{}
	}
	copilotCache.sessions[key] = s
	return s, nil
}

// dropCopilotSession forgets the cached session for an exchange address and
// OAuth token, after the API rejected it.
func dropCopilotSession(tokenURL, oauth string) {
	copilotCache.Lock()
	defer copilotCache.Unlock()
	delete(copilotCache.sessions, copilotSessionKey{tokenURL: tokenURL, oauth: oauth})
}

// exchangeCopilot trades a GitHub OAuth token for a Copilot session token at
// the given exchange address.
func exchangeCopilot(ctx context.Context, tokenURL, oauth string) (copilotSession, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return copilotSession{}, err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	res, err := copilotHTTP.Do(req)
	if err != nil {
		return copilotSession{}, fmt.Errorf("copilot: token exchange failed: %v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return copilotSession{}, fmt.Errorf("%w (token exchange answered %s)", ErrCopilotSignedOut, res.Status)
	}
	if res.StatusCode != http.StatusOK {
		return copilotSession{}, fmt.Errorf("copilot: token exchange answered %s", res.Status)
	}
	var v struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return copilotSession{}, fmt.Errorf("copilot: token exchange reply: %v", err)
	}
	if v.Token == "" || v.Endpoints.API == "" {
		return copilotSession{}, errors.New("copilot: token exchange reply has no token or endpoint")
	}
	exp := time.Now().Add(copilotFallbackTTL)
	if v.ExpiresAt > 0 {
		exp = time.Unix(v.ExpiresAt, 0)
	}
	return copilotSession{tokenURL: tokenURL, oauth: oauth, token: v.Token,
		api: strings.TrimRight(v.Endpoints.API, "/"), expires: exp}, nil
}

// signCopilot points a chat request at the session's endpoint and sets the
// headers Copilot needs: the session bearer, its identity headers, who started
// the request, and whether it carries an image.
func signCopilot(ctx context.Context, req *http.Request, body []byte) error {
	s, err := copilotSessionFor(ctx)
	if err != nil {
		return err
	}
	u, err := url.Parse(s.api + "/chat/completions")
	if err != nil {
		return err
	}
	req.URL = u
	req.Host = u.Host
	req.Header.Set("Authorization", "Bearer "+s.token)
	for k, v := range copilotIdentity {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-Initiator", copilotInitiator(body))
	if copilotHasImage(body) {
		req.Header.Set("Copilot-Vision-Request", "true")
	}
	return nil
}

// copilotInitiator is "user" when the last message is the user's, else
// "agent" (a tool result or the assistant continuing its own turn).
func copilotInitiator(body []byte) string {
	var v struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil || len(v.Messages) == 0 {
		return "user"
	}
	if v.Messages[len(v.Messages)-1].Role == "user" {
		return "user"
	}
	return "agent"
}

// copilotHasImage reports whether any message carries an image_url part.
func copilotHasImage(body []byte) bool {
	var v struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	for _, m := range v.Messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) != nil {
			continue // a plain string, or no content
		}
		for _, p := range parts {
			if p.Type == "image_url" {
				return true
			}
		}
	}
	return false
}

// ---- model list -----------------------------------------------------------------

// copilotModelRow is one entry of the Copilot models endpoint.
type copilotModelRow struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	ModelPickerEnabled bool   `json:"model_picker_enabled"`
	Capabilities       struct {
		Type     string `json:"type"`
		Supports struct {
			ReasoningEffort []string `json:"reasoning_effort"`
		} `json:"supports"`
	} `json:"capabilities"`
	Policy struct {
		State string `json:"state"`
	} `json:"policy"`
}

// copilotInternalPrefixes and copilotInternalSuffixes mark models that exist for
// Copilot's own use, not for picking.
var (
	copilotInternalPrefixes = []string{"copilot-search", "exec-agent", "trajectory"}
	copilotInternalSuffixes = []string{"-secondary", "-tertiary", "-4th", "-free-auto"}
)

func copilotInternal(id string) bool {
	for _, p := range copilotInternalPrefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	for _, s := range copilotInternalSuffixes {
		if strings.HasSuffix(id, s) {
			return true
		}
	}
	return false
}

// copilotChatModels keeps, in the order given, the chat models this account
// may use: chat type, not internal, and enabled by GitHub for the account.
func copilotChatModels(rows []copilotModelRow) []catalog.Model {
	var out []catalog.Model
	for _, r := range rows {
		if r.ID == "" || r.Capabilities.Type != "chat" || copilotInternal(r.ID) {
			continue
		}
		if !r.ModelPickerEnabled && r.Policy.State != "enabled" {
			continue
		}
		name := r.Name
		if name == "" {
			name = r.ID
		}
		var efforts []string
		if len(r.Capabilities.Supports.ReasoningEffort) > 0 {
			efforts = append([]string(nil), r.Capabilities.Supports.ReasoningEffort...)
		}
		out = append(out, catalog.Model{ID: r.ID, Name: name, Efforts: efforts})
	}
	return out
}

// refreshCopilot asks the account which chat models it can use. It returns the
// list and the endpoint it came from; the caller remembers the list.
func refreshCopilot(ctx context.Context) ([]catalog.Model, string, error) {
	s, err := copilotSessionFor(ctx)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api+"/models", nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")
	for k, v := range copilotIdentity {
		req.Header.Set(k, v)
	}
	res, err := copilotHTTP.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("copilot: models request failed: %v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		dropCopilotSession(s.tokenURL, s.oauth)
		return nil, "", fmt.Errorf("%w (models request answered %s)", ErrCopilotSignedOut, res.Status)
	}
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("copilot: models request answered %s", res.Status)
	}
	var v struct {
		Data   []copilotModelRow `json:"data"`
		Models []copilotModelRow `json:"models"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, "", fmt.Errorf("copilot: not a model list: %v", err)
	}
	rows := v.Data
	if len(rows) == 0 {
		rows = v.Models
	}
	ms := copilotChatModels(rows)
	if len(ms) == 0 {
		return nil, "", errors.New("copilot: this account has no chat model available")
	}
	return ms, s.api, nil
}
