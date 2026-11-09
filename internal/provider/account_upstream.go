package provider

// Upstream calls for the account providers. Each request reads the agent's
// own credentials again; nothing here is cached in dial's storage. The
// callers in the gateway ask AccountUpstream for the base URL, the headers
// and the body to send.

import (
	"bytes"
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

// copilotTokenURL exchanges a GitHub token for a short-lived Copilot session.
// Tests point it at a fake.
var copilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

// copilotUserAgent identifies dial to the Copilot API.
const copilotUserAgent = "dial"

// AccountUpstream works out how to reach the upstream of an account provider
// for one request: the base URL to append the path to, the headers to send,
// and the body to send (possibly rewritten). An error carries the message the
// user should see, such as "已登出，请运行 `codex login`".
func AccountUpstream(ctx context.Context, p Provider, to Protocol, body []byte) (string, map[string]string, []byte, error) {
	switch p.ID {
	case codexID:
		token, account, err := CodexToken(ctx)
		if err != nil {
			return "", nil, nil, err
		}
		out, err := AdaptCodexBody(body)
		if err != nil {
			return "", nil, nil, err
		}
		h := map[string]string{"Authorization": "Bearer " + token}
		if account != "" {
			h["chatgpt-account-id"] = account
		}
		return p.Base(to), h, out, nil
	case copilotID:
		token, api, err := copilotSession(ctx)
		if err != nil {
			return "", nil, nil, err
		}
		h := copilotHeaders(token, body)
		return api, h, body, nil
	}
	return "", nil, nil, fmt.Errorf("%s is not an account provider", p.ID)
}

// AdaptCodexBody rewrites a Responses request for the ChatGPT Codex backend:
// the codex/ prefix goes from the model, the request is streamed and not
// stored, the fields the backend refuses are removed, and a plain string input
// becomes a list of input items.
func AdaptCodexBody(body []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	if model, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(model, codexID+"/")
	}
	m["stream"] = true
	m["store"] = false
	delete(m, "max_output_tokens")
	delete(m, "temperature")
	delete(m, "top_p")
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": s}},
		}}
	}
	return json.Marshal(m)
}

// ---- Copilot ----------------------------------------------------------------

// copilotSession trades the GitHub token for a Copilot session token and the
// API address the session names. A session that is refused means the user
// has to sign in to Copilot again.
func copilotSession(ctx context.Context) (string, string, error) {
	gh, err := CopilotGitHubToken()
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotTokenURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "token "+gh)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", copilotUserAgent)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("Copilot 会话请求失败：%v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return "", "", ErrCopilotSession
	}
	var v struct {
		Token     string `json:"token"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&v); err != nil || v.Token == "" {
		return "", "", ErrCopilotSession
	}
	api := strings.TrimRight(v.Endpoints.API, "/")
	if api == "" {
		api = copilotBase
	}
	return v.Token, api, nil
}

// copilotHeaders are the headers a Copilot chat request carries. A request
// that answers a tool call is an agent turn, not a user turn, and is billed
// differently, so X-Initiator says which it is. A request with an image is
// marked as a vision request.
func copilotHeaders(token string, body []byte) map[string]string {
	h := copilotCommonHeaders(token)
	h["X-Initiator"] = copilotInitiator(body)
	if copilotHasImage(body) {
		h["Copilot-Vision-Request"] = "true"
	}
	return h
}

func copilotCommonHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization":          "Bearer " + token,
		"Editor-Version":         "vscode/1.99.3",
		"Editor-Plugin-Version":  "copilot-chat/0.26.7",
		"Copilot-Integration-Id": "vscode-chat",
		"User-Agent":             copilotUserAgent,
	}
}

// copilotInitiator is "user" when the last message is the user's own, and
// "agent" when the request sends back a tool result or continues the turn.
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

// copilotHasImage reports whether any message part is an image.
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
			continue
		}
		for _, p := range parts {
			if p.Type == "image_url" || p.Type == "input_image" {
				return true
			}
		}
	}
	return false
}

// copilotModel is one entry of the account's model list.
type copilotModel struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	ModelPickerEnabled  *bool  `json:"model_picker_enabled"`
	ModelPickerCategory string `json:"model_picker_category"`
	Capabilities       struct {
		Type string `json:"type"`
	} `json:"capabilities"`
	Policy *struct {
		State string `json:"state"`
	} `json:"policy"`
}

// fetchCopilotModels asks the account which models it can use and keeps the
// chat models that are switched on. It returns the list and the API address
// it came from.
func fetchCopilotModels(ctx context.Context) ([]catalog.Model, string, error) {
	token, api, err := copilotSession(ctx)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/models", nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range copilotCommonHeaders(token) {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("Copilot 模型列表请求失败：%v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, "", ErrCopilotSession
	}
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("Copilot 模型列表：%s", res.Status)
	}
	var v struct {
		Data   []copilotModel `json:"data"`
		Models []copilotModel `json:"models"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, "", fmt.Errorf("Copilot 模型列表格式无法识别：%v", err)
	}
	rows := v.Data
	if len(rows) == 0 {
		rows = v.Models
	}
	ms := chatModels(rows)
	if len(ms) == 0 {
		return nil, "", errors.New("这个 Copilot 账号没有可用的聊天模型")
	}
	return ms, api, nil
}

// chatModels keeps the models that may be offered to agents: chat models
// the account has switched on, without auto, internal or embedding models.
func chatModels(rows []copilotModel) []catalog.Model {
	var out []catalog.Model
	seen := map[string]bool{}
	for _, r := range rows {
		id := strings.TrimSpace(r.ID)
		if id == "" || id == "auto" || seen[id] {
			continue
		}
		if r.Capabilities.Type != "" && r.Capabilities.Type != "chat" {
			continue // embeddings and other non-chat kinds
		}
		lower := strings.ToLower(id)
		if strings.Contains(lower, "embed") || strings.Contains(lower, "internal") || r.ModelPickerCategory == "internal" {
			continue
		}
		if r.ModelPickerEnabled != nil && !*r.ModelPickerEnabled {
			continue
		}
		if r.Policy != nil && r.Policy.State != "" && r.Policy.State != "enabled" {
			continue
		}
		name := r.Name
		if name == "" {
			name = id
		}
		seen[id] = true
		out = append(out, catalog.Model{ID: id, Name: name})
	}
	return out
}

// fetchAccount refreshes the model list of an account provider. Codex's list
// is built in, so refreshing it keeps what it has.
func (p Provider) fetchAccount(ctx context.Context) ([]catalog.Model, error) {
	if p.ID != copilotID {
		return p.accountModels(), nil
	}
	ms, base, err := fetchCopilotModels(ctx)
	if err != nil {
		return nil, err
	}
	return ms, catalog.SaveLive(p.ID, base, ms)
}
