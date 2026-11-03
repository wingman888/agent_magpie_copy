package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// copilotTokenURL trades a GitHub token for a short-lived Copilot session.
const copilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

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

// copilotSession trades the GitHub token for a Copilot session token and
// returns it with the API base the session names.
func copilotSession(ctx context.Context, oauth string) (token, api string, err error) {
	b, code, err := copilotGet(ctx, copilotTokenURL, "token "+oauth, nil)
	if err != nil {
		return "", "", err
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return "", "", errCopilotSession
	}
	if code != http.StatusOK {
		return "", "", fmt.Errorf("copilot token: HTTP %d", code)
	}
	var v struct {
		Token     string `json:"token"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(b, &v) != nil || v.Token == "" {
		return "", "", errCopilotSession
	}
	api = strings.TrimRight(v.Endpoints.API, "/")
	if api == "" {
		api = copilotBase
	}
	return v.Token, api, nil
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
