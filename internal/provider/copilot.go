package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

const (
	copilotUA         = "GitHubCopilotChat/0.26.7"
	copilotDefaultAPI = "https://api.githubcopilot.com"
	copilotLoggedOut  = "Copilot 会话已失效，请重新登录 Copilot"
)

// copilotTokenURL is where a GitHub token is exchanged for a Copilot session;
// tests point it at a fake.
var copilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

// copilotLogin is the GitHub login the editor or the Copilot CLI keeps.
type copilotLogin struct {
	user  string
	oauth string
}

func copilotAppsPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "github-copilot", "apps.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "github-copilot", "apps.json")
}

// readCopilot finds the github.com login in apps.json.
func readCopilot() (copilotLogin, bool) {
	b, err := os.ReadFile(copilotAppsPath())
	if err != nil {
		return copilotLogin{}, false
	}
	var apps map[string]struct {
		User       string `json:"user"`
		OAuthToken string `json:"oauth_token"`
	}
	if json.Unmarshal(b, &apps) != nil {
		return copilotLogin{}, false
	}
	keys := make([]string, 0, len(apps))
	for k := range apps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.HasPrefix(k, "github.com") && apps[k].OAuthToken != "" {
			return copilotLogin{user: apps[k].User, oauth: apps[k].OAuthToken}, true
		}
	}
	return copilotLogin{}, false
}

// copilotSession is a short-lived token and the API address it is good for.
type copilotSession struct {
	token string
	api   string
}

// copilotSessionFor exchanges the GitHub token for a Copilot session.
func copilotSessionFor(ctx context.Context, oauth string) (copilotSession, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotTokenURL, nil)
	if err != nil {
		return copilotSession{}, err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", copilotUA)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotSession{}, fmt.Errorf("%s（%v）", copilotLoggedOut, err)
	}
	defer res.Body.Close()
	var v struct {
		Token     string `json:"token"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&v) != nil || v.Token == "" {
		return copilotSession{}, fmt.Errorf("%s（会话返回 %s）", copilotLoggedOut, res.Status)
	}
	api := strings.TrimRight(v.Endpoints.API, "/")
	if api == "" {
		api = copilotDefaultAPI
	}
	return copilotSession{token: v.Token, api: api}, nil
}

// copilotHeaders are the headers Copilot expects from an editor client.
func copilotHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization":          "Bearer " + token,
		"Editor-Version":         "vscode/1.99.3",
		"Editor-Plugin-Version":  "copilot-chat/0.26.7",
		"Copilot-Integration-Id": "vscode-chat",
		"User-Agent":             copilotUA,
		"Openai-Intent":          "conversation-panel",
	}
}

// routeCopilot sends a chat request to the session's API address.
func routeCopilot(ctx context.Context, path string, body []byte) (Upstream, error) {
	l, ok := readCopilot()
	if !ok {
		return Upstream{}, errors.New("Copilot 未登录，请先" + signInHint(copilotID))
	}
	sess, err := copilotSessionFor(ctx, l.oauth)
	if err != nil {
		return Upstream{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return Upstream{}, fmt.Errorf("request body is not JSON: %v", err)
	}
	if s, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(s, copilotID+"/")
	}
	h := copilotHeaders(sess.token)
	h["X-Initiator"] = initiator(m)
	if hasImage(m) {
		h["Copilot-Vision-Request"] = "true"
	}
	out, err := json.Marshal(m)
	if err != nil {
		return Upstream{}, err
	}
	return Upstream{URL: sess.api + path, Header: h, Body: out}, nil
}

// initiator says whether a turn is the user's own message or a tool's reply
// sent back to the agent. Copilot bills the two differently.
func initiator(m map[string]any) string {
	msgs, _ := m["messages"].([]any)
	if len(msgs) == 0 {
		msgs, _ = m["input"].([]any)
	}
	if len(msgs) == 0 {
		return "user"
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if role, _ := last["role"].(string); role != "user" {
		return "agent"
	}
	return "user"
}

// hasImage reports whether a request carries an image part anywhere in it.
func hasImage(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if t, _ := x["type"].(string); t == "image_url" || t == "input_image" {
			return true
		}
		for _, c := range x {
			if hasImage(c) {
				return true
			}
		}
	case []any:
		for _, c := range x {
			if hasImage(c) {
				return true
			}
		}
	}
	return false
}

// copilotChatModels keeps the models this account can chat with, as Copilot's
// own list describes them: chat type, enabled for the picker, enabled by policy,
// not internal, not auto, not an embedding.
func copilotChatModels(b []byte) ([]catalog.Model, error) {
	var v struct {
		Data []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			ModelPicker  *bool  `json:"model_picker_enabled"`
			Capabilities struct {
				Type string `json:"type"`
			} `json:"capabilities"`
			Policy *struct {
				State string `json:"state"`
			} `json:"policy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, errors.New("copilot: not a model list")
	}
	var out []catalog.Model
	for _, m := range v.Data {
		id := strings.TrimSpace(m.ID)
		text := strings.ToLower(id + " " + m.Name)
		switch {
		case id == "" || id == "auto":
		case m.Capabilities.Type != "chat":
		case m.ModelPicker != nil && !*m.ModelPicker:
		case m.Policy != nil && m.Policy.State != "enabled":
		case strings.Contains(text, "internal") || strings.Contains(text, "embedding"):
		default:
			name := m.Name
			if name == "" {
				name = id
			}
			out = append(out, catalog.Model{ID: id, Name: name, Provider: "github-copilot"})
		}
	}
	return out, nil
}

// fetchCopilot asks Copilot which chat models this account can use and keeps the answer.
func fetchCopilot(ctx context.Context) ([]catalog.Model, error) {
	l, ok := readCopilot()
	if !ok {
		return nil, errors.New("Copilot 未登录，请先" + signInHint(copilotID))
	}
	sess, err := copilotSessionFor(ctx, l.oauth)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sess.api+"/models", nil)
	if err != nil {
		return nil, err
	}
	for k, v := range copilotHeaders(sess.token) {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, errors.New(copilotLoggedOut)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("copilot models: %s", res.Status)
	}
	ms, err := copilotChatModels(b)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, errors.New("this Copilot account has no chat models available")
	}
	return ms, catalog.SaveLive(copilotID, sess.api, ms)
}
