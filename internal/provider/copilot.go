package provider

// Copilot login support: the GitHub OAuth token from the editor's credential
// file is exchanged for a short-lived Copilot session token. The session
// token signs chat requests and lists the account's models. It is kept in
// memory only; nothing is written to dial's config or to the editor's files.

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

// ErrCopilotSignedOut is returned when the Copilot login is missing or the
// GitHub token was rejected. The caller should ask the user to sign in again.
var ErrCopilotSignedOut = errors.New("copilot: signed out, please sign in to GitHub Copilot again")

const (
	copilotIntegrationID = "vscode-chat"
	copilotEditorVersion = "vscode/1.99.3"
	copilotPluginVersion = "copilot-chat/0.26.7"
	copilotUserAgent     = "GitHubCopilotChat/0.26.7"
	copilotDefaultAPI    = "https://api.githubcopilot.com"

	// copilotRenewWithin is how close to expiry a session token is replaced.
	copilotRenewWithin = 5 * time.Minute
	// copilotFallbackLife applies when the exchange reports no expiry.
	copilotFallbackLife = 20 * time.Minute
)

var copilotHTTP = &http.Client{Timeout: 30 * time.Second}

// copilotSession is one exchanged session token. oauth records which GitHub
// token it came from, so a changed login is exchanged again.
type copilotSession struct {
	oauth   string
	token   string
	api     string
	expires time.Time
}

var (
	copilotMu    sync.Mutex // serialises exchanges; held across the HTTP call
	copilotCache copilotSession
)

// copilotSessionFor returns a usable session token, exchanging the GitHub
// token again only when none is cached or the cached one is about to expire.
func copilotSessionFor(ctx context.Context) (copilotSession, error) {
	e, ok := readCopilot()
	if !ok || e.OAuthToken == "" {
		return copilotSession{}, ErrCopilotSignedOut
	}
	copilotMu.Lock()
	defer copilotMu.Unlock()
	if c := copilotCache; c.token != "" && c.oauth == e.OAuthToken && time.Until(c.expires) > copilotRenewWithin {
		return c, nil
	}
	s, err := exchangeCopilot(ctx, e.OAuthToken)
	if err != nil {
		return copilotSession{}, err
	}
	copilotCache = s
	return s, nil
}

// exchangeCopilot trades a GitHub OAuth token for a Copilot session token.
func exchangeCopilot(ctx context.Context, oauth string) (copilotSession, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSession{}, err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", copilotUserAgent)
	res, err := copilotHTTP.Do(req)
	if err != nil {
		return copilotSession{}, fmt.Errorf("copilot: token exchange: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusUnauthorized {
		return copilotSession{}, ErrCopilotSignedOut
	}
	if res.StatusCode != http.StatusOK {
		return copilotSession{}, fmt.Errorf("copilot: token exchange failed: %s", res.Status)
	}
	var v struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(b, &v) != nil || v.Token == "" {
		return copilotSession{}, errors.New("copilot: token exchange returned no session token")
	}
	api := strings.TrimRight(v.Endpoints.API, "/")
	if api == "" {
		api = copilotDefaultAPI
	}
	expires := time.Now().Add(copilotFallbackLife)
	if v.ExpiresAt > 0 {
		expires = time.Unix(v.ExpiresAt, 0)
	}
	return copilotSession{oauth: oauth, token: v.Token, api: api, expires: expires}, nil
}

// copilotHeaders sets the identification headers the Copilot API expects.
func copilotHeaders(h http.Header) {
	h.Set("Copilot-Integration-Id", copilotIntegrationID)
	h.Set("Editor-Version", copilotEditorVersion)
	h.Set("Editor-Plugin-Version", copilotPluginVersion)
	h.Set("User-Agent", copilotUserAgent)
}

// ---- signing ---------------------------------------------------------------

type copilotChatBody struct {
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

// copilotInitiator is "user" when the last message comes from the user, and
// "agent" for anything else (tool results, assistant turns).
func copilotInitiator(body []byte) string {
	var b copilotChatBody
	if json.Unmarshal(body, &b) == nil && len(b.Messages) > 0 && b.Messages[len(b.Messages)-1].Role == "user" {
		return "user"
	}
	return "agent"
}

// copilotHasImage reports whether any message carries an image part.
func copilotHasImage(body []byte) bool {
	var b copilotChatBody
	if json.Unmarshal(body, &b) != nil {
		return false
	}
	for _, m := range b.Messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) != nil {
			continue // plain string content, or none
		}
		for _, p := range parts {
			if p.Type == "image_url" || p.Type == "image" {
				return true
			}
		}
	}
	return false
}

