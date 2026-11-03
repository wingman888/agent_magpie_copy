package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// CopilotTokenURL trades a GitHub token for a short-lived Copilot session.
// Tests point it at a local fake server.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

// errCopilotSession is the one message for a session that no longer works.
var errCopilotSession = errors.New("Copilot 会话已失效，请重新登录 Copilot")

// copilotModel is one row of the Copilot model list; only the fields the
// filter reads.
type copilotModel struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	PickerOn     *bool  `json:"model_picker_enabled"`
	Capabilities struct {
		Type string `json:"type"`
	} `json:"capabilities"`
	Policy *struct {
		State string `json:"state"`
	} `json:"policy"`
}

// copilotChatModels keeps the models this account can use for chat. It drops
// "auto", non-chat models (embeddings and the like), models kept for internal
// use (hidden from Copilot's own picker), and models the account has not
// enabled. The server's order is kept.
func copilotChatModels(rows []copilotModel) []catalog.Model {
	var out []catalog.Model
	for _, m := range rows {
		id := strings.TrimSpace(m.ID)
		if id == "" || id == "auto" {
			continue
		}
		if m.Capabilities.Type != "chat" {
			continue
		}
		if strings.Contains(strings.ToLower(id), "embedding") {
			continue
		}
		if m.PickerOn != nil && !*m.PickerOn {
			continue
		}
		if m.Policy != nil && m.Policy.State != "" && m.Policy.State != "enabled" {
			continue
		}
		name := strings.TrimSpace(m.Name)
		if name == "" {
			name = id
		}
		out = append(out, catalog.Model{ID: id, Name: name})
	}
	return out
}

// parseCopilotModels reads a model list body ({"data":[…]}) and filters it.
func parseCopilotModels(body []byte) ([]catalog.Model, error) {
	var v struct {
		Data []copilotModel `json:"data"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("copilot: not a model list")
	}
	return copilotChatModels(v.Data), nil
}

// copilotGet performs one GET and returns the body and status code.
func copilotGet(ctx context.Context, url, auth string, extra map[string]string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	return b, res.StatusCode, nil
}

// copilotSess is a Copilot session held in memory only. It is derived from the
// GitHub login, so it is never written to disk.
type copilotSess struct {
	oauth   string    // the GitHub token it was traded from
	url     string    // the token endpoint it came from
	token   string    // the session token sent upstream
	api     string    // the API base the session names
	expires time.Time // when dial stops using it
}

// copilotCur is the one live session. Copilot sessions last about half an
// hour; a new one is traded when this one is gone or refused.
var (
	copilotMu  sync.Mutex
	copilotCur copilotSess
)

// copilotExchange trades the GitHub token for a Copilot session.
func copilotExchange(ctx context.Context, oauth string) (copilotSess, error) {
	b, code, err := copilotGet(ctx, CopilotTokenURL, "token "+oauth, nil)
	if err != nil {
		return copilotSess{}, err
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return copilotSess{}, errCopilotSession
	}
	if code != http.StatusOK {
		return copilotSess{}, fmt.Errorf("copilot token: HTTP %d", code)
	}
	var v struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(b, &v) != nil || v.Token == "" {
		return copilotSess{}, errCopilotSession
	}
	api := strings.TrimRight(v.Endpoints.API, "/")
	if api == "" {
		api = copilotBase
	}
	s := copilotSess{oauth: oauth, url: CopilotTokenURL, token: v.Token, api: api, expires: time.Now().Add(20 * time.Minute)}
	if v.ExpiresAt > 0 {
		s.expires = time.Unix(v.ExpiresAt, 0).Add(-2 * time.Minute)
	}
	return s, nil
}

// copilotSession trades the GitHub token for a Copilot session token and
// returns it with the API base the session names.
func copilotSession(ctx context.Context, oauth string) (token, api string, err error) {
	s, err := copilotExchange(ctx, oauth)
	if err != nil {
		return "", "", err
	}
	return s.token, s.api, nil
}

// copilotAuth gives the session for the signed-in Copilot account. The GitHub
// login is read from its file on every call; a live session is reused, and a
// new one is traded only when none is held.
func copilotAuth(ctx context.Context) (token, api string, err error) {
	c, ok := copilotLogin()
	if !ok {
		return "", "", errors.New("Copilot 未登录，请先登录 GitHub Copilot")
	}
	copilotMu.Lock()
	defer copilotMu.Unlock()
	if copilotCur.oauth == c.OAuthToken && copilotCur.url == CopilotTokenURL && time.Now().Before(copilotCur.expires) {
		return copilotCur.token, copilotCur.api, nil
	}
	s, err := copilotExchange(ctx, c.OAuthToken)
	if err != nil {
		return "", "", err
	}
	copilotCur = s
	return s.token, s.api, nil
}

// dropCopilotSession forgets the held session, so the next request trades again.
func dropCopilotSession() {
	copilotMu.Lock()
	defer copilotMu.Unlock()
	copilotCur = copilotSess{}
}

// copilotHeaders are the headers Copilot wants on a chat request. X-Initiator
// tells a turn the user typed (billed one way) from a turn that answers a tool
// result (billed another), by who spoke last. A request carrying an image is
// marked as a vision request.
func copilotHeaders(body []byte) map[string]string {
	h := map[string]string{
		"Editor-Version":         "vscode/1.99.0",
		"Copilot-Integration-Id": "vscode-chat",
		"X-Initiator":            "user",
	}
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil || len(v.Messages) == 0 {
		return h
	}
	if v.Messages[len(v.Messages)-1].Role != "user" {
		h["X-Initiator"] = "agent"
	}
	for _, m := range v.Messages {
		if hasImagePart(m.Content) {
			h["Copilot-Vision-Request"] = "true"
			break
		}
	}
	return h
}

// hasImagePart reports whether a chat message's content has an image part.
func hasImagePart(content json.RawMessage) bool {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return false
	}
	var parts []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(trimmed, &parts) != nil {
		return false
	}
	for _, p := range parts {
		if p.Type == "image_url" || p.Type == "image" {
			return true
		}
	}
	return false
}

// refreshCopilotModels asks the signed-in Copilot account which models it can
// use, and saves the filtered list for later reads.
func refreshCopilotModels(ctx context.Context) ([]catalog.Model, error) {
	c, ok := copilotLogin()
	if !ok {
		return nil, errors.New("Copilot 未登录，请先登录 GitHub Copilot")
	}
	tok, api, err := copilotSession(ctx, c.OAuthToken)
	if err != nil {
		return nil, err
	}
	b, code, err := copilotGet(ctx, api+"/models", "Bearer "+tok, map[string]string{
		"Editor-Version":         "vscode/1.99.0",
		"Copilot-Integration-Id": "vscode-chat",
	})
	if err != nil {
		return nil, err
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return nil, errCopilotSession
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("copilot models: HTTP %d", code)
	}
	ms, err := parseCopilotModels(b)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, errors.New("this Copilot account has no chat models enabled")
	}
	return ms, catalog.SaveLive("copilot", api, ms)
}