// signCopilot points a Chat Completions request at the session's API endpoint
// and attaches the session token and Copilot headers.
func signCopilot(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if proto != Chat {
		return fmt.Errorf("copilot: only Chat Completions is served, not %s", proto)
	}
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
	copilotHeaders(req.Header)
	req.Header.Set("X-Initiator", copilotInitiator(body))
	if copilotHasImage(body) {
		req.Header.Set("Copilot-Vision-Request", "true")
	} else {
		req.Header.Del("Copilot-Vision-Request")
	}
	return nil
}

// ---- models ----------------------------------------------------------------

// copilotModel is one entry of the Copilot model list, as far as dial reads it.
type copilotModel struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Capabilities struct {
		Type     string `json:"type"`
		Supports struct {
			ReasoningEffort []string `json:"reasoning_effort"`
		} `json:"supports"`
	} `json:"capabilities"`
	ModelPickerEnabled bool `json:"model_picker_enabled"`
	Policy             struct {
		State string `json:"state"`
	} `json:"policy"`
}

var (
	copilotInternalPrefixes = []string{"copilot-search", "exec-agent", "trajectory"}
	copilotInternalSuffixes = []string{"-secondary", "-tertiary", "-4th", "-free-auto"}
)

// copilotInternal reports whether a model id is an internal or helper model
// that is not meant for users.
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

// copilotChatModels keeps the chat models the account may use, in the order
// given. Reasoning levels are carried over as the model's Efforts.
func copilotChatModels(rows []copilotModel) []catalog.Model {
	var out []catalog.Model
	for _, r := range rows {
		if r.ID == "" || r.Capabilities.Type != "chat" || copilotInternal(r.ID) {
			continue
		}
		if !r.ModelPickerEnabled && r.Policy.State != "enabled" {
			continue // not enabled for this account
		}
		name := r.Name
		if name == "" {
			name = r.ID
		}
		out = append(out, catalog.Model{
			ID:      r.ID,
			Name:    name,
			Efforts: r.Capabilities.Supports.ReasoningEffort,
		})
	}
	return out
}

// refreshCopilot asks Copilot which chat models this account can use and
// remembers the answer (without any token) for the catalog.
func refreshCopilot(ctx context.Context) ([]catalog.Model, error) {
	s, err := copilotSessionFor(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")
	copilotHeaders(req.Header)
	res, err := copilotHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot: model list: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == http.StatusUnauthorized {
		return nil, ErrCopilotSignedOut
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("copilot: model list: %s", res.Status)
	}
	var v struct {
		Data []copilotModel `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, errors.New("copilot: model list is not JSON data")
	}
	ms := copilotChatModels(v.Data)
	if len(ms) == 0 {
		return nil, errors.New("copilot: no enabled chat models for this account")
	}
	return ms, catalog.SaveLive("copilot", s.api, ms)
}

// copilotAvailable is the Copilot model list: the last refresh when there is
// one, else the built-in list without "auto".
func copilotAvailable() []catalog.Model {
	if live, _, ok := catalog.Live("copilot"); ok {
		return live
	}
	var out []catalog.Model
	for _, m := range catalog.Builtin("copilot") {
		if m.ID != "auto" {
			out = append(out, m)
		}
	}
	return out
}
